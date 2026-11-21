package tunnel

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// State is a tunnel's state as the app shows it.
type State string

const (
	Disconnected State = "disconnected"
	Connecting   State = "connecting" // OpenVPN: RESOLVE, TCP_CONNECT, WAIT, AUTH, GET_CONFIG, ASSIGN_IP, ADD_ROUTES
	NeedsAuth    State = "needs_auth" // waiting for credentials from the app
	Connected    State = "connected"
	Reconnecting State = "reconnecting"
	Failed       State = "failed" // stopped for a reason in Error (bad credentials, process exit)
)

// StateOf maps an OpenVPN state name (the second field of >STATE:) to ours.
// ok is false for names that don't change the state shown (EXITING is
// handled by the process ending).
func StateOf(name string) (s State, ok bool) {
	switch name {
	case "CONNECTING", "RESOLVE", "TCP_CONNECT", "WAIT", "AUTH", "GET_CONFIG", "ASSIGN_IP", "ADD_ROUTES", "AUTH_PENDING":
		return Connecting, true
	case "CONNECTED":
		return Connected, true
	case "RECONNECTING":
		return Reconnecting, true
	}
	return "", false
}

// StateEvent is a >STATE: notification.
type StateEvent struct {
	Name        string // OpenVPN's name, e.g. CONNECTED
	Description string // e.g. SUCCESS, or the reason for RECONNECTING
	LocalIP     string // the tunnel address
	RemoteIP    string // the server
}

// Handler receives what OpenVPN reports. Calls come from one goroutine, in
// order; they must not block for long.
type Handler interface {
	State(StateEvent)
	Log(line string)
	// NeedCredentials: kind is "Auth" (username and password) or "Private Key".
	NeedCredentials(kind string)
	// AuthFailed: the server or the key refused what we sent.
	AuthFailed(kind string)
	Bytes(in, out uint64)
	Fatal(msg string)
}

// Conn is a client of OpenVPN's management interface (OpenVPN listens on a
// unix socket, we connect). It turns on state and bytecount
// notifications, releases the start-up hold and answers nothing by itself:
// credentials come from Credentials/PrivateKey after NeedCredentials.
type Conn struct {
	c  net.Conn
	mu sync.Mutex // serializes writes
}

const writeTimeout = 5 * time.Second

// Dial connects to the management socket at path, retrying while OpenVPN
// creates it. It gives up after wait, or when abort closes (OpenVPN exited).
func Dial(path string, wait time.Duration, abort <-chan struct{}) (net.Conn, error) {
	deadline := time.Now().Add(wait)
	for {
		c, err := net.Dial("unix", path)
		if err == nil {
			return c, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("management socket: %w", err)
		}
		select {
		case <-abort:
			return nil, errors.New("OpenVPN exited before its management socket was ready")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// NewConn wraps an established management connection.
func NewConn(c net.Conn) *Conn { return &Conn{c: c} }

func (m *Conn) Close() error { return m.c.Close() }

// send writes one command. The caller guarantees no line breaks in cmd.
func (m *Conn) send(cmd string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = m.c.SetWriteDeadline(time.Now().Add(writeTimeout)) // a stuck OpenVPN must not hold up the caller
	_, err := io.WriteString(m.c, cmd+"\n")
	return err
}

// Quote escapes s as one management argument. A line break would end the
// command and start another one, so it is refused (credentials come from
// the user, and must never be able to inject commands).
func Quote(s string) (string, error) {
	if strings.ContainsAny(s, "\r\n\x00") {
		return "", errors.New("value contains a line break")
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`, nil
}

// Credentials answers a "Auth" request.
func (m *Conn) Credentials(user, pass string) error {
	u, err := Quote(user)
	if err != nil {
		return err
	}
	p, err := Quote(pass)
	if err != nil {
		return err
	}
	if err := m.send(`username "Auth" ` + u); err != nil {
		return err
	}
	return m.send(`password "Auth" ` + p)
}

// PrivateKey answers a "Private Key" request.
func (m *Conn) PrivateKey(pass string) error {
	p, err := Quote(pass)
	if err != nil {
		return err
	}
	return m.send(`password "Private Key" ` + p)
}

// Stop asks OpenVPN to shut down cleanly.
func (m *Conn) Stop() error { return m.send("signal SIGTERM") }

// Run reads notifications until the connection closes, calling h. It returns
// nil when OpenVPN closes the socket (it exited).
func (m *Conn) Run(h Handler) error {
	r := bufio.NewReaderSize(m.c, 64<<10)
	for {
		line, err := r.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line != "" {
			if herr := m.handle(line, h); herr != nil {
				return herr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
	}
}

func (m *Conn) handle(line string, h Handler) error {
	if line[0] != '>' {
		return nil // SUCCESS:/ERROR: answers to our commands
	}
	kind, data, _ := strings.Cut(line[1:], ":")
	switch kind {
	case "HOLD":
		// OpenVPN waits here until we are listening. Subscribe first so no
		// early state is lost. Logs come from OpenVPN's own output (the launcher), which
		// also has what it prints before the management socket exists.
		for _, c := range []string{"state on", "bytecount 2", "hold release"} {
			if err := m.send(c); err != nil {
				return err
			}
		}
	case "STATE":
		f := strings.Split(data, ",")
		if len(f) >= 2 {
			ev := StateEvent{Name: f[1]}
			if len(f) > 2 {
				ev.Description = f[2]
			}
			if len(f) > 3 {
				ev.LocalIP = f[3]
			}
			if len(f) > 4 {
				ev.RemoteIP = f[4]
			}
			h.State(ev)
		}
	case "LOG":
		if f := strings.SplitN(data, ",", 3); len(f) == 3 {
			h.Log(f[2])
		}
	case "PASSWORD":
		if k, ok := between(data, "Need '", "'"); ok {
			h.NeedCredentials(k)
		} else if k, ok := between(data, "Verification Failed: '", "'"); ok {
			h.AuthFailed(k)
		}
	case "BYTECOUNT":
		in, out, ok := strings.Cut(data, ",")
		a, e1 := strconv.ParseUint(in, 10, 64)
		b, e2 := strconv.ParseUint(out, 10, 64)
		if ok && e1 == nil && e2 == nil {
			h.Bytes(a, b)
		}
	case "FATAL":
		h.Fatal(data)
	}
	return nil
}

func between(s, pre, post string) (string, bool) {
	_, s, ok := strings.Cut(s, pre)
	if !ok {
		return "", false
	}
	s, _, ok = strings.Cut(s, post)
	return s, ok
}

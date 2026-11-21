package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// Process is a running OpenVPN.
type Process interface {
	Wait() error // returns when the process has exited
	Kill() error
}

// Launcher starts OpenVPN for a tunnel and returns the process and its
// management connection. The daemon implements it; tests fake it.
type Launcher interface {
	Start(ctx context.Context, id string) (net.Conn, Process, error)
}

// Credentials are kept in memory only, while a tunnel is wanted up.
type Credentials struct {
	User, Pass string // "Auth"
	KeyPass    string // "Private Key"
	HaveAuth   bool
	HaveKey    bool
}

// Info is a tunnel's live state.
type Info struct {
	ID       string    `json:"id"`
	State    State     `json:"state"`
	Error    string    `json:"error,omitempty"`
	Need     string    `json:"need,omitempty"` // "auth" or "key": waiting for credentials (State is needs_auth)
	LocalIP  string    `json:"local_ip,omitempty"`
	RemoteIP string    `json:"remote_ip,omitempty"`
	Since    time.Time `json:"since,omitzero"` // when it connected
	BytesIn  uint64    `json:"bytes_in,omitempty"`
	BytesOut uint64    `json:"bytes_out,omitempty"`
}

// stopWait is how long OpenVPN gets after SIGTERM before it is killed.
var stopWait = 10 * time.Second

const (
	logLines     = 500
	quickExit    = 5 * time.Second // a process that dies sooner failed to start
	maxQuickFail = 5
	healthyAfter = 30 * time.Second // a run this long resets the backoff
)

// Manager keeps a set of tunnels running. It restarts a dead OpenVPN with a
// backoff, answers its credential prompts from memory, and stops for good on
// bad credentials (retrying would lock the account).
type Manager struct {
	L       Launcher
	Backoff func(attempt int) time.Duration // default: 1s, 2s, 4s … 60s

	mu   sync.Mutex
	runs map[string]*run
}

type run struct {
	m      *Manager
	id     string
	cancel context.CancelFunc
	done   chan struct{}

	mu    sync.Mutex
	info  Info
	creds Credentials
	conn  *Conn // the current management connection, nil between runs
	final bool  // stop restarting: bad credentials
	logs  []string
}

func (m *Manager) backoff(n int) time.Duration {
	if m.Backoff != nil {
		return m.Backoff(n)
	}
	return min(time.Second<<min(n, 6), time.Minute)
}

// Connect starts the tunnel, or does nothing if it is already running.
// creds may be empty: the tunnel then asks (state needs_auth).
func (m *Manager) Connect(id string, creds Credentials) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runs == nil {
		m.runs = map[string]*run{}
	}
	if r := m.runs[id]; r != nil {
		select {
		case <-r.done:
		default:
			r.SetCredentials(creds)
			return nil // running
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &run{m: m, id: id, cancel: cancel, done: make(chan struct{}), creds: creds, info: Info{ID: id, State: Connecting}}
	m.runs[id] = r
	go r.loop(ctx)
	return nil
}

// Disconnect stops the tunnel and waits until OpenVPN has exited.
func (m *Manager) Disconnect(id string) {
	m.mu.Lock()
	r := m.runs[id]
	m.mu.Unlock()
	if r == nil {
		return
	}
	r.cancel()
	<-r.done
}

// DisconnectAll stops every tunnel (daemon shutdown).
func (m *Manager) DisconnectAll() {
	m.mu.Lock()
	var rs []*run
	for _, r := range m.runs {
		rs = append(rs, r)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, r := range rs {
		wg.Go(func() { r.cancel(); <-r.done })
	}
	wg.Wait()
}

// SetCredentials gives a waiting (or later prompting) tunnel its credentials.
func (m *Manager) SetCredentials(id string, c Credentials) error {
	m.mu.Lock()
	r := m.runs[id]
	m.mu.Unlock()
	if r == nil {
		return errors.New("the tunnel is not running")
	}
	r.SetCredentials(c)
	return nil
}

// Info is the state of one tunnel; a tunnel that was never started is
// disconnected.
func (m *Manager) Info(id string) Info {
	m.mu.Lock()
	r := m.runs[id]
	m.mu.Unlock()
	if r == nil {
		return Info{ID: id, State: Disconnected}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.info
}

// Logs returns the last lines OpenVPN logged for the tunnel.
func (m *Manager) Logs(id string) []string {
	m.mu.Lock()
	r := m.runs[id]
	m.mu.Unlock()
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.logs...)
}

// AddLog appends a line to the tunnel's log (the launcher feeds OpenVPN's
// stderr here: option errors appear before the management socket exists).
func (m *Manager) AddLog(id, line string) {
	m.mu.Lock()
	r := m.runs[id]
	m.mu.Unlock()
	if r != nil {
		(&handler{r: r}).Log(line)
	}
}

// Forget drops the record of a stopped tunnel (when it is removed).
func (m *Manager) Forget(id string) {
	// Unlist first: a Connect after this starts a fresh run instead of being
	// dropped from the map while its process lives on.
	m.mu.Lock()
	r := m.runs[id]
	delete(m.runs, id)
	m.mu.Unlock()
	if r != nil {
		r.cancel()
		<-r.done
	}
}

func (r *run) SetCredentials(c Credentials) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.HaveAuth {
		r.creds.User, r.creds.Pass, r.creds.HaveAuth = c.User, c.Pass, true
	}
	if c.HaveKey {
		r.creds.KeyPass, r.creds.HaveKey = c.KeyPass, true
	}
	if r.conn != nil && r.info.Need != "" {
		r.answerLocked(r.info.Need)
	}
}

// answerLocked sends stored credentials for need ("auth" or "key"). It
// reports whether it had them.
func (r *run) answerLocked(need string) bool {
	switch {
	case need == "auth" && r.creds.HaveAuth && r.conn != nil:
		if err := r.conn.Credentials(r.creds.User, r.creds.Pass); err != nil {
			r.failLocked(fmt.Sprintf("credentials refused: %v", err))
			return true
		}
	case need == "key" && r.creds.HaveKey && r.conn != nil:
		if err := r.conn.PrivateKey(r.creds.KeyPass); err != nil {
			r.failLocked(fmt.Sprintf("key password refused: %v", err))
			return true
		}
	default:
		return false
	}
	r.info.Need = ""
	r.info.State = Connecting
	return true
}

func (r *run) failLocked(msg string) {
	r.info.State = Failed
	r.info.Error = msg
	r.final = true
}

func (r *run) loop(ctx context.Context) {
	defer close(r.done)
	defer func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.conn = nil
		r.creds = Credentials{} // memory only, and only while the tunnel is wanted
		if r.info.State != Failed {
			r.info.State = Disconnected
		}
		r.info.Need = ""
		r.info.LocalIP, r.info.RemoteIP, r.info.Since = "", "", time.Time{}
	}()
	attempt, quick := 0, 0
	for ctx.Err() == nil {
		started := time.Now()
		err := r.once(ctx)
		if ctx.Err() != nil {
			return
		}
		r.mu.Lock()
		if r.final {
			r.mu.Unlock()
			return
		}
		last := ""
		if n := len(r.logs); n > 0 {
			last = r.logs[n-1]
		}
		lived := time.Since(started)
		if lived < quickExit {
			quick++
		} else {
			quick = 0
		}
		if lived >= healthyAfter {
			attempt = 0
		}
		msg := "OpenVPN stopped"
		if err != nil {
			msg = err.Error()
		}
		if quick >= maxQuickFail {
			r.failLocked(fmt.Sprintf("%s (last log: %s)", msg, last))
			r.mu.Unlock()
			return
		}
		r.info.State = Reconnecting
		r.info.Error = msg
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.m.backoff(attempt)):
		}
		attempt++
	}
}

// once runs OpenVPN until it exits or ctx ends.
func (r *run) once(ctx context.Context) error {
	nc, proc, err := r.m.L.Start(ctx, r.id)
	if err != nil {
		return err
	}
	conn := NewConn(nc)
	r.mu.Lock()
	r.conn = conn
	r.info.Need = ""
	if r.info.State != Reconnecting {
		r.info.State = Connecting
	}
	r.mu.Unlock()
	runDone := make(chan struct{})
	go func() { defer close(runDone); _ = conn.Run(&handler{r: r}) }()
	waitDone := make(chan error, 1)
	go func() { waitDone <- proc.Wait() }()
	var werr error
	select {
	case werr = <-waitDone:
	case <-ctx.Done():
		// The kill timer starts now, not after Stop returns: a stuck write
		// must not keep a process alive.
		go func() { _ = conn.Stop() }()
		select {
		case werr = <-waitDone:
		case <-time.After(stopWait):
			_ = proc.Kill()
			werr = <-waitDone
		}
	}
	_ = conn.Close()
	<-runDone
	r.mu.Lock()
	r.conn = nil
	r.mu.Unlock()
	if werr != nil {
		return fmt.Errorf("OpenVPN exited: %w", werr)
	}
	return nil
}

// handler turns management notifications into run state.
type handler struct{ r *run }

func (h *handler) State(ev StateEvent) {
	r := h.r
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := StateOf(ev.Name)
	if !ok {
		return
	}
	if r.info.State == Failed || r.info.State == NeedsAuth && s == Connecting && r.info.Need != "" {
		return // keep waiting for credentials
	}
	r.info.State = s
	if s == Connected {
		r.info.Error = ""
		r.info.LocalIP, r.info.RemoteIP, r.info.Since = ev.LocalIP, ev.RemoteIP, time.Now()
	}
}

func (h *handler) Log(line string) {
	r := h.r
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, line)
	if len(r.logs) > logLines {
		r.logs = append(r.logs[:0], r.logs[len(r.logs)-logLines:]...)
	}
}

func (h *handler) NeedCredentials(kind string) {
	r := h.r
	r.mu.Lock()
	defer r.mu.Unlock()
	var need string
	switch kind {
	case "Auth":
		need = "auth"
	case "Private Key":
		need = "key"
	default:
		// Never hand the VPN password to a prompt we don't know (an HTTP proxy, say).
		r.failLocked(fmt.Sprintf("OpenVPN asks for %q credentials, which splitroute can't provide", kind))
		return
	}
	if r.answerLocked(need) {
		return
	}
	r.info.Need = need
	r.info.State = NeedsAuth
}

func (h *handler) AuthFailed(kind string) {
	r := h.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if kind == "Private Key" {
		r.creds.KeyPass, r.creds.HaveKey = "", false
		r.failLocked("wrong private key password")
	} else {
		r.creds.User, r.creds.Pass, r.creds.HaveAuth = "", "", false
		r.failLocked("the server refused the username or password")
	}
}

func (h *handler) Bytes(in, out uint64) {
	r := h.r
	r.mu.Lock()
	defer r.mu.Unlock()
	r.info.BytesIn, r.info.BytesOut = in, out
}

func (h *handler) Fatal(msg string) {
	r := h.r
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, "FATAL: "+msg)
	r.info.Error = msg
}

package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeProc struct {
	once sync.Once
	done chan struct{}
	err  error
}

func newProc() *fakeProc { return &fakeProc{done: make(chan struct{})} }

func (p *fakeProc) exit(err error) { p.once.Do(func() { p.err = err; close(p.done) }) }
func (p *fakeProc) Wait() error    { <-p.done; return p.err }
func (p *fakeProc) Kill() error    { p.exit(errors.New("killed")); return nil }

// fakeOpenVPN plays one OpenVPN run: it gets the server side of the
// management connection and the process.
type fakeOpenVPN func(n int, srv net.Conn, p *fakeProc)

type fakeLauncher struct {
	starts atomic.Int32
	script fakeOpenVPN
}

func (l *fakeLauncher) Start(ctx context.Context, id string) (net.Conn, Process, error) {
	n := int(l.starts.Add(1))
	srv, cli := net.Pipe()
	p := newProc()
	go func() {
		l.script(n, srv, p)
		srv.Close()
	}()
	return cli, p, nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 400 {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func newManager(l Launcher) *Manager {
	return &Manager{L: l, Backoff: func(int) time.Duration { return time.Millisecond }}
}

// serve reads commands from srv into the channel until it closes.
func serve(srv net.Conn) <-chan string {
	ch := make(chan string, 32)
	go func() {
		defer close(ch)
		br := bufio.NewReader(srv)
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				return
			}
			ch <- strings.TrimSpace(l)
		}
	}()
	return ch
}

func expect(t *testing.T, ch <-chan string, want string) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("got command %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for command %q", want)
	}
}

func TestConnectWithCredentials(t *testing.T) {
	l := &fakeLauncher{script: func(n int, srv net.Conn, p *fakeProc) {
		cmds := serve(srv)
		fmt.Fprint(srv, ">HOLD:Waiting for hold release:0\r\n")
		for _, c := range []string{"state on", "bytecount 2", "hold release"} {
			expect(t, cmds, c)
		}
		fmt.Fprint(srv, ">STATE:1,WAIT,,,,,,\r\n>PASSWORD:Need 'Auth' username/password\r\n")
		expect(t, cmds, `username "Auth" "ali"`)
		expect(t, cmds, `password "Auth" "s3cret"`)
		fmt.Fprint(srv, ">STATE:2,CONNECTED,SUCCESS,10.8.0.2,203.0.113.5,1194,,\r\n>BYTECOUNT:10,20\r\n")
		expect(t, cmds, "signal SIGTERM")
		p.exit(nil)
	}}
	m := newManager(l)
	if err := m.Connect("a", Credentials{User: "ali", Pass: "s3cret", HaveAuth: true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "connected", func() bool { return m.Info("a").State == Connected })
	if i := m.Info("a"); i.LocalIP != "10.8.0.2" || i.RemoteIP != "203.0.113.5" || i.Since.IsZero() || i.Need != "" {
		t.Errorf("info = %+v", i)
	}
	waitFor(t, "bytes", func() bool { return m.Info("a").BytesIn == 10 })
	m.Disconnect("a")
	if s := m.Info("a").State; s != Disconnected {
		t.Errorf("state after Disconnect = %s", s)
	}
	if n := l.starts.Load(); n != 1 {
		t.Errorf("OpenVPN started %d times, want 1", n)
	}
}

func TestPromptsThenCredentialsArrive(t *testing.T) {
	l := &fakeLauncher{script: func(n int, srv net.Conn, p *fakeProc) {
		cmds := serve(srv)
		fmt.Fprint(srv, ">PASSWORD:Need 'Auth' username/password\r\n")
		expect(t, cmds, `username "Auth" "u"`)
		expect(t, cmds, `password "Auth" "p"`)
		fmt.Fprint(srv, ">STATE:1,AUTH,,,,,,\r\n")
		expect(t, cmds, "signal SIGTERM")
		p.exit(nil)
	}}
	m := newManager(l)
	_ = m.Connect("a", Credentials{})
	waitFor(t, "needs_auth", func() bool { i := m.Info("a"); return i.State == NeedsAuth && i.Need == "auth" })
	if err := m.SetCredentials("a", Credentials{User: "u", Pass: "p", HaveAuth: true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "connecting again", func() bool { i := m.Info("a"); return i.State == Connecting && i.Need == "" })
	m.Disconnect("a")
}

func TestBadCredentialsStopForGood(t *testing.T) {
	l := &fakeLauncher{script: func(n int, srv net.Conn, p *fakeProc) {
		cmds := serve(srv)
		fmt.Fprint(srv, ">PASSWORD:Need 'Auth' username/password\r\n")
		if n == 2 { // asked again after the failure: nothing is sent until the app supplies new credentials
			expect(t, cmds, "signal SIGTERM")
			p.exit(nil)
			return
		}
		expect(t, cmds, `username "Auth" "u"`)
		expect(t, cmds, `password "Auth" "bad"`)
		fmt.Fprint(srv, ">PASSWORD:Verification Failed: 'Auth'\r\n")
		time.Sleep(20 * time.Millisecond) // lives long enough to not count as a start failure; OpenVPN exits next
		p.exit(errors.New("exit status 1"))
	}}
	m := newManager(l)
	_ = m.Connect("a", Credentials{User: "u", Pass: "bad", HaveAuth: true})
	waitFor(t, "failed", func() bool { return m.Info("a").State == Failed })
	time.Sleep(50 * time.Millisecond)
	if n := l.starts.Load(); n != 1 {
		t.Errorf("started %d times after bad credentials, want 1 (no retry: it locks accounts)", n)
	}
	if i := m.Info("a"); i.State != Failed || !strings.Contains(i.Error, "username or password") {
		t.Errorf("info = %+v", i)
	}
	// Reconnecting asks again, it doesn't reuse the bad password.
	m.Disconnect("a")
	_ = m.Connect("a", Credentials{})
	waitFor(t, "second start", func() bool { return l.starts.Load() == 2 })
	waitFor(t, "asks again", func() bool { return m.Info("a").State == NeedsAuth })
	m.Disconnect("a")
}

func TestRestartsWhenOpenVPNDies(t *testing.T) {
	l := &fakeLauncher{}
	l.script = func(n int, srv net.Conn, p *fakeProc) {
		cmds := serve(srv)
		fmt.Fprint(srv, ">STATE:1,CONNECTED,SUCCESS,10.8.0.2,203.0.113.5,1194,,\r\n")
		if n == 1 {
			time.Sleep(10 * time.Millisecond)
			p.exit(errors.New("signal: killed")) // crash
			return
		}
		expect(t, cmds, "signal SIGTERM")
		p.exit(nil)
	}
	m := newManager(l)
	_ = m.Connect("a", Credentials{})
	waitFor(t, "restarted and connected", func() bool { return l.starts.Load() == 2 && m.Info("a").State == Connected })
	m.Disconnect("a")
}

func TestGivesUpOnStartFailures(t *testing.T) {
	l := &fakeLauncher{script: func(n int, srv net.Conn, p *fakeProc) {
		fmt.Fprint(srv, ">LOG:1,F,Options error: bad option\r\n")
		time.Sleep(5 * time.Millisecond)
		p.exit(errors.New("exit status 1"))
	}}
	m := newManager(l)
	_ = m.Connect("a", Credentials{})
	waitFor(t, "failed", func() bool { return m.Info("a").State == Failed })
	if n := l.starts.Load(); n != maxQuickFail {
		t.Errorf("started %d times, want %d", n, maxQuickFail)
	}
	if e := m.Info("a").Error; !strings.Contains(e, "Options error: bad option") {
		t.Errorf("error %q should carry the last log line", e)
	}
	if got := m.Logs("a"); len(got) == 0 {
		t.Error("no logs kept")
	}
}

func TestConnectIsIdempotent(t *testing.T) {
	l := &fakeLauncher{script: func(n int, srv net.Conn, p *fakeProc) {
		cmds := serve(srv)
		expect(t, cmds, "signal SIGTERM")
		p.exit(nil)
	}}
	m := newManager(l)
	_ = m.Connect("a", Credentials{})
	_ = m.Connect("a", Credentials{})
	waitFor(t, "start", func() bool { return l.starts.Load() >= 1 })
	time.Sleep(20 * time.Millisecond)
	if n := l.starts.Load(); n != 1 {
		t.Errorf("started %d times, want 1", n)
	}
	m.DisconnectAll()
	if s := m.Info("a").State; s != Disconnected {
		t.Errorf("state = %s", s)
	}
}

func TestKillWhenSigtermIgnored(t *testing.T) {
	old := stopWait
	stopWait = 30 * time.Millisecond
	defer func() { stopWait = old }()
	l := &fakeLauncher{script: func(n int, srv net.Conn, p *fakeProc) {
		cmds := serve(srv)
		expect(t, cmds, "signal SIGTERM")
		<-p.done // ignores SIGTERM; only Kill ends it
	}}
	m := newManager(l)
	_ = m.Connect("a", Credentials{})
	waitFor(t, "start", func() bool { return l.starts.Load() == 1 })
	done := make(chan struct{})
	go func() { m.Disconnect("a"); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Disconnect did not kill a process that ignores SIGTERM")
	}
}

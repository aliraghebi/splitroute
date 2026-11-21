package tunnel

import (
	"bufio"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
)

type rec struct{ events []string }

func (r *rec) State(e StateEvent) {
	r.events = append(r.events, fmt.Sprintf("state %s %s %s %s", e.Name, e.Description, e.LocalIP, e.RemoteIP))
}
func (r *rec) Log(l string)             { r.events = append(r.events, "log "+l) }
func (r *rec) NeedCredentials(k string) { r.events = append(r.events, "need "+k) }
func (r *rec) AuthFailed(k string)      { r.events = append(r.events, "failed "+k) }
func (r *rec) Bytes(i, o uint64)        { r.events = append(r.events, fmt.Sprintf("bytes %d %d", i, o)) }
func (r *rec) Fatal(m string)           { r.events = append(r.events, "fatal "+m) }

// Server output copied from OpenVPN 2.6 management-notes.txt and a real run.
func TestRunNotifications(t *testing.T) {
	srv, cli := net.Pipe()
	got := make(chan []string, 1)
	var cmds []string
	done := make(chan struct{})
	go func() { // OpenVPN
		defer close(done)
		defer srv.Close()
		fmt.Fprint(srv, ">INFO:OpenVPN Management Interface Version 5 -- type 'help' for more info\r\n")
		fmt.Fprint(srv, ">HOLD:Waiting for hold release:0\r\n")
		br := bufio.NewReader(srv)
		for range 3 { // state on, bytecount, hold release
			l, _ := br.ReadString('\n')
			cmds = append(cmds, strings.TrimSpace(l))
		}
		fmt.Fprint(srv, ">STATE:1700000000,CONNECTING,,,,,,\r\n")
		fmt.Fprint(srv, ">PASSWORD:Need 'Auth' username/password\r\n")
		fmt.Fprint(srv, ">PASSWORD:Verification Failed: 'Auth'\r\n")
		fmt.Fprint(srv, ">PASSWORD:Need 'Private Key' password\r\n")
		fmt.Fprint(srv, ">STATE:1700000001,CONNECTED,SUCCESS,10.8.0.2,203.0.113.5,1194,,\r\n")
		fmt.Fprint(srv, ">LOG:1700000002,I,Initialization Sequence Completed\r\n")
		fmt.Fprint(srv, ">BYTECOUNT:1024,2048\r\n")
		fmt.Fprint(srv, "SUCCESS: hold release succeeded\r\n")
		fmt.Fprint(srv, ">FATAL:Cannot open TUN\r\n")
	}()
	var h rec
	conn := NewConn(cli)
	go func() { _ = conn.Run(&h); got <- h.events }()
	events := <-got
	<-done
	wantEvents := []string{
		"state CONNECTING   ",
		"need Auth",
		"failed Auth",
		"need Private Key",
		"state CONNECTED SUCCESS 10.8.0.2 203.0.113.5",
		"log Initialization Sequence Completed",
		"bytes 1024 2048",
		"fatal Cannot open TUN",
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Errorf("events:\n got %q\nwant %q", events, wantEvents)
	}
	wantCmds := []string{"state on", "bytecount 2", "hold release"}
	if !reflect.DeepEqual(cmds, wantCmds) {
		t.Errorf("commands: got %q, want %q", cmds, wantCmds)
	}
}

func TestQuote(t *testing.T) {
	tests := []struct {
		in, want string
		bad      bool
	}{
		{in: "plain", want: `"plain"`},
		{in: `a"b`, want: `"a\"b"`},
		{in: `a\b`, want: `"a\\b"`},
		{in: `\"`, want: `"\\\""`},
		{in: "p ss", want: `"p ss"`},
		{in: "", want: `""`},
		{in: "x\nexit", bad: true}, // would run a second command
		{in: "x\rexit", bad: true},
		{in: "x\x00", bad: true},
	}
	for _, tt := range tests {
		got, err := Quote(tt.in)
		if tt.bad {
			if err == nil {
				t.Errorf("Quote(%q) = %q, want an error", tt.in, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("Quote(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestCredentialsCommands(t *testing.T) {
	srv, cli := net.Pipe()
	c := NewConn(cli)
	lines := make(chan string, 4)
	go func() {
		br := bufio.NewReader(srv)
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				return
			}
			lines <- strings.TrimSpace(l)
		}
	}()
	if err := c.Credentials(`us"er`, `pa\ss`); err != nil {
		t.Fatal(err)
	}
	if got := <-lines; got != `username "Auth" "us\"er"` {
		t.Errorf("got %q", got)
	}
	if got := <-lines; got != `password "Auth" "pa\\ss"` {
		t.Errorf("got %q", got)
	}
	if err := c.Credentials("u\nexit", "p"); err == nil {
		t.Error("a newline in the username must be refused")
	}
	if err := c.PrivateKey("k"); err != nil {
		t.Fatal(err)
	}
	if got := <-lines; got != `password "Private Key" "k"` {
		t.Errorf("got %q", got)
	}
}

func TestStateOf(t *testing.T) {
	for name, want := range map[string]State{"CONNECTED": Connected, "RECONNECTING": Reconnecting, "WAIT": Connecting, "AUTH": Connecting, "ADD_ROUTES": Connecting} {
		if got, ok := StateOf(name); !ok || got != want {
			t.Errorf("StateOf(%s) = %v, %v", name, got, ok)
		}
	}
	if _, ok := StateOf("EXITING"); ok {
		t.Error("EXITING must not map to a state")
	}
}

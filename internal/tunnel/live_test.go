package tunnel

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Runs the real openvpn (SPLITROUTE_TEST_OPENVPN=path) against a closed port:
// checks the hold/state/log handshake on a real management socket.
func TestLiveOpenVPN(t *testing.T) {
	bin := os.Getenv("SPLITROUTE_TEST_OPENVPN")
	if bin == "" {
		t.Skip("SPLITROUTE_TEST_OPENVPN not set")
	}
	dir, err := os.MkdirTemp("", "ovpn")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "m.sock")
	cmd := exec.Command(bin, "--client", "--dev", "tun", "--remote", "127.0.0.1", "1", "udp", "--nobind",
		"--management", sock, "unix", "--management-hold", "--management-query-passwords", "--auth-user-pass",
		"--peer-fingerprint", "00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF",
		"--verb", "3", "--connect-retry-max", "1", "--resolv-retry", "0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	nc, err := Dial(sock, 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := NewConn(nc)
	var h rec
	done := make(chan error, 1)
	go func() { done <- c.Run(&h) }()
	time.Sleep(1500 * time.Millisecond)
	_ = c.Credentials("u", "p")
	time.Sleep(1500 * time.Millisecond)
	_ = c.Stop()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("no EOF after SIGTERM")
	}
	for _, e := range h.events {
		t.Log(e)
	}
}

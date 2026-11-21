//go:build darwin || linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aliraghebi/splitroute/internal/tunnel"
)

const testProfile = "client\ndev tun\nremote vpn.example.com 443 tcp\nauth-user-pass\n<ca>\n-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n</ca>\n"

func TestTunnelImportAndRemove(t *testing.T) {
	a := testApp(t, false, true)
	tn, err := a.TunnelImport("Work", []byte(testProfile), false)
	if err != nil || tn.Name != "Work" || !tn.NeedsAuth || tn.State != tunnel.Disconnected {
		t.Fatalf("import: %+v, %v", tn, err)
	}
	s, _ := a.settings()
	ts, _ := s.Tunnels()
	if len(ts) != 1 {
		t.Fatalf("tunnels = %+v", ts)
	}
	p := a.profilePath(ts[0].ID)
	for _, path := range []string{p, strings.TrimSuffix(p, "/profile.ovpn"), a.tunnelsDir()} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is %v: profiles can hold private keys, no group or other access", path, fi.Mode().Perm())
		}
	}
	if b, _ := os.ReadFile(p); string(b) != testProfile {
		t.Error("stored profile differs from the imported text")
	}
	if _, err := a.TunnelImport("Work", []byte(testProfile), false); err == nil {
		t.Error("duplicate name accepted")
	}
	if l, err := a.Tunnels(); err != nil || len(l.Tunnels) != 1 || l.Tunnels[0].State != tunnel.Disconnected {
		t.Errorf("Tunnels() = %+v, %v", l, err)
	}
	if err := a.TunnelRename("Work", "Office"); err != nil {
		t.Fatal(err)
	}
	if err := a.TunnelRemove("Office"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("profile file still there after remove: %v", err)
	}
	if err := a.TunnelRemove("Office"); err == nil {
		t.Error("removing a removed tunnel succeeded")
	}
}

func TestTunnelImportRefusals(t *testing.T) {
	a := testApp(t, false, true)
	tests := []struct {
		name, text, allow, wantErr string
	}{
		{name: "script needs confirmation", text: testProfile + "script-security 2\nup /etc/up.sh\n", wantErr: "confirm"},
		{name: "management refused even when confirmed", text: testProfile + "management 127.0.0.1 1\n", allow: "yes", wantErr: "management"},
		{name: "bad name", text: testProfile, wantErr: "tunnel name"},
	}
	for _, tt := range tests {
		name := "T"
		if tt.name == "bad name" {
			name = "x\ny"
		}
		_, err := a.TunnelImport(name, []byte(tt.text), tt.allow != "")
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("%s: err = %v, want containing %q", tt.name, err, tt.wantErr)
		}
	}
	if l, _ := a.Tunnels(); len(l.Tunnels) != 0 {
		t.Errorf("a refused import left tunnels behind: %+v", l.Tunnels)
	}
	// Confirmed scripts are stored.
	tn, err := a.TunnelImport("S", []byte(testProfile+"script-security 2\nup /etc/up.sh\n"), true)
	if err != nil || len(tn.Scripts) != 1 || tn.Scripts[0] != "up" {
		t.Errorf("confirmed import: %+v, %v", tn, err)
	}
}

func TestTunnelsNeedRoot(t *testing.T) {
	a := testApp(t, false, false)
	if _, err := a.TunnelImport("W", []byte(testProfile), false); err == nil || !strings.Contains(err.Error(), "root") {
		t.Errorf("import without root: %v", err)
	}
	if err := a.TunnelConnect("W", tunnel.Credentials{}); err == nil {
		t.Error("connect without root accepted")
	}
}

// Runs the real OpenVPN (SPLITROUTE_TEST_OPENVPN=/path) against a closed UDP
// port: start, prompt for credentials, receive them, stop. It never reaches
// the point of creating a tun device, so it needs no root.
func TestLiveTunnelLifecycle(t *testing.T) {
	bin := os.Getenv("SPLITROUTE_TEST_OPENVPN")
	if bin == "" {
		t.Skip("SPLITROUTE_TEST_OPENVPN not set")
	}
	a := testApp(t, false, true)
	a.openvpnPathForTest = bin
	a.runDirForTest = filepath.Join(os.TempDir(), "sr-"+strconv.Itoa(os.Getpid()))
	t.Cleanup(func() { os.RemoveAll(a.runDirForTest) })
	profile := "client\ndev tun\nnobind\nremote 127.0.0.1 1 udp\nauth-user-pass\nresolv-retry 0\n" +
		"peer-fingerprint 00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF\n"
	if _, err := a.TunnelImport("Live", []byte(profile), false); err != nil {
		t.Fatal(err)
	}
	if err := a.TunnelConnect("Live", tunnel.Credentials{}); err != nil {
		t.Fatal(err)
	}
	state := func() tunnel.State { l, _ := a.Tunnels(); return l.Tunnels[0].State }
	deadline := time.Now().Add(10 * time.Second)
	for state() != tunnel.NeedsAuth && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if state() != tunnel.NeedsAuth {
		logs, _ := a.TunnelLogs("Live")
		l, _ := a.Tunnels()
		t.Fatalf("state = %s (%s), want needs_auth; logs: %q", state(), l.Tunnels[0].Error, logs)
	}
	if err := a.TunnelCredentials("Live", tunnel.Credentials{User: "u", Pass: "p", HaveAuth: true}); err != nil {
		t.Fatal(err)
	}
	for state() == tunnel.NeedsAuth && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if err := a.TunnelDisconnect("Live"); err != nil {
		t.Fatal(err)
	}
	if s := state(); s != tunnel.Disconnected {
		t.Errorf("state after disconnect = %s", s)
	}
	logs, _ := a.TunnelLogs("Live")
	t.Logf("%d log lines; first: %q", len(logs), logs[:min(2, len(logs))])
	if len(logs) == 0 {
		t.Error("no OpenVPN output was captured")
	}
	if entries, _ := os.ReadDir(a.runDirForTest); len(entries) != 0 {
		t.Errorf("management socket left behind: %v", entries)
	}
}

//go:build darwin || linux

package main

import (
	"encoding/json"
	"net"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func TestParseConfigLegacy(t *testing.T) {
	cfg, err := parseConfig([]byte("# comment\nir-cidr = direct\nmalware=block # inline\n\nbase_url = https://example.com/dl/\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{BaseURL: "https://example.com/dl/", Lists: []List{{Name: "ir-cidr", Action: Direct}, {Name: "malware", Action: Block}}}
	if !reflect.DeepEqual(cfg, want) || cfg.baseURL() != "https://example.com/dl" {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
	for _, bad := range []string{"ir-cidr = proxy", "ir-cidr", "Bad_Name = direct", "a = direct\na = block", "base_url = http://x"} {
		if _, err := parseConfig([]byte(bad)); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestParseConfigJSON(t *testing.T) {
	cfg, err := parseConfig([]byte(`{"disabled": true, "lists": [
		{"name": "ir-cidr", "action": "direct"},
		{"name": "My servers", "custom": true, "action": "block", "entries": ["203.0.113.7", "198.51.100.0/24"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Disabled || len(cfg.Lists) != 2 || !cfg.Lists[1].Custom || cfg.baseURL() != defaultBaseURL {
		t.Fatalf("got %+v", cfg)
	}
	for _, bad := range []string{
		`{"lists": [{"name": "Upper", "action": "direct"}]}`,                           // release names are lowercase
		`{"lists": [{"name": "ir-cidr", "action": "direct", "entries": ["1.2.3.4"]}]}`, // entries on a release list
		`{"lists": [{"name": " x", "custom": true, "action": "direct"}]}`,              // padded name
		`{"lists": [{"name": "x", "custom": true, "action": "direct"}, {"name": "x", "custom": true, "action": "block"}]}`,
		`{"lists": [{"name": "x", "custom": true, "action": "direct", "entries": ["10.1.2.3"]}]}`, // reserved
		`{"lists": [}`,
	} {
		if _, err := parseConfig([]byte(bad)); err == nil {
			t.Errorf("%s: want error", bad)
		}
	}
}

func TestParseEntries(t *testing.T) {
	got, err := parseEntries([]string{" 203.0.113.7 ", "", "# note", "198.51.100.9/24"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []netip.Prefix{pfx("203.0.113.7/32"), pfx("198.51.100.0/24")}) {
		t.Fatalf("got %v", got)
	}
	for _, bad := range []string{"example.com", "2001:db8::1", "1.0.0.0/7", "192.168.1.1", "100.64.0.0/16"} {
		if _, err := parseEntries([]string{bad}); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestWithPreset(t *testing.T) {
	custom := List{Name: "Work", Custom: true, Action: Direct, Entries: []string{"203.0.113.0/24"}}
	cfg := Config{Disabled: true, Lists: []List{{Name: "telegram", Action: Direct}, custom}}
	got, err := withPreset(cfg, "security")
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Disabled: true, Lists: []List{{Name: "malware", Action: Block}, {Name: "phishing", Action: Block}, custom}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	if _, err := withPreset(cfg, "nope"); err == nil {
		t.Error("unknown preset: want error")
	}
	for _, p := range presets {
		if err := (Config{Lists: p.Lists}).validate(); err != nil {
			t.Errorf("preset %s: %v", p.ID, err)
		}
	}
}

func TestRespondConfig(t *testing.T) {
	a := newApp("linux", filepath.Join(t.TempDir(), "splitroute.conf"), false)
	ok := func(r response) response {
		t.Helper()
		if !r.OK {
			t.Fatalf("error: %s", r.Error)
		}
		return r
	}
	if r := ok(a.respond(request{Cmd: "get_config"})); !reflect.DeepEqual(*r.Config, defaultConfig()) {
		t.Fatalf("default config: %+v", r.Config)
	}
	off := false
	if r := ok(a.respond(request{Cmd: "set_enabled", Enabled: &off})); !r.Config.Disabled {
		t.Fatal("set_enabled false: still enabled")
	}
	bad := Config{Lists: []List{{Name: "x", Custom: true, Action: Direct, Entries: []string{"not-an-ip"}}}}
	if r := a.respond(request{Cmd: "set_config", Config: &bad}); r.OK || r.Error == "" {
		t.Fatal("invalid config accepted")
	}
	if r := ok(a.respond(request{Cmd: "apply_preset", Preset: "iran"})); !r.Config.Disabled || r.Config.Lists[0].Name != "ir-cidr" {
		t.Fatalf("preset lost disabled or lists: %+v", r.Config)
	}
	saved, err := loadConfig(a.cfgPath)
	if err != nil || !saved.Disabled || len(saved.Lists) != len(presets[0].Lists) {
		t.Fatalf("saved %+v, %v", saved, err)
	}
	if r := a.respond(request{Cmd: "rm -rf"}); r.OK {
		t.Fatal("unknown command accepted")
	}
}

func TestWantDisabledAndCustom(t *testing.T) {
	a := newApp("linux", filepath.Join(t.TempDir(), "splitroute.conf"), true)
	custom := Config{Lists: []List{{Name: "Work", Custom: true, Action: Block, Entries: []string{"203.0.113.7"}}}}
	if err := saveConfig(a.cfgPath, custom); err != nil {
		t.Fatal(err)
	}
	got, _, err := a.want() // no release lists: no network
	if err != nil || !reflect.DeepEqual(got, []Route{{pfx("203.0.113.7/32"), Block, "Work"}}) {
		t.Fatalf("got %v, %v", got, err)
	}
	custom.Disabled = true
	if err := saveConfig(a.cfgPath, custom); err != nil {
		t.Fatal(err)
	}
	if got, _, err := a.want(); err != nil || len(got) != 0 {
		t.Fatalf("disabled: got %v, %v", got, err)
	}
}

func TestParseList(t *testing.T) {
	ps, st, err := parseList([]byte("# NAME: x\nIP-CIDR,2.144.0.1/14,no-resolve\nIP-CIDR6,2400::/32,no-resolve\nDOMAIN-SUFFIX,ir\nIP-CIDR,10.0.0.0/8,no-resolve\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ps, []netip.Prefix{pfx("2.144.0.0/14")}) {
		t.Fatalf("prefixes %v", ps)
	}
	if st != (listStats{domains: 1, ipv6: 1, reserved: 1}) {
		t.Fatalf("stats %+v", st)
	}
	// A poisoned list must fail, not route half the internet outside the VPN.
	for _, bad := range []string{"IP-CIDR,0.0.0.0/1,no-resolve", "IP-CIDR,128.0.0.0/7", "IP-CIDR,nonsense"} {
		if _, _, err := parseList([]byte(bad)); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestPlanBlockWins(t *testing.T) {
	cfg := Config{Lists: []List{{Name: "ir", Action: Direct}, {Name: "bad", Action: Block}, {Name: "tg", Action: VPN}}}
	got, err := plan(cfg, map[string][]netip.Prefix{
		"ir":  {pfx("5.0.0.0/16"), pfx("2.144.0.0/14")},
		"bad": {pfx("5.0.0.0/16")},
		"tg":  {pfx("91.108.4.0/22")},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{{pfx("2.144.0.0/14"), Direct, "ir"}, {pfx("5.0.0.0/16"), Block, "bad"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestChanges(t *testing.T) {
	gw1 := Gateway{netip.MustParseAddr("192.168.1.1"), "en0"}
	gw2 := Gateway{netip.MustParseAddr("10.0.0.1"), "en1"}
	a := Route{pfx("2.144.0.0/14"), Direct, "ir"}
	b := Route{pfx("5.0.0.0/16"), Block, "bad"}
	c := Route{pfx("31.0.0.0/16"), Direct, "ir"}

	del, add := changes([]Route{a, b}, gw1, []Route{a, b, c}, gw1, false)
	if len(del) != 0 || !reflect.DeepEqual(add, []Route{c}) {
		t.Fatalf("same gateway: del %v add %v", del, add)
	}
	// New gateway: direct routes move, block routes stay.
	del, add = changes([]Route{a, b}, gw1, []Route{a, b}, gw2, false)
	if !reflect.DeepEqual(del, []Route{a}) || !reflect.DeepEqual(add, []Route{a}) {
		t.Fatalf("new gateway: del %v add %v", del, add)
	}
	del, add = changes([]Route{a, b}, gw1, nil, gw1, false)
	if !reflect.DeepEqual(del, []Route{a, b}) || len(add) != 0 {
		t.Fatalf("down: del %v add %v", del, add)
	}
}

func TestCommands(t *testing.T) {
	gw := Gateway{netip.MustParseAddr("192.168.1.1"), "wlan0"}
	d := Route{pfx("2.144.0.0/14"), Direct, "ir"}
	b := Route{pfx("5.0.0.0/16"), Block, "bad"}
	cases := []struct{ got, want string }{
		{strings.Join(darwinAdd(d, gw), " "), "-n add -net 2.144.0.0/14 192.168.1.1"},
		{strings.Join(darwinAdd(b, gw), " "), "-n add -net 5.0.0.0/16 127.0.0.1 -blackhole"},
		{strings.Join(darwinDel(d), " "), "-n delete -net 2.144.0.0/14"},
		{linuxAdd(d, gw), "route replace 2.144.0.0/14 via 192.168.1.1 dev wlan0 proto 199"},
		{linuxAdd(b, gw), "route replace blackhole 5.0.0.0/16 proto 199"},
		{linuxDel(b), "route del blackhole 5.0.0.0/16 proto 199"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func TestParseNWI(t *testing.T) {
	out := `Network information

IPv4 network interface information
   utun4 : flags      : 0x5 (IPv4,DNS)
           address    : 10.5.0.2
           VPN server : 1.2.3.4
     en0 : flags      : 0x7 (IPv4,IPv6,DNS)
           address    : 192.168.1.10
           reach      : 0x00000002 (Reachable)

IPv6 network interface information
     en1 : flags      : 0x7 (IPv4,IPv6,DNS)

Network interfaces: utun4 en0
`
	if got := parseNWI(out); !reflect.DeepEqual(got, []string{"utun4", "en0"}) {
		t.Fatalf("got %v", got)
	}
}

func TestParseLinuxDefault(t *testing.T) {
	out := "default dev wg0 scope link\ndefault via 10.8.0.1 dev tun0 metric 50\ndefault via 192.168.1.1 dev wlan0 proto dhcp metric 600\ndefault via 192.168.2.1 dev eth0 proto dhcp metric 100\n"
	gw, err := parseLinuxDefault(out)
	if err != nil {
		t.Fatal(err)
	}
	if gw != (Gateway{netip.MustParseAddr("192.168.2.1"), "eth0"}) {
		t.Fatalf("got %+v", gw)
	}
	if _, err := parseLinuxDefault("default via 10.8.0.1 dev tun0\n"); err == nil {
		t.Fatal("tunnel-only: want error")
	}
}

func TestField(t *testing.T) {
	if got := field("   route to: 1.1.1.1\n  gateway: 192.168.1.1\n  interface: utun4\n", "interface"); got != "utun4" {
		t.Errorf("darwin: %q", got)
	}
	if got := field("1.1.1.1 dev wg0 table 51820 src 10.2.0.2 uid 0\n", "dev"); got != "wg0" {
		t.Errorf("linux: %q", got)
	}
}

func TestHandleSocket(t *testing.T) {
	a := newApp("linux", filepath.Join(t.TempDir(), "splitroute.conf"), false)
	srv, cli := net.Pipe()
	go a.handle(srv)
	if _, err := cli.Write([]byte(`{"cmd":"presets"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	var r response
	if err := json.NewDecoder(cli).Decode(&r); err != nil || !r.OK || len(r.Presets) != len(presets) {
		t.Fatalf("got %+v, %v", r, err)
	}
}

func TestServiceExe(t *testing.T) {
	cases := []struct{ self, onPath, want string }{
		{"/Applications/Splitroute.app/Contents/Resources/splitroute", "/Users/me/.local/bin/splitroute", "/Applications/Splitroute.app/Contents/Resources/splitroute"},
		{"/opt/homebrew/Caskroom/splitroute/0.1.0/splitroute", "/opt/homebrew/bin/splitroute", "/opt/homebrew/bin/splitroute"},
		{"/opt/homebrew/Cellar/splitroute/0.2.0/bin/splitroute", "", "/opt/homebrew/Cellar/splitroute/0.2.0/bin/splitroute"},
		{"/usr/local/bin/splitroute", "/usr/local/bin/splitroute", "/usr/local/bin/splitroute"},
	}
	for _, c := range cases {
		if got := serviceExe(c.self, c.onPath); got != c.want {
			t.Errorf("serviceExe(%q, %q) = %q, want %q", c.self, c.onPath, got, c.want)
		}
	}
}

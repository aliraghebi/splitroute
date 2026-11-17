//go:build darwin || linux

package platform

import (
	"net/netip"
	"reflect"
	"testing"
)

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
	if got := ParseNWI(out); !reflect.DeepEqual(got, []string{"utun4", "en0"}) {
		t.Fatalf("got %v", got)
	}
}

func TestParseLinuxDefault(t *testing.T) {
	out := "default dev wg0 scope link\ndefault via 10.8.0.1 dev tun0 metric 50\ndefault via 192.168.1.1 dev wlan0 proto dhcp metric 600\ndefault via 192.168.2.1 dev eth0 proto dhcp metric 100\n"
	gw, err := ParseLinuxDefault(out)
	if err != nil {
		t.Fatal(err)
	}
	if gw != (Gateway{netip.MustParseAddr("192.168.2.1"), "eth0"}) {
		t.Fatalf("got %+v", gw)
	}
	if _, err := ParseLinuxDefault("default via 10.8.0.1 dev tun0\n"); err == nil {
		t.Fatal("tunnel-only: want error")
	}
}

func TestField(t *testing.T) {
	if got := Field("   route to: 1.1.1.1\n  gateway: 192.168.1.1\n  interface: utun4\n", "interface"); got != "utun4" {
		t.Errorf("darwin: %q", got)
	}
	if got := Field("1.1.1.1 dev wg0 table 51820 src 10.2.0.2 uid 0\n", "dev"); got != "wg0" {
		t.Errorf("linux: %q", got)
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
		if got := ServiceExe(c.self, c.onPath); got != c.want {
			t.Errorf("ServiceExe(%q, %q) = %q, want %q", c.self, c.onPath, got, c.want)
		}
	}
}

//go:build darwin || linux

package route

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/aliraghebi/splitroute/internal/platform"
	"github.com/aliraghebi/splitroute/internal/rules"
)

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func TestChanges(t *testing.T) {
	gw1 := platform.Gateway{IP: netip.MustParseAddr("192.168.1.1"), Iface: "en0"}
	gw2 := platform.Gateway{IP: netip.MustParseAddr("10.0.0.1"), Iface: "en1"}
	a := rules.Route{Prefix: pfx("2.144.0.0/14"), Action: rules.Direct, List: "ir"}
	b := rules.Route{Prefix: pfx("5.0.0.0/16"), Action: rules.Block, List: "bad"}
	c := rules.Route{Prefix: pfx("31.0.0.0/16"), Action: rules.Direct, List: "ir"}

	del, add := Changes([]rules.Route{a, b}, gw1, []rules.Route{a, b, c}, gw1, false)
	if len(del) != 0 || !reflect.DeepEqual(add, []rules.Route{c}) {
		t.Fatalf("same gateway: del %v add %v", del, add)
	}
	// New gateway: direct routes move, block routes stay.
	del, add = Changes([]rules.Route{a, b}, gw1, []rules.Route{a, b}, gw2, false)
	if !reflect.DeepEqual(del, []rules.Route{a}) || !reflect.DeepEqual(add, []rules.Route{a}) {
		t.Fatalf("new gateway: del %v add %v", del, add)
	}
	del, add = Changes([]rules.Route{a, b}, gw1, nil, gw1, false)
	if !reflect.DeepEqual(del, []rules.Route{a, b}) || len(add) != 0 {
		t.Fatalf("down: del %v add %v", del, add)
	}
}

func TestCommands(t *testing.T) {
	gw := platform.Gateway{IP: netip.MustParseAddr("192.168.1.1"), Iface: "wlan0"}
	d := rules.Route{Prefix: pfx("2.144.0.0/14"), Action: rules.Direct, List: "ir"}
	b := rules.Route{Prefix: pfx("5.0.0.0/16"), Action: rules.Block, List: "bad"}
	cases := []struct{ got, want string }{
		{strings.Join(DarwinAdd(d, gw), " "), "-n add -net 2.144.0.0/14 192.168.1.1"},
		{strings.Join(DarwinAdd(b, gw), " "), "-n add -net 5.0.0.0/16 127.0.0.1 -blackhole"},
		{strings.Join(DarwinDel(d), " "), "-n delete -net 2.144.0.0/14"},
		{LinuxAdd(d, gw), "route replace 2.144.0.0/14 via 192.168.1.1 dev wlan0 proto 199"},
		{LinuxAdd(b, gw), "route replace blackhole 5.0.0.0/16 proto 199"},
		{LinuxDel(b), "route del blackhole 5.0.0.0/16 proto 199"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

package lists

import (
	"net/netip"
	"reflect"
	"testing"
)

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func TestParseList(t *testing.T) {
	ps, st, err := Parse([]byte("# NAME: x\nIP-CIDR,2.144.0.1/14,no-resolve\nIP-CIDR6,2400::/32,no-resolve\nDOMAIN-SUFFIX,ir\nIP-CIDR,10.0.0.0/8,no-resolve\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ps, []netip.Prefix{pfx("2.144.0.0/14")}) {
		t.Fatalf("prefixes %v", ps)
	}
	if st != (Stats{Domains: 1, IPv6: 1, Reserved: 1}) {
		t.Fatalf("stats %+v", st)
	}
	// A poisoned list must fail, not route half the internet outside the VPN.
	for _, bad := range []string{"IP-CIDR,0.0.0.0/1,no-resolve", "IP-CIDR,128.0.0.0/7", "IP-CIDR,nonsense"} {
		if _, _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

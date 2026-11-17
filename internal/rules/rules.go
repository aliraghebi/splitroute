// Package rules holds the routing model: actions, entry validation and the
// route plan. It is pure: nothing here touches the system.
package rules

import (
	"fmt"
	"net/netip"
	"strings"
)

// Action is what happens to traffic for a list's IP ranges.
type Action string

const (
	Direct Action = "direct" // route via the physical gateway, outside the VPN
	Block  Action = "block"  // blackhole route
	VPN    Action = "vpn"    // no route: traffic follows the VPN (or the default route)
)

// MinBits: never route anything broader than a /8.
const MinBits = 8

// reserved ranges are never routed: touching them can cut off the LAN, loopback
// or the VPN's own addresses.
var reserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("224.0.0.0/3"),
}

// IsReserved reports whether p overlaps a range that is never routed.
func IsReserved(p netip.Prefix) bool {
	for _, r := range reserved {
		if r.Overlaps(p) {
			return true
		}
	}
	return false
}

// ParseEntries turns a custom list's entries into prefixes. Unlike release
// lists, a reserved range is an error here: the user typed it and should know
// it has no effect.
func ParseEntries(entries []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" || strings.HasPrefix(e, "#") {
			continue
		}
		var p netip.Prefix
		var err error
		if strings.Contains(e, "/") {
			p, err = netip.ParsePrefix(e)
		} else {
			var a netip.Addr
			if a, err = netip.ParseAddr(e); err == nil {
				p = netip.PrefixFrom(a, a.BitLen())
			}
		}
		switch {
		case err != nil:
			return nil, fmt.Errorf("%q is not an IP address or range (domains are not supported yet)", e)
		case !p.Addr().Is4():
			return nil, fmt.Errorf("%q: only IPv4 is supported", e)
		case p.Bits() < MinBits:
			return nil, fmt.Errorf("%q is broader than /%d", e, MinBits)
		case IsReserved(p.Masked()):
			return nil, fmt.Errorf("%q is a private or special range and is never routed", e)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

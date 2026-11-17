package rules

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
)

// MaxRoutes is a sanity cap; the Iran list is ~2.7k IPv4 ranges.
const MaxRoutes = 20000

// Route is one route splitroute manages.
type Route struct {
	Prefix netip.Prefix `json:"prefix"`
	Action Action       `json:"action"`
	List   string       `json:"list"`
}

// Use is one configured list and its action, in config order.
type Use struct {
	List   string
	Action Action
}

// Plan merges the lists into one route per prefix, sorted. When the same
// prefix is in a block list and a direct list, block wins.
func Plan(uses []Use, lists map[string][]netip.Prefix) ([]Route, error) {
	by := map[netip.Prefix]Route{}
	for _, u := range uses {
		if u.Action == VPN {
			continue
		}
		for _, p := range lists[u.List] {
			if r, ok := by[p]; ok && r.Action == Block {
				continue
			}
			by[p] = Route{p, u.Action, u.List}
		}
	}
	if len(by) > MaxRoutes {
		return nil, fmt.Errorf("%d routes exceed the limit of %d", len(by), MaxRoutes)
	}
	out := make([]Route, 0, len(by))
	for _, r := range by {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b Route) int {
		return cmp.Or(a.Prefix.Addr().Compare(b.Prefix.Addr()), cmp.Compare(a.Prefix.Bits(), b.Prefix.Bits()))
	})
	return out, nil
}

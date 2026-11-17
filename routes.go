//go:build darwin || linux

package main

import (
	"cmp"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

const (
	maxRoutes  = 20000 // sanity cap; the Iran list is ~2.7k IPv4 ranges
	linuxProto = "199" // route protocol tag that marks splitroute's routes on Linux
)

// Route is one route splitroute manages.
type Route struct {
	Prefix netip.Prefix `json:"prefix"`
	Action Action       `json:"action"`
	List   string       `json:"list"`
}

// Gateway is the physical (non-VPN) next hop.
type Gateway struct {
	IP    netip.Addr `json:"ip"`
	Iface string     `json:"iface"`
}

// plan merges the lists into one route per prefix, sorted. When the same
// prefix is in a block list and a direct list, block wins.
func plan(cfg Config, lists map[string][]netip.Prefix) ([]Route, error) {
	by := map[netip.Prefix]Route{}
	for _, la := range cfg.Lists {
		if la.Action == VPN {
			continue
		}
		for _, p := range lists[la.Name] {
			if r, ok := by[p]; ok && r.Action == Block {
				continue
			}
			by[p] = Route{p, la.Action, la.Name}
		}
	}
	if len(by) > maxRoutes {
		return nil, fmt.Errorf("%d routes exceed the limit of %d", len(by), maxRoutes)
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

type routeKey struct {
	p netip.Prefix
	a Action
}

// changes returns the routes to delete and add to go from the applied routes
// (via oldGW) to want (via gw). Direct routes move when the gateway changes;
// force re-adds everything (routes were removed behind our back).
func changes(applied []Route, oldGW Gateway, want []Route, gw Gateway, force bool) (del, add []Route) {
	wanted := map[routeKey]bool{}
	for _, r := range want {
		wanted[routeKey{r.Prefix, r.Action}] = true
	}
	kept := map[routeKey]bool{}
	for _, r := range applied {
		k := routeKey{r.Prefix, r.Action}
		if force || !wanted[k] || (r.Action == Direct && oldGW != gw) {
			del = append(del, r)
		} else {
			kept[k] = true
		}
	}
	for _, r := range want {
		if !kept[routeKey{r.Prefix, r.Action}] {
			add = append(add, r)
		}
	}
	return del, add
}

// Commands per OS. They are pure so tests can check them on any OS.

func darwinAdd(r Route, gw Gateway) []string {
	if r.Action == Block {
		return []string{"-n", "add", "-net", r.Prefix.String(), "127.0.0.1", "-blackhole"}
	}
	return []string{"-n", "add", "-net", r.Prefix.String(), gw.IP.String()}
}

func darwinDel(r Route) []string { return []string{"-n", "delete", "-net", r.Prefix.String()} }

// linuxAdd and linuxDel return lines for `ip -batch`.
func linuxAdd(r Route, gw Gateway) string {
	if r.Action == Block {
		return "route replace blackhole " + r.Prefix.String() + " proto " + linuxProto
	}
	return "route replace " + r.Prefix.String() + " via " + gw.IP.String() + " dev " + gw.Iface + " proto " + linuxProto
}

func linuxDel(r Route) string {
	if r.Action == Block {
		return "route del blackhole " + r.Prefix.String() + " proto " + linuxProto
	}
	return "route del " + r.Prefix.String() + " proto " + linuxProto
}

// isTunnel reports whether an interface name looks like a VPN tunnel.
func isTunnel(iface string) bool {
	for _, p := range []string{"utun", "ipsec", "ppp", "tun", "tap", "wg", "nordlynx", "tailscale", "zt"} {
		if strings.HasPrefix(iface, p) {
			return true
		}
	}
	return false
}

// parseNWI returns the interface names in the IPv4 section of `scutil --nwi`, in order.
func parseNWI(out string) []string {
	var ifs []string
	in := false
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "IPv4 network interface information"):
			in = true
		case strings.HasPrefix(line, "IPv6 network interface information"), strings.HasPrefix(line, "Network interfaces"):
			in = false
		case in:
			name, rest, ok := strings.Cut(strings.TrimSpace(line), " : ")
			if ok && strings.HasPrefix(strings.TrimSpace(rest), "flags") {
				ifs = append(ifs, name)
			}
		}
	}
	return ifs
}

// parseLinuxDefault picks the lowest-metric default route that isn't on a tunnel
// from `ip -4 route show default`.
func parseLinuxDefault(out string) (Gateway, error) {
	best, bestMetric := Gateway{}, -1
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != "default" {
			continue
		}
		var gw Gateway
		metric := 0
		for i := 1; i+1 < len(f); i++ {
			switch f[i] {
			case "via":
				gw.IP, _ = netip.ParseAddr(f[i+1])
			case "dev":
				gw.Iface = f[i+1]
			case "metric":
				metric, _ = strconv.Atoi(f[i+1])
			}
		}
		if !gw.IP.IsValid() || !gw.IP.Is4() || gw.Iface == "" || isTunnel(gw.Iface) {
			continue
		}
		if bestMetric < 0 || metric < bestMetric {
			best, bestMetric = gw, metric
		}
	}
	if bestMetric < 0 {
		return Gateway{}, errors.New("no default route on a physical interface")
	}
	return best, nil
}

// field returns the value after "key:" (darwin `route get`) or after "key "
// (Linux `ip route get`), or "".
func field(out, key string) string {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key+":"); ok {
			return strings.TrimSpace(v)
		}
		f := strings.Fields(line)
		for i := 0; i+1 < len(f); i++ {
			if f[i] == key {
				return f[i+1]
			}
		}
	}
	return ""
}

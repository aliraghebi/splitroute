//go:build darwin || linux

// Package route is the route-mode engine: it moves the kernel routing table
// from the applied routes to the planned ones. Commands run through the
// Query/Change functions it is given; the builders and the diff are pure.
package route

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/aliraghebi/splitroute/internal/platform"
	"github.com/aliraghebi/splitroute/internal/rules"
)

// LinuxProto is the route protocol tag that marks splitroute's routes on Linux.
const LinuxProto = "199"

type routeKey struct {
	p netip.Prefix
	a rules.Action
}

// Changes returns the routes to delete and add to go from the applied routes
// (via oldGW) to want (via gw). Direct routes move when the gateway changes;
// force re-adds everything (routes were removed behind our back).
func Changes(applied []rules.Route, oldGW platform.Gateway, want []rules.Route, gw platform.Gateway, force bool) (del, add []rules.Route) {
	wanted := map[routeKey]bool{}
	for _, r := range want {
		wanted[routeKey{r.Prefix, r.Action}] = true
	}
	kept := map[routeKey]bool{}
	for _, r := range applied {
		k := routeKey{r.Prefix, r.Action}
		if force || !wanted[k] || (r.Action == rules.Direct && oldGW != gw) {
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

func DarwinAdd(r rules.Route, gw platform.Gateway) []string {
	if r.Action == rules.Block {
		return []string{"-n", "add", "-net", r.Prefix.String(), "127.0.0.1", "-blackhole"}
	}
	return []string{"-n", "add", "-net", r.Prefix.String(), gw.IP.String()}
}

func DarwinDel(r rules.Route) []string { return []string{"-n", "delete", "-net", r.Prefix.String()} }

// LinuxAdd and LinuxDel return lines for `ip -batch`.
func LinuxAdd(r rules.Route, gw platform.Gateway) string {
	if r.Action == rules.Block {
		return "route replace blackhole " + r.Prefix.String() + " proto " + LinuxProto
	}
	return "route replace " + r.Prefix.String() + " via " + gw.IP.String() + " dev " + gw.Iface + " proto " + LinuxProto
}

func LinuxDel(r rules.Route) string {
	if r.Action == rules.Block {
		return "route del blackhole " + r.Prefix.String() + " proto " + LinuxProto
	}
	return "route del " + r.Prefix.String() + " proto " + LinuxProto
}

// Engine runs route commands for one OS.
type Engine struct {
	GOOS   string
	Query  platform.QueryFunc
	Change platform.ChangeFunc
}

// Apply deletes del and adds add (direct routes via gw).
func (e Engine) Apply(del, add []rules.Route, gw platform.Gateway) error {
	if e.GOOS == "linux" {
		batch := func(rs []rules.Route, line func(rules.Route) string) string {
			var b strings.Builder
			for _, r := range rs {
				b.WriteString(line(r) + "\n")
			}
			return b.String()
		}
		if len(del) > 0 {
			_ = e.Change("ip", []string{"-4", "-force", "-batch", "-"}, batch(del, LinuxDel)) // already gone is fine
		}
		if len(add) > 0 {
			return e.Change("ip", []string{"-4", "-force", "-batch", "-"}, batch(add, func(r rules.Route) string { return LinuxAdd(r, gw) }))
		}
		return nil
	}
	// ponytail: one `route` process per route (~3k for Iran, a few seconds);
	// writing to the routing socket directly is the upgrade if this gets slow.
	for _, r := range del {
		_ = e.Change("route", DarwinDel(r), "") // already gone is fine
	}
	failed, last := 0, error(nil)
	for _, r := range add {
		if err := e.Change("route", DarwinAdd(r, gw), ""); err != nil {
			_ = e.Change("route", DarwinDel(r), "") // stale route from a crash: replace it
			if err := e.Change("route", DarwinAdd(r, gw), ""); err != nil {
				failed, last = failed+1, err
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d routes failed; last error: %v", failed, len(add), last)
	}
	return nil
}

// RemoveAll deletes routes and, on Linux, anything else tagged with our proto.
func (e Engine) RemoveAll(routes []rules.Route, gw platform.Gateway) error {
	err := e.Apply(routes, nil, gw)
	if e.GOOS == "linux" {
		_ = e.Change("ip", []string{"-4", "route", "flush", "proto", LinuxProto}, "")
	}
	return err
}

// Present spot-checks that the applied routes are still in the routing table.
func (e Engine) Present(routes []rules.Route, gw platform.Gateway) bool {
	if len(routes) == 0 {
		return true
	}
	if e.GOOS == "linux" {
		out, err := e.Query("ip", "-4", "route", "show", "proto", LinuxProto)
		return err == nil && countLines(out) >= len(routes)
	}
	// ponytail: checks the first route of each action, not all ~3k.
	for _, act := range []rules.Action{rules.Direct, rules.Block} {
		i := slices.IndexFunc(routes, func(r rules.Route) bool { return r.Action == act })
		if i < 0 {
			continue
		}
		out, err := e.Query("route", "-n", "get", routes[i].Prefix.Addr().String())
		if err != nil {
			return false
		}
		if act == rules.Direct && platform.Field(out, "gateway") != gw.IP.String() {
			return false
		}
		if act == rules.Block && !strings.Contains(platform.Field(out, "flags"), "BLACKHOLE") && platform.Field(out, "gateway") != "127.0.0.1" {
			return false
		}
	}
	return true
}

func countLines(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

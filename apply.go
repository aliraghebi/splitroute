//go:build darwin || linux

// Planning and applying routes: up, down and the presence check.

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// want downloads the configured release lists, adds the custom lists and
// plans the routes. A disabled config plans none.
func (a *app) want() ([]Route, []string, error) {
	cfg, err := loadConfig(a.cfgPath)
	if err != nil || cfg.Disabled {
		return nil, nil, err
	}
	var names []string
	lists := map[string][]netip.Prefix{}
	for _, l := range cfg.Lists {
		switch {
		case l.Action == VPN:
		case l.Custom:
			if lists[l.Name], err = parseEntries(l.Entries); err != nil {
				return nil, nil, err // validated on load; unreachable
			}
		default:
			names = append(names, l.Name)
		}
	}
	files, warn, err := fetchLists(cfg.baseURL(), filepath.Join(a.stateDir, "lists"), names, !a.dry)
	if err != nil {
		return nil, warn, err
	}
	for _, n := range names {
		ps, st, err := parseList(files[n])
		if err != nil {
			return nil, warn, fmt.Errorf("%s.list: %w", n, err)
		}
		if len(ps) == 0 {
			warn = append(warn, fmt.Sprintf("%s has no routable IPv4 ranges (%d domains, %d IPv6, %d reserved skipped)", n, st.domains, st.ipv6, st.reserved))
		}
		lists[n] = ps
	}
	routes, err := plan(cfg, lists)
	return routes, warn, err
}

// up downloads the lists and moves the system from the saved state to the
// planned routes. It returns the new state.
func (a *app) up(force bool) (State, error) {
	want, warn, err := a.want()
	for _, w := range warn {
		log.Print("warning: ", w)
	}
	if err != nil {
		return State{}, err
	}
	st, err := a.loadState()
	if err != nil {
		return State{}, err
	}
	gw, err := a.gateway()
	if err != nil && slices.ContainsFunc(want, func(r Route) bool { return r.Action == Direct }) {
		return st, err
	}
	next := State{Gateway: gw, Routes: want, Fetched: time.Now().UTC(), Warnings: warn}
	return next, a.applyAndSave(st, next, force)
}

func (a *app) applyAndSave(old, next State, force bool) error {
	del, add := changes(old.Routes, old.Gateway, next.Routes, next.Gateway, force)
	err := a.apply(del, add, next.Gateway)
	// Save even after a partial failure, so `down` can remove what was added.
	if serr := a.saveState(next); serr != nil && err == nil {
		err = serr
	}
	if err == nil {
		log.Printf("%d routes (%d removed, %d added) via %s %s", len(next.Routes), len(del), len(add), next.Gateway.IP, next.Gateway.Iface)
	}
	return err
}

func (a *app) apply(del, add []Route, gw Gateway) error {
	if a.goos == "linux" {
		batch := func(rs []Route, line func(Route) string) string {
			var b strings.Builder
			for _, r := range rs {
				b.WriteString(line(r) + "\n")
			}
			return b.String()
		}
		if len(del) > 0 {
			_ = a.change("ip", []string{"-4", "-force", "-batch", "-"}, batch(del, linuxDel)) // already gone is fine
		}
		if len(add) > 0 {
			return a.change("ip", []string{"-4", "-force", "-batch", "-"}, batch(add, func(r Route) string { return linuxAdd(r, gw) }))
		}
		return nil
	}
	// ponytail: one `route` process per route (~3k for Iran, a few seconds);
	// writing to the routing socket directly is the upgrade if this gets slow.
	for _, r := range del {
		_ = a.change("route", darwinDel(r), "") // already gone is fine
	}
	failed, last := 0, error(nil)
	for _, r := range add {
		if err := a.change("route", darwinAdd(r, gw), ""); err != nil {
			_ = a.change("route", darwinDel(r), "") // stale route from a crash: replace it
			if err := a.change("route", darwinAdd(r, gw), ""); err != nil {
				failed, last = failed+1, err
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d routes failed; last error: %v", failed, len(add), last)
	}
	return nil
}

// present spot-checks that the saved routes are still in the routing table.
func (a *app) present(st State) bool {
	if len(st.Routes) == 0 {
		return true
	}
	if a.goos == "linux" {
		out, err := a.query("ip", "-4", "route", "show", "proto", linuxProto)
		return err == nil && countLines(out) >= len(st.Routes)
	}
	// ponytail: checks the first route of each action, not all ~3k.
	for _, act := range []Action{Direct, Block} {
		i := slices.IndexFunc(st.Routes, func(r Route) bool { return r.Action == act })
		if i < 0 {
			continue
		}
		out, err := a.query("route", "-n", "get", st.Routes[i].Prefix.Addr().String())
		if err != nil {
			return false
		}
		if act == Direct && field(out, "gateway") != st.Gateway.IP.String() {
			return false
		}
		if act == Block && !strings.Contains(field(out, "flags"), "BLACKHOLE") && field(out, "gateway") != "127.0.0.1" {
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

func (a *app) down() error {
	st, err := a.loadState()
	if err != nil {
		return err
	}
	err = a.apply(st.Routes, nil, st.Gateway)
	if a.goos == "linux" {
		_ = a.change("ip", []string{"-4", "route", "flush", "proto", linuxProto}, "")
	}
	if !a.dry {
		if rerr := os.Remove(a.statePath()); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) && err == nil {
			err = rerr
		}
	}
	if err == nil {
		log.Printf("removed %d routes", len(st.Routes))
	}
	return err
}

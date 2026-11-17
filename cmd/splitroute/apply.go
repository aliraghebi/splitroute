//go:build darwin || linux

// Planning and applying routes: up and down.

package main

import (
	"context"
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

	"github.com/aliraghebi/splitroute/internal/engine/route"
	"github.com/aliraghebi/splitroute/internal/lists"
	"github.com/aliraghebi/splitroute/internal/platform"
	"github.com/aliraghebi/splitroute/internal/rules"
	"github.com/aliraghebi/splitroute/internal/state"
)

// want downloads the configured release lists, adds the custom lists and
// plans the routes. A disabled config plans none.
func (a *app) want() ([]rules.Route, []string, error) {
	cfg, err := a.LoadConfig()
	if err != nil || cfg.Disabled {
		return nil, nil, err
	}
	var names, warn []string
	byList := map[string][]netip.Prefix{}
	for _, l := range cfg.Lists {
		switch {
		case l.Action == rules.VPN:
		case l.Import != nil:
			es, w, err := a.importEntries(l.Name)
			if err != nil {
				return nil, warn, err
			}
			if w != "" {
				warn = append(warn, w)
			}
			byList[l.Name] = es.V4
			if es.Unrouted() != "" && len(es.V4) == 0 {
				warn = append(warn, fmt.Sprintf("%s has no routable IPv4 ranges (%s)", l.Name, es.Unrouted()))
			}
		case l.Custom:
			es, err := rules.ParseEntries(l.Entries)
			if err != nil {
				return nil, nil, err // validated on load; unreachable
			}
			byList[l.Name] = es.V4
			if es.Unrouted() != "" && len(es.V4) == 0 {
				warn = append(warn, fmt.Sprintf("%s has no routable IPv4 ranges (%s)", l.Name, es.Unrouted()))
			}
		default:
			names = append(names, l.Name)
		}
	}
	files, fwarn, err := lists.Fetch(cfg.ListsURL(), filepath.Join(a.state.Dir, "lists"), names, !a.dry)
	warn = append(warn, fwarn...)
	if err != nil {
		return nil, warn, err
	}
	for _, n := range names {
		es, st, err := lists.Parse(files[n])
		if err != nil {
			return nil, warn, fmt.Errorf("%s.list: %w", n, err)
		}
		if len(es.V4) == 0 {
			warn = append(warn, fmt.Sprintf("%s has no routable IPv4 ranges (%s)", n, skipped(es, st)))
		}
		byList[n] = es.V4
	}
	routes, err := rules.Plan(cfg.Uses(), byList)
	return routes, warn, err
}

// importEntries reads an import's downloaded entries, with a warning when
// it has none yet or its last download failed.
func (a *app) importEntries(name string) (rules.Entries, string, error) {
	s, err := a.settings()
	if err != nil {
		return rules.Entries{}, "", err
	}
	if s == nil {
		return rules.Entries{}, fmt.Sprintf("import %s: not downloaded yet (run `sudo splitroute up`)", name), nil
	}
	es, err := s.ImportEntries(name)
	if err != nil {
		return rules.Entries{}, "", err
	}
	sts, err := s.Imports()
	if err != nil {
		return rules.Entries{}, "", err
	}
	for _, st := range sts {
		switch {
		case st.Name != name:
		case st.FetchedAt.IsZero() && st.LastError != "":
			return es, fmt.Sprintf("import %s: not downloaded: %s", name, st.LastError), nil
		case st.FetchedAt.IsZero():
			return es, fmt.Sprintf("import %s: not downloaded yet", name), nil
		case st.LastError != "":
			return es, fmt.Sprintf("import %s: last download failed (%s); using the copy from %s", name, st.LastError, st.FetchedAt.Local().Format(time.DateTime)), nil
		}
	}
	return es, "", nil
}

// skipped describes what a release list had besides routable IPv4 ranges.
func skipped(es rules.Entries, st lists.Stats) string {
	parts := []string{}
	if u := es.Unrouted(); u != "" {
		parts = append(parts, u)
	}
	for _, c := range []struct {
		n    int
		what string
	}{{st.Reserved, "reserved"}, {st.Invalid, "invalid domains"}, {st.Other, "other rules"}} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s skipped", c.n, c.what))
		}
	}
	if len(parts) == 0 {
		return "empty"
	}
	return strings.Join(parts, ", ")
}

func (a *app) gateway() (platform.Gateway, error) { return platform.DetectGateway(a.goos, a.query) }

// up downloads the lists and moves the system from the saved state to the
// planned routes. It returns the new state.
func (a *app) up(force bool) (state.State, error) {
	iwarn := a.refreshImports(context.Background())
	want, warn, err := a.want()
	warn = append(iwarn, warn...)
	for _, w := range warn {
		log.Print("warning: ", w)
	}
	if err != nil {
		return state.State{}, err
	}
	st, err := a.state.Load()
	if err != nil {
		return state.State{}, err
	}
	gw, err := a.gateway()
	if err != nil && slices.ContainsFunc(want, func(r rules.Route) bool { return r.Action == rules.Direct }) {
		return st, err
	}
	next := state.State{Gateway: gw, Routes: want, Fetched: time.Now().UTC(), Warnings: warn}
	return next, a.applyAndSave(st, next, force)
}

func (a *app) applyAndSave(old, next state.State, force bool) error {
	del, add := route.Changes(old.Routes, old.Gateway, next.Routes, next.Gateway, force)
	err := a.routes.Apply(del, add, next.Gateway)
	// Save even after a partial failure, so `down` can remove what was added.
	if serr := a.state.Save(next); serr != nil && err == nil {
		err = serr
	}
	if err == nil {
		log.Printf("%d routes (%d removed, %d added) via %s %s", len(next.Routes), len(del), len(add), next.Gateway.IP, next.Gateway.Iface)
	}
	return err
}

func (a *app) down() error {
	st, err := a.state.Load()
	if err != nil {
		return err
	}
	err = a.routes.RemoveAll(st.Routes, st.Gateway)
	if !a.dry {
		if rerr := os.Remove(a.state.Path()); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) && err == nil {
			err = rerr
		}
	}
	if err == nil {
		log.Printf("removed %d routes", len(st.Routes))
	}
	return err
}

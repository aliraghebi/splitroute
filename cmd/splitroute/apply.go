//go:build darwin || linux

// Planning and applying routes: up and down.

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
	"time"

	"github.com/aliraghebi/splitroute/internal/config"
	"github.com/aliraghebi/splitroute/internal/engine/route"
	"github.com/aliraghebi/splitroute/internal/lists"
	"github.com/aliraghebi/splitroute/internal/platform"
	"github.com/aliraghebi/splitroute/internal/rules"
	"github.com/aliraghebi/splitroute/internal/state"
)

// want downloads the configured release lists, adds the custom lists and
// plans the routes. A disabled config plans none.
func (a *app) want() ([]rules.Route, []string, error) {
	cfg, err := config.Load(a.cfgPath)
	if err != nil || cfg.Disabled {
		return nil, nil, err
	}
	var names []string
	byList := map[string][]netip.Prefix{}
	for _, l := range cfg.Lists {
		switch {
		case l.Action == rules.VPN:
		case l.Custom:
			if byList[l.Name], err = rules.ParseEntries(l.Entries); err != nil {
				return nil, nil, err // validated on load; unreachable
			}
		default:
			names = append(names, l.Name)
		}
	}
	files, warn, err := lists.Fetch(cfg.ListsURL(), filepath.Join(a.store.Dir, "lists"), names, !a.dry)
	if err != nil {
		return nil, warn, err
	}
	for _, n := range names {
		ps, st, err := lists.Parse(files[n])
		if err != nil {
			return nil, warn, fmt.Errorf("%s.list: %w", n, err)
		}
		if len(ps) == 0 {
			warn = append(warn, fmt.Sprintf("%s has no routable IPv4 ranges (%d domains, %d IPv6, %d reserved skipped)", n, st.Domains, st.IPv6, st.Reserved))
		}
		byList[n] = ps
	}
	routes, err := rules.Plan(cfg.Uses(), byList)
	return routes, warn, err
}

func (a *app) gateway() (platform.Gateway, error) { return platform.DetectGateway(a.goos, a.query) }

// up downloads the lists and moves the system from the saved state to the
// planned routes. It returns the new state.
func (a *app) up(force bool) (state.State, error) {
	want, warn, err := a.want()
	for _, w := range warn {
		log.Print("warning: ", w)
	}
	if err != nil {
		return state.State{}, err
	}
	st, err := a.store.Load()
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
	if serr := a.store.Save(next); serr != nil && err == nil {
		err = serr
	}
	if err == nil {
		log.Printf("%d routes (%d removed, %d added) via %s %s", len(next.Routes), len(del), len(add), next.Gateway.IP, next.Gateway.Iface)
	}
	return err
}

func (a *app) down() error {
	st, err := a.store.Load()
	if err != nil {
		return err
	}
	err = a.routes.RemoveAll(st.Routes, st.Gateway)
	if !a.dry {
		if rerr := os.Remove(a.store.Path()); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) && err == nil {
			err = rerr
		}
	}
	if err == nil {
		log.Printf("removed %d routes", len(st.Routes))
	}
	return err
}

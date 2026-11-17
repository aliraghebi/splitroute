//go:build darwin || linux

// The service loop: periodic refresh, network events and reapply.

package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aliraghebi/splitroute/internal/api"
	"github.com/aliraghebi/splitroute/internal/platform"
	"github.com/aliraghebi/splitroute/internal/state"
)

// watch applies the routes, then re-applies them when the settings change
// (socket request, or a root CLI command writing the database), when the gateway changes, when they disappear
// (VPN reconnects often rewrite routes) and once a day with fresh lists. On
// SIGINT/SIGTERM it removes them: routes left behind would point at a gateway
// nobody maintains.
func (a *app) watch() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	a.reload = make(chan struct{}, 1)
	if ln, err := api.Listen(a.goos); err != nil {
		log.Printf("warning: control socket: %v (the menu bar app can't connect)", err)
	} else {
		defer ln.Close()
		go api.Serve(ln, a)
	}
	var st state.State
	var staleWarned bool
	a.settingsChanged() // baseline, before the first read
	refresh := func() {
		next, err := a.up(false)
		a.setLastErr(err)
		if err != nil {
			log.Printf("up: %v (retrying in a minute)", err)
			st.Fetched = time.Now().Add(time.Minute - refreshEvery)
			return
		}
		st = next
	}
	refresh()
	t := time.NewTicker(checkEvery)
	defer t.Stop()
	var netCh <-chan struct{} // nil (never ready) if the route socket is unavailable
	if f, err := platform.RouteSocket(); err != nil {
		log.Printf("warning: route socket: %v (checking every %s only)", err, checkEvery)
	} else {
		netCh = netEvents(f)
	}
	var settle <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return a.down()
		case <-a.reload:
			refresh()
			continue
		case <-netCh:
			// Check a second after the first event of a burst (a network switch
			// is many messages). Not reset by later events, so steady route churn
			// from other software can't postpone the check forever.
			if settle == nil {
				settle = time.After(settleDelay)
			}
			continue
		case <-settle:
			settle = nil
		case <-t.C:
		}
		a.warnStaleFile(&staleWarned)
		if a.settingsChanged() || time.Since(st.Fetched) > refreshEvery {
			refresh()
			continue
		}
		if len(st.Routes) == 0 {
			continue // nothing applied; never overwrite the saved state with an empty one
		}
		gw, err := a.gateway()
		if err != nil {
			continue // offline: keep routes until a gateway is back
		}
		if gw != st.Gateway || !a.routes.Present(st.Routes, st.Gateway) {
			next := state.State{Gateway: gw, Routes: st.Routes, Fetched: st.Fetched, Warnings: st.Warnings}
			err := a.applyAndSave(st, next, gw == st.Gateway)
			a.setLastErr(err)
			if err != nil {
				log.Printf("reapply: %v", err)
			}
			st = next
		}
	}
}

// modTime is the file's modification time, or zero if it can't be read.
// netEvents signals when a relevant message is read from f (a route socket),
// coalescing bursts into one pending signal. A signal only triggers the same
// gateway and presence check as the ticker, which is a no-op when nothing that
// matters changed (including after our own route changes).
func netEvents(f *os.File) <-chan struct{} {
	ch := make(chan struct{}, 1)
	go func() {
		buf := make([]byte, 1<<16)
		for {
			n, err := f.Read(buf)
			if err != nil && !errors.Is(err, syscall.ENOBUFS) { // ENOBUFS: messages dropped, treat as a change
				log.Printf("warning: route socket: %v (checking every %s only)", err, checkEvery)
				return
			}
			if err == nil && !platform.Relevant(buf[:n]) {
				continue
			}
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}()
	return ch
}

func modTime(path string) time.Time {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

func (a *app) setLastErr(err error) {
	a.errMu.Lock()
	defer a.errMu.Unlock()
	a.lastErr = ""
	if err != nil {
		a.lastErr = err.Error()
	}
}

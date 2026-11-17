//go:build darwin || linux

// The status report for `status` and the control socket.

package main

import (
	"fmt"
	"time"

	"github.com/aliraghebi/splitroute/internal/api"
	"github.com/aliraghebi/splitroute/internal/platform"
)

// Status builds the report for `status` and the control socket.
func (a *app) Status() (api.Status, error) {
	s := api.Status{Version: version, Lists: []api.ListCount{}}
	cfg, err := a.LoadConfig()
	if err != nil {
		return s, err
	}
	s.Enabled = !cfg.Disabled
	if gw, err := a.gateway(); err != nil {
		s.GatewayError = err.Error()
	} else {
		s.Gateway = &gw
	}
	s.VPN = platform.VPNIface(a.goos, a.query)
	st, err := a.state.Load()
	if err != nil {
		return s, err
	}
	s.Routes, s.Fetched, s.Warnings = len(st.Routes), st.Fetched, st.Warnings
	s.Lists = api.CountByList(st.Routes)
	s.Missing = !a.routes.Present(st.Routes, st.Gateway)
	a.errMu.Lock()
	s.Error = a.lastErr
	a.errMu.Unlock()
	return s, nil
}

func (a *app) printStatus() error {
	s, err := a.Status()
	if err != nil {
		return err
	}
	if s.Gateway != nil {
		fmt.Printf("gateway:   %s on %s\n", s.Gateway.IP, s.Gateway.Iface)
	} else {
		fmt.Println("gateway:  ", s.GatewayError)
	}
	if s.VPN != "" {
		fmt.Printf("vpn:       %s carries traffic to 1.1.1.1\n", s.VPN)
	} else {
		fmt.Println("vpn:       none detected")
	}
	if !s.Enabled {
		fmt.Println("enabled:   no (turned off in the config)")
	}
	if s.Routes == 0 {
		fmt.Println("routes:    none (run `sudo splitroute up`)")
		return nil
	}
	fmt.Printf("routes:    %d, lists from %s\n", s.Routes, s.Fetched.Format(time.RFC3339))
	for _, l := range s.Lists {
		fmt.Printf("           %5d %s %s\n", l.Routes, l.Action, l.Name)
	}
	for _, w := range s.Warnings {
		fmt.Println("warning:  ", w)
	}
	if s.Missing {
		fmt.Println("warning:   routes are missing from the routing table; run `sudo splitroute up`")
	}
	return nil
}

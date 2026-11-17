//go:build darwin || linux

// The status report for `status` and the control socket.

package main

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Status is what `status` prints and the control socket returns.
type Status struct {
	Version      string      `json:"version"`
	Enabled      bool        `json:"enabled"`
	Gateway      *Gateway    `json:"gateway,omitempty"`
	GatewayError string      `json:"gateway_error,omitempty"`
	VPN          string      `json:"vpn,omitempty"`
	Routes       int         `json:"routes"`
	Lists        []ListCount `json:"lists"`
	Fetched      time.Time   `json:"fetched"`
	Missing      bool        `json:"missing"` // saved routes are gone from the routing table
	Warnings     []string    `json:"warnings,omitempty"`
	Error        string      `json:"error,omitempty"` // service only: the last apply failed
}

// ListCount is how many routes a list contributed.
type ListCount struct {
	Name   string `json:"name"`
	Action Action `json:"action"`
	Routes int    `json:"routes"`
}

func (a *app) buildStatus() (Status, error) {
	s := Status{Version: version, Lists: []ListCount{}}
	cfg, err := loadConfig(a.cfgPath)
	if err != nil {
		return s, err
	}
	s.Enabled = !cfg.Disabled
	if gw, err := a.gateway(); err != nil {
		s.GatewayError = err.Error()
	} else {
		s.Gateway = &gw
	}
	s.VPN = a.vpnIface()
	st, err := a.loadState()
	if err != nil {
		return s, err
	}
	s.Routes, s.Fetched, s.Warnings = len(st.Routes), st.Fetched, st.Warnings
	s.Lists = countByList(st.Routes)
	s.Missing = !a.present(st)
	a.errMu.Lock()
	s.Error = a.lastErr
	a.errMu.Unlock()
	return s, nil
}

func countByList(routes []Route) []ListCount {
	idx := map[string]int{}
	out := []ListCount{}
	for _, r := range routes {
		i, ok := idx[r.List]
		if !ok {
			i = len(out)
			idx[r.List] = i
			out = append(out, ListCount{Name: r.List, Action: r.Action})
		}
		out[i].Routes++
	}
	slices.SortFunc(out, func(x, y ListCount) int { return strings.Compare(x.Name, y.Name) })
	return out
}

func (a *app) status() error {
	s, err := a.buildStatus()
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

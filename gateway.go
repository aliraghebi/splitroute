//go:build darwin || linux

// Physical gateway and VPN interface detection.

package main

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// gateway finds the physical next hop, ignoring VPN tunnels.
func (a *app) gateway() (Gateway, error) {
	if a.goos == "linux" {
		out, err := a.query("ip", "-4", "route", "show", "default")
		if err != nil {
			return Gateway{}, fmt.Errorf("ip route: %w", err)
		}
		return parseLinuxDefault(out)
	}
	out, err := a.query("scutil", "--nwi")
	if err != nil {
		return Gateway{}, fmt.Errorf("scutil --nwi: %w", err)
	}
	for _, ifc := range parseNWI(out) {
		if isTunnel(ifc) {
			continue
		}
		r, err := a.query("ipconfig", "getoption", ifc, "router")
		if err != nil {
			continue
		}
		if ip, err := netip.ParseAddr(strings.TrimSpace(r)); err == nil && ip.Is4() {
			return Gateway{ip, ifc}, nil
		}
	}
	return Gateway{}, errors.New("no physical interface with a router")
}

// vpnIface returns the tunnel that carries traffic to 1.1.1.1, or "".
func (a *app) vpnIface() string {
	var out string
	if a.goos == "linux" {
		out, _ = a.query("ip", "-4", "route", "get", "1.1.1.1")
		out = field(out, "dev")
	} else {
		out, _ = a.query("route", "-n", "get", "1.1.1.1")
		out = field(out, "interface")
	}
	if isTunnel(out) {
		return out
	}
	return ""
}

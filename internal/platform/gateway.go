//go:build darwin || linux

// Physical gateway and VPN interface detection.

package platform

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Gateway is the physical (non-VPN) next hop.
type Gateway struct {
	IP    netip.Addr `json:"ip"`
	Iface string     `json:"iface"`
}

// DetectGateway finds the physical next hop, ignoring VPN tunnels.
func DetectGateway(goos string, query QueryFunc) (Gateway, error) {
	if goos == "linux" {
		out, err := query("ip", "-4", "route", "show", "default")
		if err != nil {
			return Gateway{}, fmt.Errorf("ip route: %w", err)
		}
		return ParseLinuxDefault(out)
	}
	out, err := query("scutil", "--nwi")
	if err != nil {
		return Gateway{}, fmt.Errorf("scutil --nwi: %w", err)
	}
	for _, ifc := range ParseNWI(out) {
		if IsTunnel(ifc) {
			continue
		}
		r, err := query("ipconfig", "getoption", ifc, "router")
		if err != nil {
			continue
		}
		if ip, err := netip.ParseAddr(strings.TrimSpace(r)); err == nil && ip.Is4() {
			return Gateway{ip, ifc}, nil
		}
	}
	return Gateway{}, errors.New("no physical interface with a router")
}

// VPNIface returns the tunnel that carries traffic to 1.1.1.1, or "".
func VPNIface(goos string, query QueryFunc) string {
	var out string
	if goos == "linux" {
		out, _ = query("ip", "-4", "route", "get", "1.1.1.1")
		out = Field(out, "dev")
	} else {
		out, _ = query("route", "-n", "get", "1.1.1.1")
		out = Field(out, "interface")
	}
	if IsTunnel(out) {
		return out
	}
	return ""
}

// IsTunnel reports whether an interface name looks like a VPN tunnel.
func IsTunnel(iface string) bool {
	for _, p := range []string{"utun", "ipsec", "ppp", "tun", "tap", "wg", "nordlynx", "tailscale", "zt"} {
		if strings.HasPrefix(iface, p) {
			return true
		}
	}
	return false
}

// ParseNWI returns the interface names in the IPv4 section of `scutil --nwi`, in order.
func ParseNWI(out string) []string {
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

// ParseLinuxDefault picks the lowest-metric default route that isn't on a tunnel
// from `ip -4 route show default`.
func ParseLinuxDefault(out string) (Gateway, error) {
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
		if !gw.IP.IsValid() || !gw.IP.Is4() || gw.Iface == "" || IsTunnel(gw.Iface) {
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

// Field returns the value after "key:" (darwin `route get`) or after "key "
// (Linux `ip route get`), or "".
func Field(out, key string) string {
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

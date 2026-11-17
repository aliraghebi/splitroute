//go:build darwin || linux

package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	minBits  = 8        // never route anything broader than a /8
	maxBytes = 64 << 20 // per downloaded file
)

// reserved ranges are never routed: touching them can cut off the LAN, loopback
// or the VPN's own addresses.
var reserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("224.0.0.0/3"),
}

// listStats counts lines a list had that splitroute can't route.
type listStats struct{ domains, ipv6, reserved int }

// parseList extracts IPv4 ranges from a Shadowrocket-style .list file. A range
// broader than /8 is an error: it would hijack a large share of all traffic.
func parseList(b []byte) ([]netip.Prefix, listStats, error) {
	var out []netip.Prefix
	var st listStats
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		typ, rest, _ := strings.Cut(line, ",")
		switch typ {
		case "IP-CIDR":
		case "IP-CIDR6":
			st.ipv6++
			continue
		default:
			st.domains++
			continue
		}
		val, _, _ := strings.Cut(rest, ",")
		p, err := netip.ParsePrefix(val)
		if err != nil || !p.Addr().Is4() {
			return nil, st, fmt.Errorf("bad IP-CIDR line %q", line)
		}
		p = p.Masked()
		if p.Bits() < minBits {
			return nil, st, fmt.Errorf("refusing %s: broader than /%d", p, minBits)
		}
		if isReserved(p) {
			st.reserved++
			continue
		}
		out = append(out, p)
	}
	return out, st, sc.Err()
}

func isReserved(p netip.Prefix) bool {
	for _, r := range reserved {
		if r.Overlaps(p) {
			return true
		}
	}
	return false
}

// parseSums reads a sha256sum file into file name → hex digest.
func parseSums(b []byte) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			m[strings.TrimPrefix(f[1], "*")] = f[0]
		}
	}
	return m
}

var client = &http.Client{Timeout: 60 * time.Second}

func httpGet(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "splitroute/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBytes))
}

// fetchLists downloads each named list and checks it against the release's
// SHA256SUMS. A list that can't be downloaded or verified falls back to the
// last verified copy in cacheDir, so a network hiccup never drops routes.
func fetchLists(base, cacheDir string, names []string, save bool) (map[string][]byte, []string, error) {
	var warn []string
	var sums map[string]string
	if b, err := httpGet(base + "/SHA256SUMS"); err != nil {
		warn = append(warn, fmt.Sprintf("download SHA256SUMS: %v", err))
	} else {
		sums = parseSums(b)
	}
	out := map[string][]byte{}
	for _, n := range names {
		file := n + ".list"
		cached := filepath.Join(cacheDir, file)
		var err error
		if sums == nil {
			err = errors.New("no checksums")
		} else if want, ok := sums[file]; !ok {
			return nil, warn, fmt.Errorf("unknown list %q (run `splitroute lists`)", n)
		} else if b, gerr := httpGet(base + "/" + file); gerr != nil {
			err = gerr
		} else if got := sha256.Sum256(b); hex.EncodeToString(got[:]) != want {
			err = errors.New("checksum mismatch")
		} else {
			out[n] = b
			if save {
				if werr := writeFileAtomic(cached, b); werr != nil {
					warn = append(warn, fmt.Sprintf("cache %s: %v", file, werr))
				}
			}
			continue
		}
		b, cerr := os.ReadFile(cached)
		if cerr != nil {
			return nil, warn, fmt.Errorf("%s: %v, and no cached copy", file, err)
		}
		warn = append(warn, fmt.Sprintf("%s: %v; using cached copy", file, err))
		out[n] = b
	}
	return out, warn, nil
}

func writeFileAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (a *app) lists() error {
	names, err := a.availableLists()
	if err != nil {
		return err
	}
	fmt.Println("Lists (only their IPv4 ranges are routed; domain-only lists have no effect):")
	for _, n := range names {
		fmt.Println("  " + n)
	}
	return nil
}

// availableLists returns the list names in the latest release.
func (a *app) availableLists() ([]string, error) {
	cfg, err := loadConfig(a.cfgPath)
	if err != nil {
		return nil, err
	}
	b, err := httpGet(cfg.baseURL() + "/SHA256SUMS")
	if err != nil {
		return nil, err
	}
	var names []string
	for f := range parseSums(b) {
		if n, ok := strings.CutSuffix(f, ".list"); ok {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return names, nil
}

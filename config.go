//go:build darwin || linux

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strings"
)

// Action is what happens to traffic for a list's IP ranges.
type Action string

const (
	Direct Action = "direct" // route via the physical gateway, outside the VPN
	Block  Action = "block"  // blackhole route
	VPN    Action = "vpn"    // no route: traffic follows the VPN (or the default route)
)

// ListAction maps one release list (its name without ".list") to an action.
type ListAction struct {
	Name   string
	Action Action
}

// Config is the parsed config file.
type Config struct {
	BaseURL string
	Lists   []ListAction
}

const defaultBaseURL = "https://github.com/aliraghebi/routing-rules/releases/latest/download"

// defaultConfig is used when the config file does not exist.
var defaultConfig = Config{BaseURL: defaultBaseURL, Lists: []ListAction{{"ir-cidr", Direct}}}

var listName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// parseConfig reads "name = action" lines; "base_url = …" overrides the release URL.
// Blank lines and "#" comments are ignored.
func parseConfig(r io.Reader) (Config, error) {
	cfg := Config{BaseURL: defaultBaseURL}
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line, _, _ := strings.Cut(sc.Text(), "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if !ok || key == "" || val == "" {
			return Config{}, fmt.Errorf("line %d: want \"name = action\"", n)
		}
		if key == "base_url" {
			if !strings.HasPrefix(val, "https://") {
				return Config{}, fmt.Errorf("line %d: base_url must be https", n)
			}
			cfg.BaseURL = strings.TrimSuffix(val, "/")
			continue
		}
		if !listName.MatchString(key) {
			return Config{}, fmt.Errorf("line %d: invalid list name %q", n, key)
		}
		a := Action(val)
		if a != Direct && a != Block && a != VPN {
			return Config{}, fmt.Errorf("line %d: action must be direct, block or vpn, not %q", n, val)
		}
		if seen[key] {
			return Config{}, fmt.Errorf("line %d: list %q listed twice", n, key)
		}
		seen[key] = true
		cfg.Lists = append(cfg.Lists, ListAction{key, a})
	}
	return cfg, sc.Err()
}

// loadConfig reads path, or returns the default config when it doesn't exist.
func loadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return defaultConfig, nil
	}
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	cfg, err := parseConfig(f)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

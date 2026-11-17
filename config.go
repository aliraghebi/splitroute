//go:build darwin || linux

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/netip"
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

// List is one routing-rules release list (by its name without ".list"), or a
// custom list of the user's own IPv4 addresses and ranges.
type List struct {
	Name    string   `json:"name"`
	Action  Action   `json:"action"`
	Custom  bool     `json:"custom,omitempty"`
	Entries []string `json:"entries,omitempty"` // custom lists only: "1.2.3.4" or "1.2.3.0/24"
}

// Config is the daemon's configuration. Disabled keeps the service running
// with no routes (the menu bar app's off switch).
type Config struct {
	BaseURL  string `json:"base_url,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	Lists    []List `json:"lists"`
}

const (
	defaultBaseURL = "https://github.com/aliraghebi/routing-rules/releases/latest/download"
	maxNameLen     = 64
)

// defaultConfig is used when the config file does not exist.
func defaultConfig() Config {
	return Config{Lists: []List{{Name: "ir-cidr", Action: Direct}}}
}

var listName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// validate checks everything that comes from the config file or the socket.
func (c Config) validate() error {
	if c.BaseURL != "" && !strings.HasPrefix(c.BaseURL, "https://") {
		return errors.New("base_url must be https")
	}
	seen := map[string]bool{}
	for _, l := range c.Lists {
		switch {
		case l.Name == "" || len(l.Name) > maxNameLen || strings.TrimSpace(l.Name) != l.Name:
			return fmt.Errorf("list name %q: 1-%d characters, no leading or trailing spaces", l.Name, maxNameLen)
		case !l.Custom && !listName.MatchString(l.Name):
			return fmt.Errorf("invalid list name %q", l.Name)
		case !l.Custom && len(l.Entries) > 0:
			return fmt.Errorf("list %q: only custom lists have entries", l.Name)
		case l.Action != Direct && l.Action != Block && l.Action != VPN:
			return fmt.Errorf("list %q: action must be direct, block or vpn, not %q", l.Name, l.Action)
		case seen[l.Name]:
			return fmt.Errorf("list %q listed twice", l.Name)
		}
		seen[l.Name] = true
		if l.Custom {
			if _, err := parseEntries(l.Entries); err != nil {
				return fmt.Errorf("list %q: %w", l.Name, err)
			}
		}
	}
	return nil
}

func (c Config) baseURL() string {
	if c.BaseURL == "" {
		return defaultBaseURL
	}
	return strings.TrimSuffix(c.BaseURL, "/")
}

// parseEntries turns a custom list's entries into prefixes. Unlike release
// lists, a reserved range is an error here: the user typed it and should know
// it has no effect.
func parseEntries(entries []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" || strings.HasPrefix(e, "#") {
			continue
		}
		var p netip.Prefix
		var err error
		if strings.Contains(e, "/") {
			p, err = netip.ParsePrefix(e)
		} else {
			var a netip.Addr
			if a, err = netip.ParseAddr(e); err == nil {
				p = netip.PrefixFrom(a, a.BitLen())
			}
		}
		switch {
		case err != nil:
			return nil, fmt.Errorf("%q is not an IP address or range (domains are not supported yet)", e)
		case !p.Addr().Is4():
			return nil, fmt.Errorf("%q: only IPv4 is supported", e)
		case p.Bits() < minBits:
			return nil, fmt.Errorf("%q is broader than /%d", e, minBits)
		case isReserved(p.Masked()):
			return nil, fmt.Errorf("%q is a private or special range and is never routed", e)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// parseConfig reads the JSON config, or the original "name = action" format
// that splitroute 0.1 used (blank lines and "#" comments ignored,
// "base_url = …" overrides the release URL).
func parseConfig(b []byte) (Config, error) {
	var cfg Config
	if t := bytes.TrimSpace(b); len(t) > 0 && t[0] == '{' {
		if err := json.Unmarshal(t, &cfg); err != nil {
			return Config{}, err
		}
	} else if err := parseLegacy(bytes.NewReader(b), &cfg); err != nil {
		return Config{}, err
	}
	return cfg, cfg.validate()
}

func parseLegacy(r io.Reader, cfg *Config) error {
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
			return fmt.Errorf("line %d: want \"name = action\"", n)
		}
		if key == "base_url" {
			cfg.BaseURL = val
			continue
		}
		cfg.Lists = append(cfg.Lists, List{Name: key, Action: Action(val)})
	}
	return sc.Err()
}

// loadConfig reads path, or returns the default config when it doesn't exist.
func loadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return defaultConfig(), nil
	}
	if err != nil {
		return Config{}, err
	}
	cfg, err := parseConfig(b)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// saveConfig validates cfg and writes it as JSON, replacing a 0.1-format file.
func saveConfig(path string, cfg Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if cfg.Lists == nil {
		cfg.Lists = []List{} // "lists": [] rather than null for clients
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'))
}

// Preset is a named set of release lists and actions.
type Preset struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Lists []List `json:"lists"`
}

// presets only use lists that have IPv4 ranges; domain-only lists (ads, nsfw)
// do nothing with routes.
var presets = []Preset{
	{ID: "iran", Name: "Iran", Lists: []List{
		{Name: "ir-cidr", Action: Direct},
		{Name: "malware", Action: Block},
		{Name: "phishing", Action: Block},
	}},
	{ID: "security", Name: "Block malware and phishing", Lists: []List{
		{Name: "malware", Action: Block},
		{Name: "phishing", Action: Block},
	}},
}

// withPreset replaces cfg's release lists with the preset's and keeps the
// custom lists, after them.
func withPreset(cfg Config, id string) (Config, error) {
	for _, p := range presets {
		if p.ID != id {
			continue
		}
		lists := append([]List{}, p.Lists...)
		for _, l := range cfg.Lists {
			if l.Custom {
				lists = append(lists, l)
			}
		}
		cfg.Lists = lists
		return cfg, cfg.validate()
	}
	return cfg, fmt.Errorf("unknown preset %q", id)
}

func (a *app) preset(args []string) error {
	if len(args) == 0 {
		for _, p := range presets {
			fmt.Printf("%-10s %s\n", p.ID, p.Name)
			for _, l := range p.Lists {
				fmt.Printf("           %-6s %s\n", l.Action, l.Name)
			}
		}
		return nil
	}
	if a.dry {
		cfg, err := loadConfig(a.cfgPath)
		if err == nil {
			cfg, err = withPreset(cfg, args[0])
		}
		if err == nil {
			err = json.NewEncoder(os.Stdout).Encode(cfg)
		}
		return err
	}
	if _, err := a.updateConfig(func(c Config) (Config, error) { return withPreset(c, args[0]) }); err != nil {
		return err
	}
	log.Printf("preset %s saved to %s; the service applies it within %s (or run `sudo splitroute up`)", args[0], a.cfgPath, checkEvery)
	return nil
}

// updateConfig loads the config, changes it with f, saves it and asks the
// service (if this process is it) to apply it now.
func (a *app) updateConfig(f func(Config) (Config, error)) (Config, error) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	cfg, err := loadConfig(a.cfgPath)
	if err != nil {
		return Config{}, err
	}
	if cfg, err = f(cfg); err != nil {
		return Config{}, err
	}
	if err := saveConfig(a.cfgPath, cfg); err != nil {
		return Config{}, err
	}
	if a.reload != nil {
		select {
		case a.reload <- struct{}{}:
		default: // a reload is already pending
		}
	}
	return cfg, nil
}

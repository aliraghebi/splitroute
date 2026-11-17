//go:build darwin || linux

// Command splitroute sends chosen routing-rules IP lists outside a VPN
// (direct, via the physical gateway) or drops them (block), using plain
// system routes on macOS and Linux. More specific routes beat a VPN's default
// route, so this works next to most VPN apps, but not VPN kill switches that
// force all traffic into the tunnel.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

var version = "dev"

const usage = `splitroute sends chosen IP lists outside a VPN (direct) or drops them (block).

Usage: splitroute <command> [flags]

Commands:
  up         download the lists and apply routes
  down       remove every route splitroute added
  status     show the gateway, VPN and applied routes
  watch      apply, then keep routes correct as the network changes (run by the service)
  lists      show the lists available in the latest release
  preset     list presets; "preset <id>" replaces the release lists with a preset's
  install    install and start the system service (launchd or systemd)
  uninstall  stop and remove the service, then remove the routes
  version    print the version

Config (default /etc/splitroute.conf, JSON; without it, ir-cidr goes direct):
  {"lists": [
    {"name": "ir-cidr", "action": "direct"},
    {"name": "malware", "action": "block"},
    {"name": "Work", "custom": true, "action": "direct", "entries": ["203.0.113.0/24"]}
  ]}
direct: via the physical gateway, outside the VPN; block: drop; vpn: leave alone.
The 0.1 format ("ir-cidr = direct" lines) is still read.

Flags:
`

const (
	checkEvery   = 15 * time.Second
	refreshEvery = 24 * time.Hour
	serviceLabel = "io.github.aliraghebi.splitroute"
)

func main() {
	log.SetFlags(0)
	fs := flag.NewFlagSet("splitroute", flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/splitroute.conf", "config file")
	dry := fs.Bool("dry-run", false, "print route changes instead of making them")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage); fs.PrintDefaults() }
	_ = fs.Parse(os.Args[1:])
	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(2)
	}
	cmd := fs.Arg(0)
	var args []string
	for rest := fs.Args()[1:]; len(rest) > 0; rest = fs.Args()[1:] { // flags may also follow the command or its argument
		_ = fs.Parse(rest)
		if fs.NArg() == 0 {
			break
		}
		args = append(args, fs.Arg(0))
	}
	if len(args) > 1 || (len(args) == 1 && cmd != "preset") {
		fs.Usage()
		os.Exit(2)
	}
	a := newApp(runtime.GOOS, *cfgPath, *dry)
	if err := a.do(cmd, args); err != nil {
		log.Fatalf("splitroute: %v", err)
	}
}

// app holds everything a command needs. query always runs (it only reads
// system state); change is replaced by a printer in dry-run mode.
type app struct {
	goos     string
	cfgPath  string
	stateDir string
	dry      bool
	query    func(name string, args ...string) (string, error)
	change   func(name string, args []string, stdin string) error

	cfgMu   sync.Mutex    // serializes config read-modify-write
	reload  chan struct{} // set by watch: asks it to re-apply now
	errMu   sync.Mutex
	lastErr string // watch: the last failed apply, for status
}

func newApp(goos, cfgPath string, dry bool) *app {
	a := &app{goos: goos, cfgPath: cfgPath, dry: dry, stateDir: "/var/lib/splitroute", query: execQuery, change: execChange}
	if goos == "darwin" {
		a.stateDir = "/var/db/splitroute"
	}
	if dry {
		a.change = func(name string, args []string, stdin string) error {
			fmt.Println(name, strings.Join(args, " "))
			if stdin != "" {
				fmt.Print(stdin)
			}
			return nil
		}
	}
	return a
}

func execQuery(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}

func execChange(name string, args []string, stdin string) error {
	c := exec.Command(name, args...)
	c.Stdin = strings.NewReader(stdin)
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (a *app) do(cmd string, args []string) error {
	needRoot := map[string]bool{"up": true, "down": true, "watch": true, "install": true, "uninstall": true, "preset": len(args) > 0}
	if needRoot[cmd] && !a.dry && os.Geteuid() != 0 {
		return fmt.Errorf("%s needs root: run it with sudo", cmd)
	}
	switch cmd {
	case "up":
		return a.withLock(func() error { _, err := a.up(false); return err })
	case "down":
		return a.withLock(a.down)
	case "status":
		return a.status()
	case "watch":
		return a.withLock(a.watch)
	case "lists":
		return a.lists()
	case "preset":
		return a.preset(args)
	case "install":
		return a.install()
	case "uninstall":
		return a.uninstall()
	case "version":
		fmt.Println(version)
		return nil
	}
	return fmt.Errorf("unknown command %q (see splitroute -h)", cmd)
}

// State is what splitroute last applied; `down` removes exactly these routes.
type State struct {
	Gateway  Gateway   `json:"gateway"`
	Routes   []Route   `json:"routes"`
	Fetched  time.Time `json:"fetched"`
	Warnings []string  `json:"warnings,omitempty"`
}

func (a *app) statePath() string { return filepath.Join(a.stateDir, "state.json") }

func (a *app) loadState() (State, error) {
	var st State
	b, err := os.ReadFile(a.statePath())
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

func (a *app) saveState(st State) error {
	if a.dry {
		return nil
	}
	b, err := json.MarshalIndent(st, "", " ")
	if err != nil {
		return err
	}
	return writeFileAtomic(a.statePath(), b)
}

// withLock keeps a manual `up` from racing the service.
func (a *app) withLock(f func() error) error {
	if a.dry {
		return f()
	}
	if err := os.MkdirAll(a.stateDir, 0o755); err != nil {
		return err
	}
	lf, err := os.OpenFile(filepath.Join(a.stateDir, "lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another splitroute is running (the service?); stop it first")
	}
	return f()
}

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

// want downloads the configured release lists, adds the custom lists and
// plans the routes. A disabled config plans none.
func (a *app) want() ([]Route, []string, error) {
	cfg, err := loadConfig(a.cfgPath)
	if err != nil || cfg.Disabled {
		return nil, nil, err
	}
	var names []string
	lists := map[string][]netip.Prefix{}
	for _, l := range cfg.Lists {
		switch {
		case l.Action == VPN:
		case l.Custom:
			if lists[l.Name], err = parseEntries(l.Entries); err != nil {
				return nil, nil, err // validated on load; unreachable
			}
		default:
			names = append(names, l.Name)
		}
	}
	files, warn, err := fetchLists(cfg.baseURL(), filepath.Join(a.stateDir, "lists"), names, !a.dry)
	if err != nil {
		return nil, warn, err
	}
	for _, n := range names {
		ps, st, err := parseList(files[n])
		if err != nil {
			return nil, warn, fmt.Errorf("%s.list: %w", n, err)
		}
		if len(ps) == 0 {
			warn = append(warn, fmt.Sprintf("%s has no routable IPv4 ranges (%d domains, %d IPv6, %d reserved skipped)", n, st.domains, st.ipv6, st.reserved))
		}
		lists[n] = ps
	}
	routes, err := plan(cfg, lists)
	return routes, warn, err
}

// up downloads the lists and moves the system from the saved state to the
// planned routes. It returns the new state.
func (a *app) up(force bool) (State, error) {
	want, warn, err := a.want()
	for _, w := range warn {
		log.Print("warning: ", w)
	}
	if err != nil {
		return State{}, err
	}
	st, err := a.loadState()
	if err != nil {
		return State{}, err
	}
	gw, err := a.gateway()
	if err != nil && slices.ContainsFunc(want, func(r Route) bool { return r.Action == Direct }) {
		return st, err
	}
	next := State{Gateway: gw, Routes: want, Fetched: time.Now().UTC(), Warnings: warn}
	return next, a.applyAndSave(st, next, force)
}

func (a *app) applyAndSave(old, next State, force bool) error {
	del, add := changes(old.Routes, old.Gateway, next.Routes, next.Gateway, force)
	err := a.apply(del, add, next.Gateway)
	// Save even after a partial failure, so `down` can remove what was added.
	if serr := a.saveState(next); serr != nil && err == nil {
		err = serr
	}
	if err == nil {
		log.Printf("%d routes (%d removed, %d added) via %s %s", len(next.Routes), len(del), len(add), next.Gateway.IP, next.Gateway.Iface)
	}
	return err
}

func (a *app) apply(del, add []Route, gw Gateway) error {
	if a.goos == "linux" {
		batch := func(rs []Route, line func(Route) string) string {
			var b strings.Builder
			for _, r := range rs {
				b.WriteString(line(r) + "\n")
			}
			return b.String()
		}
		if len(del) > 0 {
			_ = a.change("ip", []string{"-4", "-force", "-batch", "-"}, batch(del, linuxDel)) // already gone is fine
		}
		if len(add) > 0 {
			return a.change("ip", []string{"-4", "-force", "-batch", "-"}, batch(add, func(r Route) string { return linuxAdd(r, gw) }))
		}
		return nil
	}
	// ponytail: one `route` process per route (~3k for Iran, a few seconds);
	// writing to the routing socket directly is the upgrade if this gets slow.
	for _, r := range del {
		_ = a.change("route", darwinDel(r), "") // already gone is fine
	}
	failed, last := 0, error(nil)
	for _, r := range add {
		if err := a.change("route", darwinAdd(r, gw), ""); err != nil {
			_ = a.change("route", darwinDel(r), "") // stale route from a crash: replace it
			if err := a.change("route", darwinAdd(r, gw), ""); err != nil {
				failed, last = failed+1, err
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d routes failed; last error: %v", failed, len(add), last)
	}
	return nil
}

// present spot-checks that the saved routes are still in the routing table.
func (a *app) present(st State) bool {
	if len(st.Routes) == 0 {
		return true
	}
	if a.goos == "linux" {
		out, err := a.query("ip", "-4", "route", "show", "proto", linuxProto)
		return err == nil && countLines(out) >= len(st.Routes)
	}
	// ponytail: checks the first route of each action, not all ~3k.
	for _, act := range []Action{Direct, Block} {
		i := slices.IndexFunc(st.Routes, func(r Route) bool { return r.Action == act })
		if i < 0 {
			continue
		}
		out, err := a.query("route", "-n", "get", st.Routes[i].Prefix.Addr().String())
		if err != nil {
			return false
		}
		if act == Direct && field(out, "gateway") != st.Gateway.IP.String() {
			return false
		}
		if act == Block && !strings.Contains(field(out, "flags"), "BLACKHOLE") && field(out, "gateway") != "127.0.0.1" {
			return false
		}
	}
	return true
}

func countLines(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

func (a *app) down() error {
	st, err := a.loadState()
	if err != nil {
		return err
	}
	err = a.apply(st.Routes, nil, st.Gateway)
	if a.goos == "linux" {
		_ = a.change("ip", []string{"-4", "route", "flush", "proto", linuxProto}, "")
	}
	if !a.dry {
		if rerr := os.Remove(a.statePath()); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) && err == nil {
			err = rerr
		}
	}
	if err == nil {
		log.Printf("removed %d routes", len(st.Routes))
	}
	return err
}

// watch applies the routes, then re-applies them when the config changes
// (socket request or file edit), when the gateway changes, when they disappear
// (VPN reconnects often rewrite routes) and once a day with fresh lists. On
// SIGINT/SIGTERM it removes them: routes left behind would point at a gateway
// nobody maintains.
func (a *app) watch() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	a.reload = make(chan struct{}, 1)
	if ln, err := a.listen(); err != nil {
		log.Printf("warning: control socket: %v (the menu bar app can't connect)", err)
	} else {
		defer ln.Close()
		go a.serve(ln)
	}
	var st State
	var cfgMod time.Time
	refresh := func() {
		cfgMod = modTime(a.cfgPath)
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
	for {
		select {
		case <-ctx.Done():
			return a.down()
		case <-a.reload:
			refresh()
			continue
		case <-t.C:
		}
		if time.Since(st.Fetched) > refreshEvery || !modTime(a.cfgPath).Equal(cfgMod) {
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
		if gw != st.Gateway || !a.present(st) {
			next := State{Gateway: gw, Routes: st.Routes, Fetched: st.Fetched, Warnings: st.Warnings}
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

func (a *app) servicePath() string {
	if a.goos == "linux" {
		return "/etc/systemd/system/splitroute.service"
	}
	return "/Library/LaunchDaemons/" + serviceLabel + ".plist"
}

func (a *app) install() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	path, _ := exec.LookPath("splitroute")
	exe, err := filepath.Abs(serviceExe(self, path))
	if err != nil {
		return err
	}
	unit := serviceFile(a.goos, exe, a.cfgPath)
	if a.dry {
		fmt.Printf("write %s:\n%s", a.servicePath(), unit)
	} else if err := os.WriteFile(a.servicePath(), []byte(unit), 0o644); err != nil {
		return err
	}
	if a.goos == "linux" {
		if err := a.change("systemctl", []string{"daemon-reload"}, ""); err != nil {
			return err
		}
		return a.change("systemctl", []string{"enable", "--now", "splitroute"}, "")
	}
	_ = a.change("launchctl", []string{"bootout", "system/" + serviceLabel}, "") // not loaded is fine
	a.waitUnloaded()
	return a.change("launchctl", []string{"bootstrap", "system", a.servicePath()}, "")
}

// serviceExe picks the binary the service runs: this one, unless it lives in a
// versioned Homebrew folder (Cellar, Caskroom) that the next upgrade deletes;
// then the PATH entry (Homebrew's stable symlink). Never a PATH entry
// otherwise: it may be an unrelated build (the app's admin prompt inherits the
// user's PATH).
func serviceExe(self, onPath string) string {
	if onPath != "" && (strings.Contains(self, "/Cellar/") || strings.Contains(self, "/Caskroom/")) {
		return onPath
	}
	return self
}

// waitUnloaded waits for launchd to drop the old job. bootout returns before
// the old process exits (its SIGTERM handler removes the routes, which takes
// seconds), and bootstrapping while the label still exists fails with
// "5: Input/output error".
func (a *app) waitUnloaded() {
	if a.dry {
		return
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		if _, err := a.query("launchctl", "print", "system/"+serviceLabel); err != nil {
			return
		}
	}
}

func (a *app) uninstall() error {
	// Stopping the service runs its cleanup (`down`).
	if a.goos == "linux" {
		_ = a.change("systemctl", []string{"disable", "--now", "splitroute"}, "")
	} else {
		_ = a.change("launchctl", []string{"bootout", "system/" + serviceLabel}, "")
	}
	if !a.dry {
		if err := os.Remove(a.servicePath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return a.withLock(a.down) // in case the service wasn't running
}

func serviceFile(goos, exe, cfgPath string) string {
	if goos == "linux" {
		return fmt.Sprintf(`[Unit]
Description=splitroute: route IP lists outside the VPN
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=%s watch --config %s
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, exe, cfgPath)
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>watch</string><string>--config</string><string>%s</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>/var/log/splitroute.log</string>
</dict>
</plist>
`, serviceLabel, exe, cfgPath)
}

//go:build darwin || linux

// Command splitroute sends chosen routing-rules IP lists outside a VPN
// (direct, via the physical gateway) or drops them (block), using plain
// system routes on macOS and Linux. More specific routes beat a VPN's default
// route, so this works next to most VPN apps, but not VPN kill switches that
// force all traffic into the tunnel.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/aliraghebi/splitroute/internal/engine/route"
	"github.com/aliraghebi/splitroute/internal/lists"
	"github.com/aliraghebi/splitroute/internal/platform"
	"github.com/aliraghebi/splitroute/internal/state"
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
	settleDelay  = time.Second // after the last network event, before checking
	refreshEvery = 24 * time.Hour
)

func main() {
	log.SetFlags(0)
	lists.UserAgent = "splitroute/" + version
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
// system state); change is replaced by a printer in dry-run mode. They are the
// only way anything here touches the system.
type app struct {
	goos    string
	cfgPath string
	dry     bool
	query   platform.QueryFunc
	change  platform.ChangeFunc
	store   state.Store
	routes  route.Engine

	cfgMu   sync.Mutex    // serializes config read-modify-write
	reload  chan struct{} // set by watch: asks it to re-apply now
	errMu   sync.Mutex
	lastErr string // watch: the last failed apply, for status
}

func newApp(goos, cfgPath string, dry bool) *app {
	a := &app{goos: goos, cfgPath: cfgPath, dry: dry, query: platform.ExecQuery, change: platform.ExecChange}
	a.store = state.Store{Dir: state.DefaultDir(goos), Dry: dry}
	if dry {
		a.change = func(name string, args []string, stdin string) error {
			fmt.Println(name, strings.Join(args, " "))
			if stdin != "" {
				fmt.Print(stdin)
			}
			return nil
		}
	}
	a.routes = route.Engine{GOOS: goos, Query: a.query, Change: a.change}
	return a
}

func (a *app) do(cmd string, args []string) error {
	needRoot := map[string]bool{"up": true, "down": true, "watch": true, "install": true, "uninstall": true, "preset": len(args) > 0}
	if needRoot[cmd] && !a.dry && os.Geteuid() != 0 {
		return fmt.Errorf("%s needs root: run it with sudo", cmd)
	}
	switch cmd {
	case "up":
		return a.store.WithLock(func() error { _, err := a.up(false); return err })
	case "down":
		return a.store.WithLock(a.down)
	case "status":
		return a.printStatus()
	case "watch":
		return a.store.WithLock(a.watch)
	case "lists":
		return a.printLists()
	case "preset":
		return a.runPreset(args)
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

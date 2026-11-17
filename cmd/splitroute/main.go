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
	"github.com/aliraghebi/splitroute/internal/store"
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

  config export         print the settings as JSON
  config import <file>  replace the settings with a JSON (or 0.1-format) file

Settings live in a database in the state directory. The first run as root
imports the config file (default /etc/splitroute.conf; without it, ir-cidr goes
direct); after that the file is no longer read. The JSON format:
  {"lists": [
    {"name": "ir-cidr", "action": "direct"},
    {"name": "malware", "action": "block"},
    {"name": "Work", "custom": true, "action": "direct", "entries": ["203.0.113.0/24", "example.com"]}
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
	if maxArgs := map[string]int{"preset": 1, "config": 2}; len(args) > maxArgs[cmd] {
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
	goos     string
	cfgPath  string // the old config file: imported into the database on the first root run
	dry      bool
	canWrite bool // may open the settings database read-write (root, not dry-run)
	query    platform.QueryFunc
	change   platform.ChangeFunc
	state    state.Store
	routes   route.Engine

	dbMu    sync.Mutex
	db      *store.Store  // opened on first use by settings()
	reload  chan struct{} // set by watch: asks it to re-apply now
	errMu   sync.Mutex
	lastErr string // watch: the last failed apply, for status
}

func newApp(goos, cfgPath string, dry bool) *app {
	a := &app{goos: goos, cfgPath: cfgPath, dry: dry, canWrite: !dry && os.Geteuid() == 0, query: platform.ExecQuery, change: platform.ExecChange}
	a.state = state.Store{Dir: state.DefaultDir(goos), Dry: dry}
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
	needRoot := map[string]bool{"up": true, "down": true, "watch": true, "install": true, "uninstall": true,
		"preset": len(args) > 0, "config": len(args) > 0 && args[0] == "import"}
	if needRoot[cmd] && !a.dry && os.Geteuid() != 0 {
		return fmt.Errorf("%s needs root: run it with sudo", cmd)
	}
	defer a.closeSettings()
	switch cmd {
	case "up":
		return a.state.WithLock(func() error { _, err := a.up(false); return err })
	case "down":
		return a.state.WithLock(a.down)
	case "status":
		return a.printStatus()
	case "watch":
		return a.state.WithLock(a.watch)
	case "lists":
		return a.printLists()
	case "preset":
		return a.runPreset(args)
	case "config":
		return a.configCmd(args)
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

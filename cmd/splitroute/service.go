//go:build darwin || linux

// Installing splitroute as a launchd or systemd service.

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/aliraghebi/splitroute/internal/platform"
)

func (a *app) servicePath() string { return platform.ServicePath(a.goos) }

// errAppManaged: the menu bar app registered the service with SMAppService;
// a CLI install would replace its job and uninstall would fight Login Items.
var errAppManaged = errors.New("the Splitroute app manages the service: use the app (Advanced) or System Settings > General > Login Items")

// appManaged reports whether the loaded macOS job belongs to the app.
func (a *app) appManaged() bool {
	if a.goos != "darwin" {
		return false
	}
	out, err := a.query("launchctl", "print", "system/"+platform.ServiceLabel)
	return err == nil && platform.ManagedByApp(out)
}

func (a *app) install() error {
	if a.appManaged() {
		return errAppManaged
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	path, _ := exec.LookPath("splitroute")
	exe, err := filepath.Abs(platform.ServiceExe(self, path))
	if err != nil {
		return err
	}
	unit := platform.ServiceFile(a.goos, exe, a.cfgPath)
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
	_ = a.change("launchctl", []string{"bootout", "system/" + platform.ServiceLabel}, "") // not loaded is fine
	a.waitUnloaded()
	return a.change("launchctl", []string{"bootstrap", "system", a.servicePath()}, "")
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
		if _, err := a.query("launchctl", "print", "system/"+platform.ServiceLabel); err != nil {
			return
		}
	}
}

func (a *app) uninstall() error {
	if a.appManaged() {
		return errAppManaged
	}
	// Stopping the service runs its cleanup (`down`).
	if a.goos == "linux" {
		_ = a.change("systemctl", []string{"disable", "--now", "splitroute"}, "")
	} else {
		_ = a.change("launchctl", []string{"bootout", "system/" + platform.ServiceLabel}, "")
	}
	if !a.dry {
		if err := os.Remove(a.servicePath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return a.state.WithLock(a.down) // in case the service wasn't running
}

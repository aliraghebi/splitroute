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
	"strings"
	"time"
)

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

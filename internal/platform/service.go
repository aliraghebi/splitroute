//go:build darwin || linux

// launchd and systemd service files.

package platform

import (
	"fmt"
	"strings"
)

const ServiceLabel = "io.github.aliraghebi.splitroute"

// ManagedByApp reports whether `launchctl print system/<label>` output is a
// job the menu bar app registered with SMAppService (macOS Login Items), which
// the CLI must not replace or remove.
func ManagedByApp(launchctlPrint string) bool {
	return strings.Contains(launchctlPrint, "managed_by = com.apple.xpc.ServiceManagement")
}

// ServicePath is where the service file lives on goos.
func ServicePath(goos string) string {
	if goos == "linux" {
		return "/etc/systemd/system/splitroute.service"
	}
	return "/Library/LaunchDaemons/" + ServiceLabel + ".plist"
}

// ServiceExe picks the binary the service runs: this one, unless it lives in a
// versioned Homebrew folder (Cellar, Caskroom) that the next upgrade deletes;
// then the PATH entry (Homebrew's stable symlink). Never a PATH entry
// otherwise: it may be an unrelated build (the app's admin prompt inherits the
// user's PATH).
func ServiceExe(self, onPath string) string {
	if onPath != "" && (strings.Contains(self, "/Cellar/") || strings.Contains(self, "/Caskroom/")) {
		return onPath
	}
	return self
}

func ServiceFile(goos, exe, cfgPath string) string {
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
`, ServiceLabel, exe, cfgPath)
}

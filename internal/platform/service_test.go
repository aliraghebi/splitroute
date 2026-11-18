//go:build darwin || linux

package platform

import "testing"

func TestManagedByApp(t *testing.T) {
	// Real `launchctl print` output (macOS 27), trimmed.
	smapp := `system/io.github.aliraghebi.splitroute.probe = {
	active count = 0
	path = (submitted by smd.81560)
	type = Submitted
	managed_by = com.apple.xpc.ServiceManagement
	state = not running

	program identifier = Contents/Resources/splitroute (mode: 2)
	parent bundle identifier = io.github.aliraghebi.splitroute.probe
`
	legacy := `system/io.github.aliraghebi.splitroute = {
	active count = 1
	path = /Library/LaunchDaemons/io.github.aliraghebi.splitroute.plist
	type = LaunchDaemon
	state = running

	program = /Applications/Splitroute.app/Contents/Resources/splitroute
`
	tests := []struct {
		name string
		out  string
		want bool
	}{
		{"registered by the app", smapp, true},
		{"installed by the CLI", legacy, false},
		{"not loaded", "Could not find service \"io.github.aliraghebi.splitroute\" in domain for system\n", false},
	}
	for _, tt := range tests {
		if got := ManagedByApp(tt.out); got != tt.want {
			t.Errorf("%s: got %v", tt.name, got)
		}
	}
}

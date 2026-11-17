//go:build darwin || linux

// Package platform holds everything OS-specific: running commands, gateway and
// VPN detection, the route socket, and service files. Command runners are
// passed in, so the parsers and decisions stay testable on any OS.
package platform

import (
	"fmt"
	"os/exec"
	"strings"
)

// QueryFunc runs a command that only reads system state.
type QueryFunc func(name string, args ...string) (string, error)

// ChangeFunc runs a command that changes the system. Dry-run replaces it with
// a printer.
type ChangeFunc func(name string, args []string, stdin string) error

func ExecQuery(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}

func ExecChange(name string, args []string, stdin string) error {
	c := exec.Command(name, args...)
	c.Stdin = strings.NewReader(stdin)
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

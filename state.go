//go:build darwin || linux

// The saved state file and the lock around route changes.

package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

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

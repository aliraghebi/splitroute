//go:build darwin || linux

// Package state holds the saved state file and the lock around route changes.
package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/aliraghebi/splitroute/internal/fileutil"
	"github.com/aliraghebi/splitroute/internal/platform"
	"github.com/aliraghebi/splitroute/internal/rules"
)

// State is what splitroute last applied; `down` removes exactly these routes.
type State struct {
	Gateway  platform.Gateway `json:"gateway"`
	Routes   []rules.Route    `json:"routes"`
	Fetched  time.Time        `json:"fetched"`
	Warnings []string         `json:"warnings,omitempty"`
}

// Store is the state directory. In dry-run mode it never writes or locks.
type Store struct {
	Dir string
	Dry bool
}

// DefaultDir is where the state lives on goos.
func DefaultDir(goos string) string {
	if goos == "darwin" {
		return "/var/db/splitroute"
	}
	return "/var/lib/splitroute"
}

func (s Store) Path() string { return filepath.Join(s.Dir, "state.json") }

func (s Store) Load() (State, error) {
	var st State
	b, err := os.ReadFile(s.Path())
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

func (s Store) Save(st State) error {
	if s.Dry {
		return nil
	}
	b, err := json.MarshalIndent(st, "", " ")
	if err != nil {
		return err
	}
	return fileutil.WriteAtomic(s.Path(), b)
}

// WithLock keeps a manual `up` from racing the service.
func (s Store) WithLock(f func() error) error {
	if s.Dry {
		return f()
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	lf, err := os.OpenFile(filepath.Join(s.Dir, "lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another splitroute is running (the service?); stop it first")
	}
	return f()
}

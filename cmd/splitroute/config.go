//go:build darwin || linux

// Config changes, presets and the lists command; also the api.Backend methods.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/aliraghebi/splitroute/internal/config"
	"github.com/aliraghebi/splitroute/internal/lists"
	"github.com/aliraghebi/splitroute/internal/store"
)

func (a *app) dbPath() string { return filepath.Join(a.state.Dir, "splitroute.db") }

// settings returns the settings database, opening it on first use. With
// canWrite it is opened read-write, created if needed, and on the first run
// filled from the old config file (or the defaults). Otherwise it is opened
// read-only; nil with no error means there is no database yet, and callers
// read the old config file instead.
func (a *app) settings() (*store.Store, error) {
	a.dbMu.Lock()
	defer a.dbMu.Unlock()
	if a.db != nil {
		return a.db, nil
	}
	if !a.canWrite {
		s, err := store.OpenReadOnly(a.dbPath())
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		a.db = s
		return s, nil
	}
	s, err := store.Open(a.dbPath())
	if err != nil {
		return nil, err
	}
	if err := a.seed(s); err != nil {
		s.Close()
		return nil, err
	}
	a.db = s
	return s, nil
}

// seed fills a new database from the old config file, or the defaults when
// there is none. An unreadable or invalid file is an error: starting with the
// defaults instead would silently change what gets routed.
func (a *app) seed(s *store.Store) error {
	if _, err := s.Config(); !errors.Is(err, store.ErrNotInitialized) {
		return err // already seeded (nil), or broken
	}
	cfg, err := config.Load(a.cfgPath)
	if err != nil {
		return err
	}
	from := a.cfgPath
	if _, err := os.Stat(a.cfgPath); err != nil {
		from = ""
	}
	ok, err := s.Init(cfg, from)
	if ok && from != "" {
		log.Printf("imported %s into %s; the file is no longer read (change settings with the app, `splitroute preset` or `splitroute config import`)", from, a.dbPath())
	}
	return err
}

func (a *app) closeSettings() {
	a.dbMu.Lock()
	defer a.dbMu.Unlock()
	if a.db != nil {
		a.db.Close()
		a.db = nil
	}
}

// LoadConfig reads the settings: from the database, or from the old config
// file while there is no database (before the first root run, and in dry-run).
func (a *app) LoadConfig() (config.Config, error) {
	s, err := a.settings()
	if err != nil {
		return config.Config{}, err
	}
	if s == nil {
		return config.Load(a.cfgPath)
	}
	cfg, err := s.Config()
	if errors.Is(err, store.ErrNotInitialized) {
		return config.Load(a.cfgPath)
	}
	return cfg, err
}

// UpdateConfig changes the settings with f in one database transaction and
// asks the service (if this process is it) to apply them now. Another
// process's service notices the write by itself (store.Changed).
func (a *app) UpdateConfig(f func(config.Config) (config.Config, error)) (config.Config, error) {
	if !a.canWrite {
		return config.Config{}, errors.New("changing settings needs root: run it with sudo")
	}
	s, err := a.settings()
	if err != nil {
		return config.Config{}, err
	}
	cfg, err := s.Update(f)
	if err != nil {
		return config.Config{}, err
	}
	a.wake()
	return cfg, nil
}

// settingsChanged reports whether another process changed the settings since
// the last call (the first call sets the baseline).
func (a *app) settingsChanged() bool {
	s, err := a.settings()
	if err != nil || s == nil {
		return false
	}
	c, err := s.Changed()
	if err != nil {
		log.Printf("warning: settings database: %v", err)
	}
	return c
}

// warnStaleFile logs once if the old config file was edited after it was
// imported: the edit has no effect, and silence would be confusing.
func (a *app) warnStaleFile(warned *bool) {
	if *warned {
		return
	}
	s, err := a.settings()
	if err != nil || s == nil {
		return
	}
	from, at, err := s.MigratedFrom()
	if err != nil || from == "" {
		return
	}
	if mt := modTime(from); mt.After(at) {
		log.Printf("warning: %s changed after it was imported into %s and is no longer read; use `splitroute config import %s`", from, a.dbPath(), from)
		*warned = true
	}
}

func (a *app) configCmd(args []string) error {
	switch {
	case len(args) == 1 && args[0] == "export":
		cfg, err := a.LoadConfig()
		if err != nil {
			return err
		}
		if cfg.Lists == nil {
			cfg.Lists = []config.List{}
		}
		b, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Printf("%s\n", b)
		return err
	case len(args) == 2 && args[0] == "import":
		b, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		cfg, err := config.Parse(b)
		if err != nil {
			return fmt.Errorf("%s: %w", args[1], err)
		}
		if a.dry {
			return json.NewEncoder(os.Stdout).Encode(cfg)
		}
		if _, err := a.UpdateConfig(func(config.Config) (config.Config, error) { return cfg, nil }); err != nil {
			return err
		}
		log.Printf("imported %s; the service applies it within %s (or run `sudo splitroute up`)", args[1], checkEvery)
		return nil
	}
	return errors.New("usage: splitroute config export | splitroute config import <file>")
}

// AvailableLists returns the list names in the latest release.
func (a *app) AvailableLists() ([]string, error) {
	cfg, err := a.LoadConfig()
	if err != nil {
		return nil, err
	}
	return lists.Available(cfg.ListsURL())
}

func (a *app) printLists() error {
	names, err := a.AvailableLists()
	if err != nil {
		return err
	}
	fmt.Println("Lists (only their IPv4 ranges are routed; domain-only lists have no effect):")
	for _, n := range names {
		fmt.Println("  " + n)
	}
	return nil
}

func (a *app) runPreset(args []string) error {
	if len(args) == 0 {
		for _, p := range config.Presets {
			fmt.Printf("%-10s %s\n", p.ID, p.Name)
			for _, l := range p.Lists {
				fmt.Printf("           %-6s %s\n", l.Action, l.Name)
			}
		}
		return nil
	}
	if a.dry {
		cfg, err := a.LoadConfig()
		if err == nil {
			cfg, err = config.WithPreset(cfg, args[0])
		}
		if err == nil {
			err = json.NewEncoder(os.Stdout).Encode(cfg)
		}
		return err
	}
	if _, err := a.UpdateConfig(func(c config.Config) (config.Config, error) { return config.WithPreset(c, args[0]) }); err != nil {
		return err
	}
	log.Printf("preset %s saved to %s; the service applies it within %s (or run `sudo splitroute up`)", args[0], a.dbPath(), checkEvery)
	return nil
}

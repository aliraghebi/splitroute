//go:build darwin || linux

// Config changes, presets and the lists command; also the api.Backend methods.

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/aliraghebi/splitroute/internal/config"
	"github.com/aliraghebi/splitroute/internal/lists"
)

func (a *app) LoadConfig() (config.Config, error) { return config.Load(a.cfgPath) }

// UpdateConfig loads the config, changes it with f, saves it and asks the
// service (if this process is it) to apply it now.
func (a *app) UpdateConfig(f func(config.Config) (config.Config, error)) (config.Config, error) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	cfg, err := config.Load(a.cfgPath)
	if err != nil {
		return config.Config{}, err
	}
	if cfg, err = f(cfg); err != nil {
		return config.Config{}, err
	}
	if err := config.Save(a.cfgPath, cfg); err != nil {
		return config.Config{}, err
	}
	if a.reload != nil {
		select {
		case a.reload <- struct{}{}:
		default: // a reload is already pending
		}
	}
	return cfg, nil
}

// AvailableLists returns the list names in the latest release.
func (a *app) AvailableLists() ([]string, error) {
	cfg, err := config.Load(a.cfgPath)
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
		cfg, err := config.Load(a.cfgPath)
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
	log.Printf("preset %s saved to %s; the service applies it within %s (or run `sudo splitroute up`)", args[0], a.cfgPath, checkEvery)
	return nil
}

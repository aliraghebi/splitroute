//go:build darwin || linux

package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"os/user"
	"strconv"
	"time"
)

// The control socket speaks one JSON request and one JSON response per
// connection. The menu bar app uses it; the daemon is the only writer of the
// config, so the app never needs root.
//
//	{"cmd": "status"}                          → status
//	{"cmd": "get_config"}                      → config
//	{"cmd": "set_config", "config": {...}}     → config (validated, saved, applied)
//	{"cmd": "set_enabled", "enabled": false}   → config
//	{"cmd": "apply_preset", "preset": "iran"}  → config
//	{"cmd": "lists"}                           → lists available in the latest release
//	{"cmd": "presets"}                         → presets

type request struct {
	Cmd     string  `json:"cmd"`
	Config  *Config `json:"config,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
	Preset  string  `json:"preset,omitempty"`
}

type response struct {
	OK      bool     `json:"ok"`
	Error   string   `json:"error,omitempty"`
	Status  *Status  `json:"status,omitempty"`
	Config  *Config  `json:"config,omitempty"`
	Lists   []string `json:"lists,omitempty"`
	Presets []Preset `json:"presets,omitempty"`
}

const (
	maxRequest  = 1 << 20
	connTimeout = 90 * time.Second // "lists" downloads SHA256SUMS
)

func (a *app) sockPath() string {
	if a.goos == "darwin" {
		return "/var/run/splitroute.sock"
	}
	return "/run/splitroute.sock"
}

// listen opens the control socket. On macOS members of the admin group may
// connect (they can sudo anyway); elsewhere only root.
func (a *app) listen() (net.Listener, error) {
	p := a.sockPath()
	_ = os.Remove(p) // stale socket from a crash; watch holds the lock, so no other daemon owns it
	ln, err := net.Listen("unix", p)
	if err != nil {
		return nil, err
	}
	mode := os.FileMode(0o600)
	if a.goos == "darwin" {
		if g, err := user.LookupGroup("admin"); err == nil {
			if gid, err := strconv.Atoi(g.Gid); err == nil && os.Chown(p, 0, gid) == nil {
				mode = 0o660
			}
		}
	}
	if err := os.Chmod(p, mode); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func (a *app) serve(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				log.Printf("control socket: %v", err)
			}
			return
		}
		go a.handle(c)
	}
}

func (a *app) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(connTimeout))
	var req request
	var resp response
	if err := json.NewDecoder(io.LimitReader(c, maxRequest)).Decode(&req); err != nil {
		resp = response{Error: "bad request: " + err.Error()}
	} else {
		resp = a.respond(req)
	}
	_ = json.NewEncoder(c).Encode(resp)
}

func (a *app) respond(req request) response {
	var r response
	var err error
	switch req.Cmd {
	case "status":
		var s Status
		s, err = a.buildStatus()
		r.Status = &s
	case "get_config":
		var cfg Config
		cfg, err = loadConfig(a.cfgPath)
		r.Config = &cfg
	case "set_config":
		if req.Config == nil {
			return response{Error: "set_config needs a config"}
		}
		r.Config, err = a.respondConfig(func(Config) (Config, error) { return *req.Config, nil })
	case "set_enabled":
		if req.Enabled == nil {
			return response{Error: "set_enabled needs enabled"}
		}
		r.Config, err = a.respondConfig(func(c Config) (Config, error) { c.Disabled = !*req.Enabled; return c, nil })
	case "apply_preset":
		r.Config, err = a.respondConfig(func(c Config) (Config, error) { return withPreset(c, req.Preset) })
	case "lists":
		r.Lists, err = a.availableLists()
	case "presets":
		r.Presets = presets
	default:
		err = errors.New("unknown command " + strconv.Quote(req.Cmd))
	}
	if err != nil {
		return response{Error: err.Error()}
	}
	r.OK = true
	return r
}

func (a *app) respondConfig(f func(Config) (Config, error)) (*Config, error) {
	cfg, err := a.updateConfig(f)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

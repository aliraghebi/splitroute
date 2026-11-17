//go:build darwin || linux

// Package api is the control socket. It speaks one JSON request and one JSON
// response per connection. The menu bar app uses it; the daemon is the only
// writer of the config, so the app never needs root.
//
//	{"cmd": "status"}                          → status
//	{"cmd": "get_config"}                      → config
//	{"cmd": "set_config", "config": {...}}     → config (validated, saved, applied)
//	{"cmd": "set_enabled", "enabled": false}   → config
//	{"cmd": "apply_preset", "preset": "iran"}  → config
//	{"cmd": "lists"}                           → lists available in the latest release
//	{"cmd": "presets"}                         → presets
package api

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

	"github.com/aliraghebi/splitroute/internal/config"
)

// Backend is what the socket needs from the daemon.
type Backend interface {
	Status() (Status, error)
	LoadConfig() (config.Config, error)
	// UpdateConfig loads the config, changes it with f, validates, saves and
	// applies it.
	UpdateConfig(f func(config.Config) (config.Config, error)) (config.Config, error)
	AvailableLists() ([]string, error)
}

type Request struct {
	Cmd     string         `json:"cmd"`
	Config  *config.Config `json:"config,omitempty"`
	Enabled *bool          `json:"enabled,omitempty"`
	Preset  string         `json:"preset,omitempty"`
}

type Response struct {
	OK      bool            `json:"ok"`
	Error   string          `json:"error,omitempty"`
	Status  *Status         `json:"status,omitempty"`
	Config  *config.Config  `json:"config,omitempty"`
	Lists   []string        `json:"lists,omitempty"`
	Presets []config.Preset `json:"presets,omitempty"`
}

const (
	maxRequest  = 1 << 20
	connTimeout = 90 * time.Second // "lists" downloads SHA256SUMS
)

// SockPath is the socket's path on goos.
func SockPath(goos string) string {
	if goos == "darwin" {
		return "/var/run/splitroute.sock"
	}
	return "/run/splitroute.sock"
}

// Listen opens the control socket. On macOS members of the admin group may
// connect (they can sudo anyway); elsewhere only root.
func Listen(goos string) (net.Listener, error) {
	p := SockPath(goos)
	_ = os.Remove(p) // stale socket from a crash; watch holds the lock, so no other daemon owns it
	ln, err := net.Listen("unix", p)
	if err != nil {
		return nil, err
	}
	mode := os.FileMode(0o600)
	if goos == "darwin" {
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

// Serve handles connections until ln is closed.
func Serve(ln net.Listener, b Backend) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				log.Printf("control socket: %v", err)
			}
			return
		}
		go Handle(c, b)
	}
}

// Handle reads one request from c, answers it and closes c.
func Handle(c net.Conn, b Backend) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(connTimeout))
	var req Request
	var resp Response
	if err := json.NewDecoder(io.LimitReader(c, maxRequest)).Decode(&req); err != nil {
		resp = Response{Error: "bad request: " + err.Error()}
	} else {
		resp = Respond(b, req)
	}
	_ = json.NewEncoder(c).Encode(resp)
}

// Respond answers one request.
func Respond(b Backend, req Request) Response {
	var r Response
	var err error
	switch req.Cmd {
	case "status":
		var s Status
		s, err = b.Status()
		r.Status = &s
	case "get_config":
		var cfg config.Config
		cfg, err = b.LoadConfig()
		r.Config = &cfg
	case "set_config":
		if req.Config == nil {
			return Response{Error: "set_config needs a config"}
		}
		r.Config, err = update(b, func(config.Config) (config.Config, error) { return *req.Config, nil })
	case "set_enabled":
		if req.Enabled == nil {
			return Response{Error: "set_enabled needs enabled"}
		}
		r.Config, err = update(b, func(c config.Config) (config.Config, error) { c.Disabled = !*req.Enabled; return c, nil })
	case "apply_preset":
		r.Config, err = update(b, func(c config.Config) (config.Config, error) { return config.WithPreset(c, req.Preset) })
	case "lists":
		r.Lists, err = b.AvailableLists()
	case "presets":
		r.Presets = config.Presets
	default:
		err = errors.New("unknown command " + strconv.Quote(req.Cmd))
	}
	if err != nil {
		return Response{Error: err.Error()}
	}
	r.OK = true
	return r
}

func update(b Backend, f func(config.Config) (config.Config, error)) (*config.Config, error) {
	cfg, err := b.UpdateConfig(f)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

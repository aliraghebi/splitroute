//go:build darwin || linux

package api

import (
	"errors"
	"strconv"
	"time"

	"github.com/aliraghebi/splitroute/internal/tunnel"
)

// Tunnel is a stored tunnel and its live state. The daemon never returns
// credentials or profile text.
type Tunnel struct {
	Name         string       `json:"name"`
	Remotes      []string     `json:"remotes,omitempty"`
	NeedsAuth    bool         `json:"needs_auth"`
	KeyEncrypted bool         `json:"key_encrypted"`
	Scripts      []string     `json:"scripts,omitempty"` // confirmed script directives
	AutoConnect  bool         `json:"auto_connect"`
	State        tunnel.State `json:"state"`
	Error        string       `json:"error,omitempty"`
	Need         string       `json:"need,omitempty"` // "auth" or "key" while State is needs_auth
	LocalIP      string       `json:"local_ip,omitempty"`
	RemoteIP     string       `json:"remote_ip,omitempty"`
	Since        time.Time    `json:"since,omitzero"`
	BytesIn      uint64       `json:"bytes_in,omitempty"`
	BytesOut     uint64       `json:"bytes_out,omitempty"`
}

// TunnelCheck is what a profile asks for, shown before it is imported.
type TunnelCheck struct {
	Remotes        []string `json:"remotes"`
	NeedsAuth      bool     `json:"needs_auth"`
	KeyEncrypted   bool     `json:"key_encrypted"`
	Scripts        []string `json:"scripts,omitempty"`
	ScriptSecurity int      `json:"script_security"`
}

// TunnelList is the answer to "tunnels".
type TunnelList struct {
	Tunnels []Tunnel `json:"tunnels"`
	// Unavailable is why tunnels can't run here (no OpenVPN, wrong hash), or "".
	Unavailable string `json:"unavailable,omitempty"`
}

// Tunnels is what the socket needs from the daemon for tunnels. Tunnels are
// addressed by name.
type Tunnels interface {
	Tunnels() (TunnelList, error)
	TunnelImport(name string, profile []byte, allowScripts bool) (Tunnel, error)
	TunnelRemove(name string) error
	TunnelRename(name, newName string) error
	TunnelAutoConnect(name string, on bool) error
	TunnelConnect(name string, c tunnel.Credentials) error
	TunnelCredentials(name string, c tunnel.Credentials) error
	TunnelDisconnect(name string) error
	TunnelLogs(name string) ([]string, error)
}

func checkOf(p tunnel.Profile) *TunnelCheck {
	return &TunnelCheck{Remotes: p.Remotes, NeedsAuth: p.NeedsAuth, KeyEncrypted: p.KeyEncrypted, Scripts: p.Scripts, ScriptSecurity: p.ScriptSecurity}
}

func credsOf(req Request) tunnel.Credentials {
	return tunnel.Credentials{User: req.Username, Pass: req.Password, KeyPass: req.KeyPassword,
		HaveAuth: req.Username != "" || req.Password != "", HaveKey: req.KeyPassword != ""}
}

// respondTunnel answers the tunnel_* commands and "tunnels".
func respondTunnel(b Backend, req Request) (r Response, handled bool) {
	if len(req.Cmd) < 7 || req.Cmd[:6] != "tunnel" {
		return r, false
	}
	t, ok := b.(Tunnels)
	if !ok {
		return Response{Error: "this build has no tunnel support"}, true
	}
	need := func() bool { return req.Name != "" }
	var err error
	switch req.Cmd {
	case "tunnels":
		var l TunnelList
		if l, err = t.Tunnels(); err == nil {
			r.Tunnels = &l
		}
	case "tunnel_check":
		var p tunnel.Profile
		if p, err = tunnel.ParseProfile([]byte(req.Text)); err == nil {
			r.Check = checkOf(p)
		}
	case "tunnel_import":
		if !need() || req.Text == "" {
			return Response{Error: "tunnel_import needs a name and the profile text"}, true
		}
		var tn Tunnel
		if tn, err = t.TunnelImport(req.Name, []byte(req.Text), req.AllowScripts); err == nil {
			r.Tunnel = &tn
		}
	case "tunnel_remove", "tunnel_disconnect", "tunnel_logs", "tunnel_rename", "tunnel_auto_connect", "tunnel_connect", "tunnel_credentials":
		if !need() {
			return Response{Error: req.Cmd + " needs a name"}, true
		}
		switch req.Cmd {
		case "tunnel_remove":
			err = t.TunnelRemove(req.Name)
		case "tunnel_disconnect":
			err = t.TunnelDisconnect(req.Name)
		case "tunnel_logs":
			r.Logs, err = t.TunnelLogs(req.Name)
		case "tunnel_rename":
			err = t.TunnelRename(req.Name, req.NewName)
		case "tunnel_auto_connect":
			if req.Enabled == nil {
				return Response{Error: "tunnel_auto_connect needs enabled"}, true
			}
			err = t.TunnelAutoConnect(req.Name, *req.Enabled)
		case "tunnel_connect":
			err = t.TunnelConnect(req.Name, credsOf(req))
		case "tunnel_credentials":
			err = t.TunnelCredentials(req.Name, credsOf(req))
		}
	default:
		err = errors.New("unknown command " + strconv.Quote(req.Cmd))
	}
	if err != nil {
		return Response{Error: err.Error()}, true
	}
	r.OK = true
	return r, true
}

//go:build darwin || linux

// Tunnels: OpenVPN profiles, run and supervised by the service.

package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/aliraghebi/splitroute/internal/api"
	"github.com/aliraghebi/splitroute/internal/store"
	"github.com/aliraghebi/splitroute/internal/tunnel"
)

// openvpnSHA256 pins the bundled OpenVPN (set at build time by
// macos/build-app.sh with -ldflags "-X main.openvpnSHA256=…"). The service
// runs as root, so it refuses to start a binary that doesn't match. Empty in
// builds that don't bundle one.
var openvpnSHA256 string

func (a *app) tunnelsDir() string { return filepath.Join(a.state.Dir, "tunnels") }

func (a *app) profilePath(id int64) string {
	return filepath.Join(a.tunnelsDir(), strconv.FormatInt(id, 10), "profile.ovpn")
}

// openvpnPath finds the OpenVPN that ships next to this binary and checks it
// against the pinned hash.
func (a *app) openvpnPath() (string, error) {
	if a.openvpnPathForTest != "" {
		return a.openvpnPathForTest, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	p := filepath.Join(filepath.Dir(exe), "openvpn")
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", errors.New("OpenVPN is not installed (it ships inside the Splitroute app)")
	}
	if err != nil {
		return "", err
	}
	// No pin, no tunnels: a daemon built without one (the CLI archives, go run)
	// could be made to run a binary dropped next to it, as root.
	if openvpnSHA256 == "" {
		return "", errors.New("this build of splitroute can't run tunnels (it has no pinned OpenVPN); use the Splitroute app")
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != openvpnSHA256 {
		return "", errors.New("the bundled OpenVPN does not match the build; reinstall Splitroute")
	}
	return p, nil
}

// secureDir creates dir (root-only) or checks that an existing one is not
// open to other users or a symlink.
func secureDir(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || fi.Mode()&0o077 != 0 || !ok || int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%s must be a directory owned by this user with mode 0700", dir)
	}
	return nil
}

func (a *app) tunnelRunDir() string {
	if a.runDirForTest != "" {
		return a.runDirForTest
	}
	return filepath.Join(filepath.Dir(api.SockPath(a.goos)), "splitroute-tunnels")
}

// ovpnLauncher starts OpenVPN for a stored tunnel.
type ovpnLauncher struct{ a *app }

func (l ovpnLauncher) Start(ctx context.Context, idStr string) (net.Conn, tunnel.Process, error) {
	a := l.a
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return nil, nil, err
	}
	s, err := a.settings()
	if err != nil || s == nil {
		return nil, nil, fmt.Errorf("settings: %v", err)
	}
	t, err := s.TunnelByID(id)
	if err != nil {
		return nil, nil, err
	}
	// The profile is read once; what is hashed and checked is what OpenVPN
	// gets (it reads the same file, so a swap in between needs root anyway).
	profile := a.profilePath(id)
	text, err := os.ReadFile(profile)
	if err != nil {
		return nil, nil, fmt.Errorf("profile: %w", err)
	}
	if sum := sha256.Sum256(text); hex.EncodeToString(sum[:]) != t.ProfileSHA256 {
		return nil, nil, errors.New("the stored profile changed since it was imported; import it again")
	}
	pr, err := tunnel.ParseProfile(text)
	if err != nil {
		return nil, nil, err
	}
	for _, d := range pr.Scripts { // a profile stored by an older, laxer check
		if !slices.Contains(t.Scripts, d) {
			return nil, nil, fmt.Errorf("the profile runs %q, which was never confirmed; import it again", d)
		}
	}
	src, err := a.openvpnPath()
	if err != nil {
		return nil, nil, err
	}
	runDir := a.tunnelRunDir()
	if err := secureDir(runDir); err != nil {
		return nil, nil, err
	}
	// Run a copy of exactly the bytes that were hashed, from the root-only dir:
	// hashing and exec'ing the same path leaves a window to swap the file.
	bin, err := a.copyOpenVPN(src, runDir)
	if err != nil {
		return nil, nil, err
	}
	sock := filepath.Join(runDir, idStr+".sock")
	_ = os.Remove(sock)
	args := []string{"--config", profile, "--cd", filepath.Dir(profile),
		"--management", sock, "unix", "--management-hold", "--management-query-passwords", "--verb", "3"}
	if len(t.Scripts) == 0 {
		// Last option wins: whatever the profile says, nothing runs unless the user confirmed scripts.
		args = append(args, "--script-security", "1")
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"} // nothing inherited from whoever started the service
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	p := &osProc{cmd: cmd, done: make(chan struct{})}
	go func() { // OpenVPN's output is the log: it also has what is printed before the socket exists
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			a.tunnels.AddLog(idStr, sc.Text())
		}
		_, _ = io.Copy(io.Discard, out)
		p.err = cmd.Wait()
		_ = os.Remove(sock)
		close(p.done)
	}()
	conn, err := tunnel.Dial(sock, 10*time.Second, p.done)
	if err != nil {
		_ = p.Kill()
		<-p.done
		return nil, nil, err
	}
	return conn, p, nil
}

// copyOpenVPN verifies src against the pin and writes the verified bytes to
// runDir, returning the copy's path.
func (a *app) copyOpenVPN(src, runDir string) (string, error) {
	b, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	if a.openvpnPathForTest == "" {
		if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != openvpnSHA256 {
			return "", errors.New("the bundled OpenVPN does not match the build; reinstall Splitroute")
		}
	}
	dst := filepath.Join(runDir, "openvpn")
	_ = os.Remove(dst)
	if err := os.WriteFile(dst, b, 0o700); err != nil {
		return "", err
	}
	return dst, nil
}

type osProc struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func (p *osProc) Wait() error { <-p.done; return p.err }
func (p *osProc) Kill() error { return p.cmd.Process.Kill() }

var tunnelMu sync.Mutex // serializes imports and removals (file and row change together)

func (a *app) tunnelStore() (*store.Store, error) {
	if !a.canWrite {
		return nil, errors.New("tunnels need root: run it with sudo")
	}
	s, err := a.settings()
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("settings are not initialized")
	}
	return s, nil
}

func (a *app) tunnelByName(s *store.Store, name string) (store.Tunnel, error) {
	ts, err := s.Tunnels()
	if err != nil {
		return store.Tunnel{}, err
	}
	for _, t := range ts {
		if t.Name == name {
			return t, nil
		}
	}
	return store.Tunnel{}, fmt.Errorf("no tunnel named %q", name)
}

func idOf(t store.Tunnel) string { return strconv.FormatInt(t.ID, 10) }

func apiTunnel(t store.Tunnel, i tunnel.Info) api.Tunnel {
	return api.Tunnel{Name: t.Name, Remotes: t.Remotes, NeedsAuth: t.NeedsAuth, KeyEncrypted: t.KeyEncrypted,
		Scripts: t.Scripts, AutoConnect: t.AutoConnect, State: i.State, Error: i.Error, Need: i.Need,
		LocalIP: i.LocalIP, RemoteIP: i.RemoteIP, Since: i.Since, BytesIn: i.BytesIn, BytesOut: i.BytesOut}
}

func (a *app) Tunnels() (api.TunnelList, error) {
	l := api.TunnelList{Tunnels: []api.Tunnel{}}
	if _, err := a.openvpnPath(); err != nil {
		l.Unavailable = err.Error()
	}
	s, err := a.settings()
	if err != nil || s == nil {
		return l, err
	}
	ts, err := s.Tunnels()
	if err != nil {
		return l, err
	}
	for _, t := range ts {
		l.Tunnels = append(l.Tunnels, apiTunnel(t, a.tunnels.Info(idOf(t))))
	}
	return l, nil
}

func (a *app) TunnelImport(name string, text []byte, allowScripts bool) (api.Tunnel, error) {
	s, err := a.tunnelStore()
	if err != nil {
		return api.Tunnel{}, err
	}
	p, err := tunnel.ParseProfile(text)
	if err != nil {
		return api.Tunnel{}, err
	}
	if len(p.Scripts) > 0 && !allowScripts {
		return api.Tunnel{}, fmt.Errorf("the profile runs commands as root (%v); confirm them to import it", p.Scripts)
	}
	tunnelMu.Lock()
	defer tunnelMu.Unlock()
	sum := sha256.Sum256(text)
	t := store.Tunnel{Name: name, ProfileSHA256: hex.EncodeToString(sum[:]), Remotes: p.Remotes,
		NeedsAuth: p.NeedsAuth, KeyEncrypted: p.KeyEncrypted, Scripts: p.Scripts}
	if t.ID, err = s.AddTunnel(t); err != nil {
		return api.Tunnel{}, err
	}
	if err := a.writeProfile(t.ID, text); err != nil {
		_ = s.RemoveTunnel(t.ID)
		_ = os.RemoveAll(filepath.Dir(a.profilePath(t.ID)))
		return api.Tunnel{}, err
	}
	return apiTunnel(t, tunnel.Info{State: tunnel.Disconnected}), nil
}

// writeProfile stores a profile in a root-only directory of its own.
func (a *app) writeProfile(id int64, text []byte) error {
	if err := secureDir(a.tunnelsDir()); err != nil {
		return err
	}
	if err := secureDir(filepath.Dir(a.profilePath(id))); err != nil {
		return err
	}
	return writePrivate(a.profilePath(id), text)
}

// writePrivate writes a 0600 file atomically.
func writePrivate(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (a *app) TunnelRemove(name string) error {
	s, err := a.tunnelStore()
	if err != nil {
		return err
	}
	tunnelMu.Lock()
	defer tunnelMu.Unlock()
	t, err := a.tunnelByName(s, name)
	if err != nil {
		return err
	}
	a.tunnels.Forget(idOf(t)) // stops it first
	// Files first: if this fails the row stays and the removal can be retried.
	if err := os.RemoveAll(filepath.Dir(a.profilePath(t.ID))); err != nil {
		return err
	}
	return s.RemoveTunnel(t.ID)
}

func (a *app) TunnelRename(name, newName string) error {
	s, err := a.tunnelStore()
	if err != nil {
		return err
	}
	t, err := a.tunnelByName(s, name)
	if err != nil {
		return err
	}
	return s.RenameTunnel(t.ID, newName)
}

func (a *app) TunnelAutoConnect(name string, on bool) error {
	s, err := a.tunnelStore()
	if err != nil {
		return err
	}
	t, err := a.tunnelByName(s, name)
	if err != nil {
		return err
	}
	return s.SetTunnelAutoConnect(t.ID, on)
}

func (a *app) TunnelConnect(name string, c tunnel.Credentials) error {
	tunnelMu.Lock() // not while a remove is in progress
	defer tunnelMu.Unlock()
	s, err := a.tunnelStore()
	if err != nil {
		return err
	}
	t, err := a.tunnelByName(s, name)
	if err != nil {
		return err
	}
	if _, err := a.openvpnPath(); err != nil {
		return err
	}
	return a.tunnels.Connect(idOf(t), c)
}

func (a *app) TunnelCredentials(name string, c tunnel.Credentials) error {
	s, err := a.tunnelStore()
	if err != nil {
		return err
	}
	t, err := a.tunnelByName(s, name)
	if err != nil {
		return err
	}
	return a.tunnels.SetCredentials(idOf(t), c)
}

func (a *app) TunnelDisconnect(name string) error {
	s, err := a.tunnelStore()
	if err != nil {
		return err
	}
	t, err := a.tunnelByName(s, name)
	if err != nil {
		return err
	}
	a.tunnels.Disconnect(idOf(t))
	return nil
}

func (a *app) TunnelLogs(name string) ([]string, error) {
	s, err := a.settings()
	if err != nil || s == nil {
		return nil, err
	}
	t, err := a.tunnelByName(s, name)
	if err != nil {
		return nil, err
	}
	return a.tunnels.Logs(idOf(t)), nil
}

// startAutoTunnels connects the tunnels marked auto-connect. One that needs
// credentials waits (needs_auth) until the app sends them.
func (a *app) startAutoTunnels() {
	s, err := a.settings()
	if err != nil || s == nil || !a.canWrite {
		return
	}
	ts, err := s.Tunnels()
	if err != nil {
		log.Printf("tunnels: %v", err)
		return
	}
	for _, t := range ts {
		if !t.AutoConnect {
			continue
		}
		if err := a.TunnelConnect(t.Name, tunnel.Credentials{}); err != nil {
			log.Printf("tunnel %s: %v", t.Name, err)
		}
	}
}

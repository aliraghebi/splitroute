//go:build darwin || linux

package main

import (
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aliraghebi/splitroute/internal/api"
	"github.com/aliraghebi/splitroute/internal/config"
	"github.com/aliraghebi/splitroute/internal/rules"
)

// testApp is an app whose state dir and config file live in a temp dir. With
// canWrite it opens the settings database read-write, as root would.
func testApp(t *testing.T, dry, canWrite bool) *app {
	t.Helper()
	dir := t.TempDir()
	a := newApp("linux", filepath.Join(dir, "splitroute.conf"), dry)
	a.state.Dir = filepath.Join(dir, "state")
	a.canWrite = canWrite
	t.Cleanup(a.closeSettings)
	return a
}

func TestRespondConfig(t *testing.T) {
	a := testApp(t, false, true)
	ok := func(r api.Response) api.Response {
		t.Helper()
		if !r.OK {
			t.Fatalf("error: %s", r.Error)
		}
		return r
	}
	if r := ok(api.Respond(a, api.Request{Cmd: "get_config"})); !reflect.DeepEqual(*r.Config, config.Default()) {
		t.Fatalf("default config: %+v", r.Config)
	}
	off := false
	if r := ok(api.Respond(a, api.Request{Cmd: "set_enabled", Enabled: &off})); !r.Config.Disabled {
		t.Fatal("set_enabled false: still enabled")
	}
	bad := config.Config{Lists: []config.List{{Name: "x", Custom: true, Action: rules.Direct, Entries: []string{"not-an-ip"}}}}
	if r := api.Respond(a, api.Request{Cmd: "set_config", Config: &bad}); r.OK || r.Error == "" {
		t.Fatal("invalid config accepted")
	}
	if r := ok(api.Respond(a, api.Request{Cmd: "apply_preset", Preset: "iran"})); !r.Config.Disabled || r.Config.Lists[0].Name != "ir-cidr" {
		t.Fatalf("preset lost disabled or lists: %+v", r.Config)
	}
	saved, err := a.LoadConfig()
	if err != nil || !saved.Disabled || len(saved.Lists) != len(config.Presets[0].Lists) {
		t.Fatalf("saved %+v, %v", saved, err)
	}
	if r := api.Respond(a, api.Request{Cmd: "rm -rf"}); r.OK {
		t.Fatal("unknown command accepted")
	}
}

// Dry-run without a database reads the old config file.
func TestWantDisabledAndCustom(t *testing.T) {
	a := testApp(t, true, false)
	custom := config.Config{Lists: []config.List{
		{Name: "Work", Custom: true, Action: rules.Block, Entries: []string{"203.0.113.7", "example.com", "2400:cb00::/32"}},
		{Name: "Sites", Custom: true, Action: rules.Direct, Entries: []string{"example.org", "AS13335"}},
	}}
	if err := config.Save(a.cfgPath, custom); err != nil {
		t.Fatal(err)
	}
	got, warn, err := a.want() // no release lists: no network
	if err != nil || !reflect.DeepEqual(got, []rules.Route{{Prefix: netip.MustParsePrefix("203.0.113.7/32"), Action: rules.Block, List: "Work"}}) {
		t.Fatalf("got %v, %v", got, err)
	}
	// Only IPv4 is routed; a list with nothing routable says so.
	if !reflect.DeepEqual(warn, []string{"Sites has no routable IPv4 ranges (1 domains, 1 AS numbers)"}) {
		t.Fatalf("warnings %q", warn)
	}
	custom.Disabled = true
	if err := config.Save(a.cfgPath, custom); err != nil {
		t.Fatal(err)
	}
	if got, _, err := a.want(); err != nil || len(got) != 0 {
		t.Fatalf("disabled: got %v, %v", got, err)
	}
}

func TestFirstRunImportsConfigFile(t *testing.T) {
	a := testApp(t, false, true)
	if err := os.WriteFile(a.cfgPath, []byte("ir-cidr = direct\nmalware = block\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := config.Config{Lists: []config.List{{Name: "ir-cidr", Action: rules.Direct}, {Name: "malware", Action: rules.Block}}}
	if got, err := a.LoadConfig(); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v", got, err)
	}
	// The file is no longer read once imported.
	if err := os.WriteFile(a.cfgPath, []byte("telegram = direct\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.closeSettings()
	if got, err := a.LoadConfig(); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("after editing the file: %+v, %v", got, err)
	}
	// Non-root readers see the database, not the file.
	ro := testApp(t, false, false)
	ro.state.Dir, ro.cfgPath = a.state.Dir, a.cfgPath
	if got, err := ro.LoadConfig(); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("read-only: %+v, %v", got, err)
	}
	if _, err := ro.UpdateConfig(func(c config.Config) (config.Config, error) { return c, nil }); err == nil {
		t.Fatal("non-root update accepted")
	}
}

func TestFirstRunRefusesBadConfigFile(t *testing.T) {
	a := testApp(t, false, true)
	if err := os.WriteFile(a.cfgPath, []byte("ir-cidr = proxy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.LoadConfig(); err == nil {
		t.Fatal("invalid config file imported (or replaced by defaults)")
	}
	// Once the file is fixed, the next run imports it.
	if err := os.WriteFile(a.cfgPath, []byte("malware = block\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := a.LoadConfig(); err != nil || len(got.Lists) != 1 || got.Lists[0].Name != "malware" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestConfigImportExport(t *testing.T) {
	a := testApp(t, false, true)
	in := filepath.Join(t.TempDir(), "in.json")
	if err := os.WriteFile(in, []byte(`{"lists":[{"name":"Work","custom":true,"action":"block","entries":["# lab","203.0.113.0/24"]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.configCmd([]string{"import", in}); err != nil {
		t.Fatal(err)
	}
	got, err := a.LoadConfig()
	want := config.Config{Lists: []config.List{{Name: "Work", Custom: true, Action: rules.Block, Entries: []string{"# lab", "203.0.113.0/24"}}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v", got, err)
	}
	if err := os.WriteFile(in, []byte(`{"lists":[{"name":"x","custom":true,"action":"direct","entries":["10.0.0.1"]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.configCmd([]string{"import", in}); err == nil {
		t.Fatal("invalid import accepted")
	}
	if after, _ := a.LoadConfig(); !reflect.DeepEqual(after, want) {
		t.Fatalf("failed import changed settings: %+v", after)
	}
	for _, bad := range [][]string{{}, {"export", "x"}, {"import"}, {"rm"}} {
		if err := a.configCmd(bad); err == nil {
			t.Errorf("config %q: want usage error", bad)
		}
	}
}

// A root CLI command writing the database is seen by the service's handle.
func TestSettingsChangedAcrossProcesses(t *testing.T) {
	daemon := testApp(t, false, true)
	daemon.settingsChanged() // baseline
	cli := testApp(t, false, true)
	cli.state.Dir, cli.cfgPath = daemon.state.Dir, daemon.cfgPath
	if daemon.settingsChanged() {
		t.Fatal("changed before any write")
	}
	if _, err := cli.UpdateConfig(func(c config.Config) (config.Config, error) { return config.WithPreset(c, "security") }); err != nil {
		t.Fatal(err)
	}
	if !daemon.settingsChanged() {
		t.Fatal("service missed a CLI write")
	}
	if got, err := daemon.LoadConfig(); err != nil || got.Lists[0].Name != "malware" {
		t.Fatalf("service reads %+v, %v", got, err)
	}
}

func TestListCommand(t *testing.T) {
	a := testApp(t, false, true)
	var out strings.Builder
	a.stdout = &out
	run := func(args ...string) error {
		t.Helper()
		out.Reset()
		return a.listCmd(args)
	}
	for _, args := range [][]string{
		{"create", "Work", "block"},
		{"enable", "malware", "block"},
		{"add", "Work", "203.0.113.7", "Example.COM", "2400:cb00::/32"},
		{"move", "Work", "1"},
		{"rename", "Work", "Office"},
		{"action", "Office", "direct"},
	} {
		if err := run(args...); err != nil {
			t.Fatalf("%q: %v", args, err)
		}
	}
	a.stdin = strings.NewReader("# from stdin\nAS13335\nexample.com\n")
	if err := run("add", "Office", "-"); err != nil {
		t.Fatal(err)
	}
	if err := run("delete", "Office", "203.0.113.7"); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	want := "  1  direct  Office                custom: 1 domains, 1 IPv6, 1 AS numbers\n" +
		"  2  direct  ir-cidr               release\n" +
		"  3  block   malware               release\n"
	if out.String() != want {
		t.Fatalf("list:\n%s\nwant:\n%s", out.String(), want)
	}
	if err := run("show", "Office"); err != nil || out.String() != "example.com\n2400:cb00::/32\n# from stdin\nAS13335\n" {
		t.Fatalf("show: %q, %v", out.String(), err)
	}
	for _, bad := range [][]string{
		{"show", "malware"}, {"show", "nope"}, {"create"}, {"create", "a", "b", "c"}, {"move", "Office", "x"},
		{"move", "Office", "9"}, {"add", "Office"}, {"add", "malware", "1.2.3.4"}, {"add", "Office", "10.0.0.1"},
		{"rename", "malware", "x"}, {"remove", "nope"}, {"help"},
	} {
		if err := run(bad...); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
	// Dry-run prints the result and saves nothing.
	dry := testApp(t, true, false)
	dry.stdout = &out
	out.Reset()
	if err := dry.listCmd([]string{"create", "Home"}); err != nil || !strings.Contains(out.String(), `"name":"Home"`) {
		t.Fatalf("dry-run: %q, %v", out.String(), err)
	}
	if cfg, _ := dry.LoadConfig(); len(cfg.Lists) != 1 {
		t.Fatalf("dry-run saved: %+v", cfg)
	}
}

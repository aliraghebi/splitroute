//go:build darwin || linux

package main

import (
	"net/netip"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aliraghebi/splitroute/internal/api"
	"github.com/aliraghebi/splitroute/internal/config"
	"github.com/aliraghebi/splitroute/internal/rules"
)

func TestRespondConfig(t *testing.T) {
	a := newApp("linux", filepath.Join(t.TempDir(), "splitroute.conf"), false)
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
	saved, err := config.Load(a.cfgPath)
	if err != nil || !saved.Disabled || len(saved.Lists) != len(config.Presets[0].Lists) {
		t.Fatalf("saved %+v, %v", saved, err)
	}
	if r := api.Respond(a, api.Request{Cmd: "rm -rf"}); r.OK {
		t.Fatal("unknown command accepted")
	}
}

func TestWantDisabledAndCustom(t *testing.T) {
	a := newApp("linux", filepath.Join(t.TempDir(), "splitroute.conf"), true)
	custom := config.Config{Lists: []config.List{{Name: "Work", Custom: true, Action: rules.Block, Entries: []string{"203.0.113.7"}}}}
	if err := config.Save(a.cfgPath, custom); err != nil {
		t.Fatal(err)
	}
	got, _, err := a.want() // no release lists: no network
	if err != nil || !reflect.DeepEqual(got, []rules.Route{{Prefix: netip.MustParsePrefix("203.0.113.7/32"), Action: rules.Block, List: "Work"}}) {
		t.Fatalf("got %v, %v", got, err)
	}
	custom.Disabled = true
	if err := config.Save(a.cfgPath, custom); err != nil {
		t.Fatal(err)
	}
	if got, _, err := a.want(); err != nil || len(got) != 0 {
		t.Fatalf("disabled: got %v, %v", got, err)
	}
}

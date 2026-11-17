//go:build darwin || linux

package api

import (
	"encoding/json"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/aliraghebi/splitroute/internal/config"
)

// noBackend fails every call; "presets" must not need the daemon.
type noBackend struct{}

func (noBackend) Status() (Status, error)            { panic("unexpected") }
func (noBackend) LoadConfig() (config.Config, error) { panic("unexpected") }
func (noBackend) UpdateConfig(func(config.Config) (config.Config, error)) (config.Config, error) {
	panic("unexpected")
}
func (noBackend) AvailableLists() ([]string, error) { panic("unexpected") }

func TestHandleSocket(t *testing.T) {
	srv, cli := net.Pipe()
	go Handle(srv, noBackend{})
	if _, err := cli.Write([]byte(`{"cmd":"presets"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	var r Response
	if err := json.NewDecoder(cli).Decode(&r); err != nil || !r.OK || len(r.Presets) != len(config.Presets) {
		t.Fatalf("got %+v, %v", r, err)
	}
}

// memBackend keeps the config in memory, applying updates like the store
// (f, then Validate).
type memBackend struct {
	noBackend
	cfg config.Config
}

func (m *memBackend) LoadConfig() (config.Config, error) { return m.cfg, nil }
func (m *memBackend) UpdateConfig(f func(config.Config) (config.Config, error)) (config.Config, error) {
	c, err := f(m.cfg)
	if err == nil {
		err = c.Validate()
	}
	if err != nil {
		return config.Config{}, err
	}
	m.cfg = c
	return c, nil
}

func TestListEditCommands(t *testing.T) {
	m := &memBackend{cfg: config.Default()}
	pos := 0
	steps := []struct {
		req  Request
		ok   bool
		want string // list names and actions after
	}{
		{Request{Cmd: "list_create", Name: "Work", Action: "block"}, true, "ir-cidr:direct Work:block"},
		{Request{Cmd: "list_create", Name: "Work", Action: "block"}, false, ""},
		{Request{Cmd: "list_create", Action: "block"}, false, ""},
		{Request{Cmd: "list_enable", Name: "telegram", Action: "vpn"}, true, "ir-cidr:direct Work:block telegram:vpn"},
		{Request{Cmd: "list_set_action", Name: "Work", Action: "direct"}, true, "ir-cidr:direct Work:direct telegram:vpn"},
		{Request{Cmd: "list_set_action", Name: "Work", Action: "nope"}, false, ""},
		{Request{Cmd: "list_move", Name: "telegram"}, false, ""},
		{Request{Cmd: "list_move", Name: "telegram", Position: &pos}, true, "telegram:vpn ir-cidr:direct Work:direct"},
		{Request{Cmd: "list_rename", Name: "Work", NewName: "Office"}, true, "telegram:vpn ir-cidr:direct Office:direct"},
		{Request{Cmd: "list_remove", Name: "ir-cidr"}, true, "telegram:vpn Office:direct"},
		{Request{Cmd: "list_remove", Name: "ir-cidr"}, false, ""},
	}
	for _, s := range steps {
		r := Respond(m, s.req)
		if r.OK != s.ok {
			t.Fatalf("%+v: ok=%v error=%q", s.req, r.OK, r.Error)
		}
		if !s.ok {
			continue
		}
		var got []string
		for _, l := range r.Config.Lists {
			got = append(got, l.Name+":"+string(l.Action))
		}
		if strings.Join(got, " ") != s.want {
			t.Fatalf("%+v: got %v, want %s", s.req, got, s.want)
		}
	}

	r := Respond(m, Request{Cmd: "entries_add", Name: "Office", Entries: []string{"example.com", "Example.com", "AS13335"}})
	if !r.OK || r.Edit == nil || r.Edit.Added != 2 || r.Edit.Duplicates != 1 {
		t.Fatalf("entries_add: %+v %+v", r, r.Edit)
	}
	r = Respond(m, Request{Cmd: "entries_remove", Name: "Office", Entries: []string{"example.com", "example.net"}})
	if !r.OK || r.Edit.Removed != 1 || len(r.Edit.NotFound) != 1 {
		t.Fatalf("entries_remove: %+v %+v", r, r.Edit)
	}
	if got := m.cfg.Lists[1].Entries; len(got) != 1 || got[0] != "AS13335" {
		t.Fatalf("stored entries %q", got)
	}
	// A failed edit stores nothing and reports no counts.
	before := m.cfg
	if r := Respond(m, Request{Cmd: "entries_add", Name: "Office", Entries: []string{"10.0.0.1"}}); r.OK || r.Edit != nil {
		t.Fatalf("invalid entry: %+v", r)
	}
	if !reflect.DeepEqual(m.cfg, before) {
		t.Fatal("failed edit changed the config")
	}
}

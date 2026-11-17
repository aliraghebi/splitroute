package config

import (
	"reflect"
	"testing"

	"github.com/aliraghebi/splitroute/internal/rules"
)

func TestParseConfigLegacy(t *testing.T) {
	cfg, err := Parse([]byte("# comment\nir-cidr = direct\nmalware=block # inline\n\nbase_url = https://example.com/dl/\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{BaseURL: "https://example.com/dl/", Lists: []List{{Name: "ir-cidr", Action: rules.Direct}, {Name: "malware", Action: rules.Block}}}
	if !reflect.DeepEqual(cfg, want) || cfg.ListsURL() != "https://example.com/dl" {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
	for _, bad := range []string{"ir-cidr = proxy", "ir-cidr", "Bad_Name = direct", "a = direct\na = block", "base_url = http://x"} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestParseConfigJSON(t *testing.T) {
	cfg, err := Parse([]byte(`{"disabled": true, "lists": [
		{"name": "ir-cidr", "action": "direct"},
		{"name": "My servers", "custom": true, "action": "block", "entries": ["203.0.113.7", "198.51.100.0/24"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Disabled || len(cfg.Lists) != 2 || !cfg.Lists[1].Custom || cfg.ListsURL() != DefaultBaseURL {
		t.Fatalf("got %+v", cfg)
	}
	for _, bad := range []string{
		`{"lists": [{"name": "Upper", "action": "direct"}]}`,                           // release names are lowercase
		`{"lists": [{"name": "ir-cidr", "action": "direct", "entries": ["1.2.3.4"]}]}`, // entries on a release list
		`{"lists": [{"name": " x", "custom": true, "action": "direct"}]}`,              // padded name
		`{"lists": [{"name": "x", "custom": true, "action": "direct"}, {"name": "x", "custom": true, "action": "block"}]}`,
		`{"lists": [{"name": "x", "custom": true, "action": "direct", "entries": ["10.1.2.3"]}]}`, // reserved
		`{"lists": [}`,
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s: want error", bad)
		}
	}
}

func TestWithPreset(t *testing.T) {
	custom := List{Name: "Work", Custom: true, Action: rules.Direct, Entries: []string{"203.0.113.0/24"}}
	cfg := Config{Disabled: true, Lists: []List{{Name: "telegram", Action: rules.Direct}, custom}}
	got, err := WithPreset(cfg, "security")
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Disabled: true, Lists: []List{{Name: "malware", Action: rules.Block}, {Name: "phishing", Action: rules.Block}, custom}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	if _, err := WithPreset(cfg, "nope"); err == nil {
		t.Error("unknown preset: want error")
	}
	for _, p := range Presets {
		if err := (Config{Lists: p.Lists}).Validate(); err != nil {
			t.Errorf("preset %s: %v", p.ID, err)
		}
	}
}

package rules

import (
	"net/netip"
	"reflect"
	"testing"
)

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func TestParseEntries(t *testing.T) {
	got, err := ParseEntries([]string{" 203.0.113.7 ", "", "# note", "198.51.100.9/24"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []netip.Prefix{pfx("203.0.113.7/32"), pfx("198.51.100.0/24")}) {
		t.Fatalf("got %v", got)
	}
	for _, bad := range []string{"example.com", "2001:db8::1", "1.0.0.0/7", "192.168.1.1", "100.64.0.0/16"} {
		if _, err := ParseEntries([]string{bad}); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestPlanBlockWins(t *testing.T) {
	uses := []Use{{"ir", Direct}, {"bad", Block}, {"tg", VPN}}
	got, err := Plan(uses, map[string][]netip.Prefix{
		"ir":  {pfx("5.0.0.0/16"), pfx("2.144.0.0/14")},
		"bad": {pfx("5.0.0.0/16")},
		"tg":  {pfx("91.108.4.0/22")},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{{pfx("2.144.0.0/14"), Direct, "ir"}, {pfx("5.0.0.0/16"), Block, "bad"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

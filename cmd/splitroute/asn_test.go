//go:build darwin || linux

package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aliraghebi/splitroute/internal/config"
	"github.com/aliraghebi/splitroute/internal/lists"
	"github.com/aliraghebi/splitroute/internal/rules"
)

func TestWantASN(t *testing.T) {
	raw, err := os.ReadFile("../../internal/lists/testdata/asn.txt")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(raw)
	zw.Close()
	table := buf.Bytes()
	sum := sha256.Sum256(table)
	var down atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case down.Load():
			http.Error(w, "down", http.StatusBadGateway)
		case r.URL.Path == "/SHA256SUMS":
			w.Write([]byte(hex.EncodeToString(sum[:]) + "  asn.txt.gz\n"))
		case r.URL.Path == "/asn.txt.gz":
			w.Write(table)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := lists.Client
	lists.Client = srv.Client()
	defer func() { lists.Client = old }()

	cfg := config.Config{BaseURL: srv.URL, Lists: []config.List{
		{Name: "Sites", Custom: true, Action: rules.Direct, Entries: []string{"AS13335", "AS3", "example.org"}},
		{Name: "Iran", Custom: true, Action: rules.Direct, Entries: []string{"AS58224"}},
		{Name: "Work", Custom: true, Action: rules.Block, Entries: []string{"203.0.113.7"}},
	}}
	a := testApp(t, false, true)
	if err := config.Save(a.cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	count := func(rs []rules.Route) map[string]int {
		m := map[string]int{}
		for _, r := range rs {
			m[r.List]++
		}
		return m
	}

	got, warn, err := a.want()
	if err != nil {
		t.Fatal(err)
	}
	if c := count(got); c["Sites"] != 11 || c["Iran"] != 8 || c["Work"] != 1 {
		t.Fatalf("routes per list %v", c)
	}
	if len(warn) != 1 || !strings.Contains(warn[0], "Sites: AS3 announce no prefixes") {
		t.Fatalf("warnings %q", warn)
	}

	// Release down: the cached table keeps ASN routes.
	down.Store(true)
	got, warn, err = a.want()
	if err != nil || count(got)["Sites"] != 11 || !strings.Contains(strings.Join(warn, "\n"), "using cached copy") {
		t.Fatalf("cached: %v %q %v", count(got), warn, err)
	}

	// No table at all: AS entries are skipped with a warning, the rest is routed.
	os.RemoveAll(a.state.Dir + "/lists")
	got, warn, err = a.want()
	if err != nil || count(got)["Work"] != 1 || count(got)["Sites"] != 0 || !strings.Contains(strings.Join(warn, "\n"), "AS numbers not routed") {
		t.Fatalf("no table: %v %q %v", count(got), warn, err)
	}
	// ...and no misleading "AS3 announces nothing" while the table is missing.
	if strings.Contains(strings.Join(warn, "\n"), "announce no prefixes") {
		t.Fatalf("unknown-AS warning without a table: %q", warn)
	}
}

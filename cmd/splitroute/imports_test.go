//go:build darwin || linux

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aliraghebi/splitroute/internal/config"
	"github.com/aliraghebi/splitroute/internal/lists"
	"github.com/aliraghebi/splitroute/internal/rules"
)

func TestImportEndToEnd(t *testing.T) {
	var body atomic.Value
	body.Store("0.0.0.0 ads.example.com\n0.0.0.0 tracker.example.net\n198.51.100.0/24\n")
	var hits, notModified atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b := body.Load().(string)
		etag := `"` + itoa(len(b)) + `"`
		if r.Header.Get("If-None-Match") == etag {
			notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Write([]byte(b))
	}))
	defer srv.Close()

	a := testApp(t, false, true)
	a.httpClient = srv.Client()
	a.httpClient.CheckRedirect = lists.HTTPSOnly
	var out strings.Builder
	a.stdout = &out
	ctx := context.Background()

	if err := a.listCmd([]string{"remove", "ir-cidr"}); err != nil { // no release downloads in tests
		t.Fatal(err)
	}
	if err := a.listCmd([]string{"import", "Ads", srv.URL + "/hosts", "block"}); err != nil {
		t.Fatal(err)
	}
	if !a.importsDue() {
		t.Fatal("new import not due")
	}
	if _, warn, _ := a.want(); len(warn) == 0 || !strings.Contains(warn[0], "not downloaded yet") {
		t.Fatalf("before download: %q", warn)
	}
	if warn := a.refreshImports(ctx); len(warn) != 0 {
		t.Fatalf("download: %q", warn)
	}
	got, warn, err := a.want()
	if err != nil || len(warn) != 0 || len(got) != 1 || got[0].List != "Ads" || got[0].Action != rules.Block || got[0].Prefix.String() != "198.51.100.0/24" {
		t.Fatalf("routes %v, %q, %v", got, warn, err)
	}
	if a.importsDue() {
		t.Fatal("due right after a download")
	}
	out.Reset()
	if err := a.listCmd([]string{"show", "Ads"}); err != nil || out.String() != "198.51.100.0/24\nfull:ads.example.com\nfull:tracker.example.net\n" {
		t.Fatalf("show: %q, %v", out.String(), err)
	}
	if err := a.listCmd([]string{"add", "Ads", "example.org"}); err == nil {
		t.Fatal("added entries to an import")
	}

	// Refresh with unchanged content: 304, nothing rewritten.
	if err := a.listCmd([]string{"refresh", "Ads"}); err != nil {
		t.Fatal(err)
	}
	if warn := a.refreshImports(ctx); len(warn) != 0 || notModified.Load() != 1 {
		t.Fatalf("304: %q, %d", warn, notModified.Load())
	}

	// A captive portal page: refused, last good copy kept, error in status.
	body.Store("<html>\n<body>Please sign in</body>\n</html>\n")
	if err := a.RefreshImport("Ads"); err != nil {
		t.Fatal(err)
	}
	if warn := a.refreshImports(ctx); len(warn) != 1 || !strings.Contains(warn[0], "not a list") {
		t.Fatalf("portal page: %q", warn)
	}
	if got, warn, _ := a.want(); len(got) != 1 || len(warn) != 1 || !strings.Contains(warn[0], "using the copy from") {
		t.Fatalf("after failure: %v, %q", got, warn)
	}
	st, err := a.Status()
	if err != nil || len(st.Imports) != 1 || st.Imports[0].Entries != 3 || st.Imports[0].Error == "" || st.Imports[0].FetchedAt.IsZero() {
		t.Fatalf("status %+v, %v", st.Imports, err)
	}
	out.Reset()
	if err := a.listCmd(nil); err != nil || !strings.Contains(out.String(), "import: 3 entries from") || !strings.Contains(out.String(), "last download failed") {
		t.Fatalf("list: %q, %v", out.String(), err)
	}

	// New content replaces the entries; presets keep imports.
	body.Store("203.0.113.0/24\n")
	a.RefreshImport("Ads")
	if warn := a.refreshImports(ctx); len(warn) != 0 {
		t.Fatalf("new content: %q", warn)
	}
	if got, _, _ := a.want(); len(got) != 1 || got[0].Prefix.String() != "203.0.113.0/24" {
		t.Fatalf("after new content: %v", got)
	}
	cfg, err := a.UpdateConfig(func(c config.Config) (config.Config, error) { return config.WithPreset(c, "security") })
	if err != nil || cfg.Lists[len(cfg.Lists)-1].Import == nil {
		t.Fatalf("preset dropped the import: %+v, %v", cfg, err)
	}
	if hits.Load() != 4 {
		t.Errorf("%d requests, want 4", hits.Load())
	}

	// Dry-run and non-root never download.
	ro := testApp(t, true, false)
	ro.state.Dir, ro.cfgPath, ro.httpClient = a.state.Dir, a.cfgPath, a.httpClient
	a.RefreshImport("Ads")
	if warn := ro.refreshImports(ctx); warn != nil || hits.Load() != 4 {
		t.Fatalf("dry-run downloaded: %q, %d", warn, hits.Load())
	}
}

func itoa(i int) string {
	s := ""
	for {
		s = string(rune('0'+i%10)) + s
		if i /= 10; i == 0 {
			return s
		}
	}
}

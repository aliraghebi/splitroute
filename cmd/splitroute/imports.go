//go:build darwin || linux

// Downloading imports.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/aliraghebi/splitroute/internal/lists"
	"github.com/aliraghebi/splitroute/internal/store"
)

// refreshImports downloads every import that is due and returns warnings for
// the ones that failed. A failure keeps the import's last good entries. Only
// processes that may write the settings download; dry-run never does.
func (a *app) refreshImports(ctx context.Context) []string {
	if !a.canWrite {
		return nil
	}
	s, err := a.settings()
	if err != nil || s == nil {
		return nil // reported by the config read that follows
	}
	sts, err := s.Imports()
	if err != nil {
		return []string{fmt.Sprintf("imports: %v", err)}
	}
	var warn []string
	now := time.Now()
	for _, st := range sts {
		if !st.Due(now) {
			continue
		}
		if err := a.downloadImport(ctx, s, st); err != nil {
			if errors.Is(err, store.ErrImportChanged) {
				continue // removed or re-pointed meanwhile; the next pass handles it
			}
			warn = append(warn, fmt.Sprintf("import %s: %v (keeping the last good copy)", st.Name, err))
		}
	}
	return warn
}

func (a *app) downloadImport(ctx context.Context, s *store.Store, st store.ImportState) error {
	f, err := lists.FetchImport(ctx, a.httpClient, st.URL, st.ETag, st.LastModified)
	if err == nil && !f.NotModified {
		sum := sha256.Sum256(f.Body)
		hash := hex.EncodeToString(sum[:])
		if hash == st.SHA256 && st.Entries > 0 {
			f.NotModified = true // same bytes; skip parsing and rewriting
		} else {
			var d store.Download
			var ps lists.Stats
			d.Entries, ps, err = lists.ParseText(f.Body)
			if err == nil {
				n := len(d.Entries.V4) + len(d.Entries.V6) + len(d.Entries.Domains) + len(d.Entries.Suffixes) + len(d.Entries.ASNs)
				if err = lists.CheckImport(n, ps); err == nil {
					d.ETag, d.LastModified, d.SHA256 = f.ETag, f.LastModified, hash
					d.Skipped = ps.Reserved + ps.Invalid + ps.Other
					if err = s.SaveDownload(st.Name, st.URL, d); err == nil {
						log.Printf("import %s: %d entries (%d lines skipped)", st.Name, n, d.Skipped)
						return nil
					}
					if errors.Is(err, store.ErrImportChanged) {
						return err
					}
				}
			}
		}
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if merr := s.MarkChecked(st.Name, st.URL, f.ETag, f.LastModified, msg); merr != nil && err == nil {
		err = merr
	}
	return err
}

// importsDue reports whether the service should run a refresh pass now.
func (a *app) importsDue() bool {
	if !a.canWrite {
		return false
	}
	s, err := a.settings()
	if err != nil || s == nil {
		return false
	}
	due, err := s.ImportsDue(time.Now())
	if err != nil {
		log.Printf("warning: imports: %v", err)
	}
	return due
}

// RefreshImport asks for an import to be downloaded on the next pass, and
// wakes the service if this process is it.
func (a *app) RefreshImport(name string) error {
	if !a.canWrite {
		return errors.New("refreshing an import needs root: run it with sudo")
	}
	s, err := a.settings()
	if err != nil {
		return err
	}
	if err := s.ForceImport(name); err != nil {
		return err
	}
	a.wake()
	return nil
}

func (a *app) wake() {
	if a.reload != nil {
		select {
		case a.reload <- struct{}{}:
		default: // a reload is already pending
		}
	}
}

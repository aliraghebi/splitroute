//go:build darwin || linux

package api

import (
	"slices"
	"strings"
	"time"

	"github.com/aliraghebi/splitroute/internal/platform"
	"github.com/aliraghebi/splitroute/internal/rules"
)

// Status is what `status` prints and the control socket returns.
type Status struct {
	Version      string            `json:"version"`
	Enabled      bool              `json:"enabled"`
	Gateway      *platform.Gateway `json:"gateway,omitempty"`
	GatewayError string            `json:"gateway_error,omitempty"`
	VPN          string            `json:"vpn,omitempty"`
	Routes       int               `json:"routes"`
	Lists        []ListCount       `json:"lists"`
	Fetched      time.Time         `json:"fetched"`
	Missing      bool              `json:"missing"` // saved routes are gone from the routing table
	Warnings     []string          `json:"warnings,omitempty"`
	Error        string            `json:"error,omitempty"` // service only: the last apply failed
	Imports      []ImportStatus    `json:"imports,omitempty"`
}

// ImportStatus is an import's download state.
type ImportStatus struct {
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Entries   int       `json:"entries"`
	Skipped   int       `json:"skipped,omitempty"` // lines in the last download that were skipped
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	CheckedAt time.Time `json:"checked_at,omitzero"`
	Error     string    `json:"error,omitempty"` // last attempt failed; the entries are from FetchedAt
}

// ListCount is how many routes a list contributed.
type ListCount struct {
	Name   string       `json:"name"`
	Action rules.Action `json:"action"`
	Routes int          `json:"routes"`
}

// CountByList groups routes by list, sorted by list name.
func CountByList(routes []rules.Route) []ListCount {
	idx := map[string]int{}
	out := []ListCount{}
	for _, r := range routes {
		i, ok := idx[r.List]
		if !ok {
			i = len(out)
			idx[r.List] = i
			out = append(out, ListCount{Name: r.List, Action: r.Action})
		}
		out[i].Routes++
	}
	slices.SortFunc(out, func(x, y ListCount) int { return strings.Compare(x.Name, y.Name) })
	return out
}

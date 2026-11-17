//go:build darwin || linux

package api

import (
	"encoding/json"
	"net"
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

package main

import (
	"encoding/hex"
	"os"
	"testing"
	"time"
)

// A real RTM_ADD for a network route, read from a PF_ROUTE socket (same as
// internal/platform's fixture).
const netRouteAdd = "84000501000000004308000007000000561e00000100000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001002000002390300000000000000000010020000c0a865fe000000000000000007020000ffffff00"

func TestNetEvents(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ch := netEvents(r)
	b, _ := hex.DecodeString(netRouteAdd)
	w.Write(b)
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("no signal after a route message")
	}
}

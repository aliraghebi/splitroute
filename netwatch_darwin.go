package main

import (
	"os"
	"syscall"
)

// routeSocket opens a PF_ROUTE socket: the kernel writes a message to it on
// every route, address or interface change.
func routeSocket() (*os.File, error) {
	fd, err := syscall.Socket(syscall.AF_ROUTE, syscall.SOCK_RAW, syscall.AF_UNSPEC)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "route"), nil
}

// relevant reports whether a route socket read holds a change that can move the
// gateway or remove our routes: interface or address changes, and network
// route add/delete/change. Host and cloned routes (ARP, per-connection cache)
// and RTM_GET replies are constant noise.
func relevant(b []byte) bool {
	msgs, err := syscall.ParseRoutingMessage(b)
	if err != nil {
		return true
	}
	for _, m := range msgs {
		switch m := m.(type) {
		case *syscall.InterfaceMessage, *syscall.InterfaceAddrMessage:
			return true
		case *syscall.RouteMessage:
			h := m.Header
			switch h.Type {
			case syscall.RTM_ADD, syscall.RTM_DELETE, syscall.RTM_CHANGE:
				if h.Flags&(syscall.RTF_HOST|syscall.RTF_LLINFO|syscall.RTF_WASCLONED) == 0 {
					return true
				}
			}
		}
	}
	return false
}

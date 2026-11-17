package platform

import (
	"os"
	"syscall"
)

// rtnetlink multicast groups (linux/rtnetlink.h); not in package syscall.
const (
	rtmgrpLink       = 0x1
	rtmgrpIPv4IfAddr = 0x10
	rtmgrpIPv4Route  = 0x40
)

// RouteSocket opens an rtnetlink socket subscribed to link, IPv4 address and
// IPv4 route changes.
func RouteSocket() (*os.File, error) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW, syscall.NETLINK_ROUTE)
	if err != nil {
		return nil, err
	}
	sa := &syscall.SockaddrNetlink{
		Family: syscall.AF_NETLINK,
		Groups: rtmgrpLink | rtmgrpIPv4IfAddr | rtmgrpIPv4Route,
	}
	if err := syscall.Bind(fd, sa); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), "netlink"), nil
}

// Relevant: the subscribed groups carry no neighbor (ARP) traffic, so every
// message is a link, address or route change.
func Relevant([]byte) bool { return true }

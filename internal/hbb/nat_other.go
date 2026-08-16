//go:build !linux

package hbb

import (
	"errors"
	"net"
	"net/netip"
	"time"
)

var errWouldBlock = errors.New("connect would block")

// dialPunchLinux is unsupported on non-Linux platforms; dialPunch falls back
// to a plain dial.
func dialPunchLinux(local, peer netip.AddrPort, timeout time.Duration) (net.Conn, error) {
	return nil, errors.New("hole punch unsupported on this platform")
}

// socketBind on non-Linux platforms is a no-op stub; dialPunch falls back to a
// plain dial, so these are never used.
func socketBind(local netip.AddrPort) (int, error) {
	return -1, errors.New("hole punch unsupported on this platform")
}

func socketConnect(fd int, peer netip.AddrPort) error {
	return errors.New("hole punch unsupported on this platform")
}

func closeFD(fd int) {}

func socketToConn(fd int, local, peer netip.AddrPort) (net.Conn, error) {
	return nil, errors.New("hole punch unsupported on this platform")
}

var _ = time.Second

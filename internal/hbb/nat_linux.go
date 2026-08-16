//go:build linux

package hbb

import (
	"errors"
	"net"
	"net/netip"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

var errWouldBlock = errors.New("connect would block")

func socketBind(local netip.AddrPort) (int, error) {
	addr := local.Addr()
	ip := addr.As16()
	var domain int
	var sa syscall.Sockaddr
	if addr.Is4() {
		domain = syscall.AF_INET
		var a4 [4]byte
		copy(a4[:], ip[12:16])
		sa = &syscall.SockaddrInet4{Addr: a4, Port: int(local.Port())}
	} else {
		domain = syscall.AF_INET6
		var a16 [16]byte
		copy(a16[:], ip[:])
		sa = &syscall.SockaddrInet6{Addr: a16, Port: int(local.Port())}
	}

	fd, err := syscall.Socket(domain, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK, 0)
	if err != nil {
		return -1, err
	}
	_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
	if err := syscall.Bind(fd, sa); err != nil {
		syscall.Close(fd)
		return -1, err
	}
	return fd, nil
}

// dialPunchLinux binds the local address with SO_REUSEADDR (reusing the
// rendezvous socket port) and performs a TCP simultaneous-open connect.
func dialPunchLinux(local, peer netip.AddrPort, timeout time.Duration) (net.Conn, error) {
	fd, err := socketBind(local)
	if err != nil {
		return nil, err
	}

	ip := peer.Addr().As16()
	var sa syscall.Sockaddr
	if peer.Addr().Is4() {
		var a4 [4]byte
		copy(a4[:], ip[12:16])
		sa = &syscall.SockaddrInet4{Addr: a4, Port: int(peer.Port())}
	} else {
		var a16 [16]byte
		copy(a16[:], ip[:])
		sa = &syscall.SockaddrInet6{Addr: a16, Port: int(peer.Port())}
	}

	deadline := time.Now().Add(timeout)
	err = syscall.Connect(fd, sa)
	inProgress := err == syscall.EINPROGRESS || err == syscall.EAGAIN || err == syscall.EALREADY
	if err != nil && !inProgress {
		syscall.Close(fd)
		return nil, err
	}

	if inProgress {
		for {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				syscall.Close(fd)
				return nil, errors.New("hole punch connect timeout")
			}
			ms := int(remaining / time.Millisecond)
			if ms < 1 {
				ms = 1
			}
			_, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}, ms)
			if err != nil && err != unix.EINTR {
				syscall.Close(fd)
				return nil, err
			}
			if err == nil {
				break
			}
		}
		soErr, err := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_ERROR)
		if err != nil {
			syscall.Close(fd)
			return nil, err
		}
		if soErr != 0 {
			syscall.Close(fd)
			return nil, syscall.Errno(soErr)
		}
	}

	_ = syscall.SetNonblock(fd, false)
	conn := &syscallConn{fd: fd, local: local, peer: peer}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

type syscallConn struct {
	fd    int
	local netip.AddrPort
	peer  netip.AddrPort
}

func (c *syscallConn) Read(b []byte) (int, error)         { return syscall.Read(c.fd, b) }
func (c *syscallConn) Write(b []byte) (int, error)        { return syscall.Write(c.fd, b) }
func (c *syscallConn) Close() error                       { return syscall.Close(c.fd) }
func (c *syscallConn) LocalAddr() net.Addr                { return tcpAddr(c.local) }
func (c *syscallConn) RemoteAddr() net.Addr               { return tcpAddr(c.peer) }
func (c *syscallConn) SetDeadline(t time.Time) error      { return setDeadline(c.fd, t) }
func (c *syscallConn) SetReadDeadline(t time.Time) error  { return setDeadline(c.fd, t) }
func (c *syscallConn) SetWriteDeadline(t time.Time) error { return setDeadline(c.fd, t) }

func tcpAddr(a netip.AddrPort) *net.TCPAddr {
	return &net.TCPAddr{IP: a.Addr().AsSlice(), Port: int(a.Port())}
}

func setDeadline(fd int, t time.Time) error {
	var tv syscall.Timeval
	if !t.IsZero() {
		d := time.Until(t)
		if d <= 0 {
			tv = syscall.Timeval{}
		} else {
			sec := d / time.Second
			usec := (d - sec*time.Second) / time.Microsecond
			tv.Sec = int64(sec)
			tv.Usec = int64(usec)
		}
	} else {
		tv.Sec = 0
		tv.Usec = 0
	}
	_, _, errno := syscall.Syscall6(
		syscall.SYS_SETSOCKOPT,
		uintptr(fd),
		uintptr(syscall.SOL_SOCKET),
		uintptr(syscall.SO_RCVTIMEO),
		uintptr(unsafe.Pointer(&tv)),
		unsafe.Sizeof(tv),
		0,
	)
	if errno != 0 {
		return errno
	}
	_, _, errno = syscall.Syscall6(
		syscall.SYS_SETSOCKOPT,
		uintptr(fd),
		uintptr(syscall.SOL_SOCKET),
		uintptr(syscall.SO_SNDTIMEO),
		uintptr(unsafe.Pointer(&tv)),
		unsafe.Sizeof(tv),
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

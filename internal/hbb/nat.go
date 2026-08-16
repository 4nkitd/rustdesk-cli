//go:build !windows

package hbb

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"syscall"
	"time"

	"google.golang.org/protobuf/proto"

	pb "rdcli/internal/hbb/proto"
)

// dialPunch attempts a TCP simultaneous-open hole punch toward the peer from
// the same local address used for the rendezvous connection. It falls back to
// a plain dial (any local address) if the local bind fails.
func dialPunch(local, peer netip.AddrPort, timeout time.Duration) (net.Conn, error) {
	if local.IsValid() && local.Port() > 0 {
		conn, err := dialPunchLinux(local, peer, timeout)
		if err == nil {
			return conn, nil
		}
	}
	d := net.Dialer{Timeout: timeout}
	return d.Dial("tcp", peer.String())
}

// TestNATType determines the local NAT behaviour the same way the reference
// client does: connect to the rendezvous TCP port, then to port-1 reusing the
// same local address, and compare the server-observed source ports. Equal
// ports mean an asymmetric (cone) NAT; different ports mean symmetric.
func TestNATType(server string, timeout time.Duration) (pb.NatType, error) {
	addr := normalizeServerAddr(server)
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return pb.NatType_UNKNOWN_NAT, fmt.Errorf("hbb: invalid server %s: %w", server, err)
	}
	p, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || p == 0 {
		return pb.NatType_UNKNOWN_NAT, fmt.Errorf("hbb: invalid server port %q", portStr)
	}
	server2 := net.JoinHostPort(host, strconv.FormatUint(p-1, 10))

	req, err := proto.Marshal(&pb.RendezvousMessage{
		Union: &pb.RendezvousMessage_TestNatRequest{TestNatRequest: &pb.TestNatRequest{}},
	})
	if err != nil {
		return pb.NatType_UNKNOWN_NAT, fmt.Errorf("hbb: marshal nat request: %w", err)
	}

	port1, port2 := int32(0), int32(0)
	var local *net.TCPAddr
	for i := 0; i < 2; i++ {
		target := addr
		if i == 1 {
			target = server2
		}
		d := net.Dialer{Timeout: timeout, LocalAddr: local, Control: reuseControl}
		if timeout > 0 {
			d.Deadline = time.Now().Add(timeout)
		}
		conn, err := d.Dial("tcp", target)
		if err != nil {
			return pb.NatType_UNKNOWN_NAT, fmt.Errorf("hbb: nat test connect %s: %w", target, err)
		}
		conn.SetDeadline(time.Now().Add(timeout))
		if i == 0 {
			local = conn.LocalAddr().(*net.TCPAddr)
		}
		if _, err := conn.Write(Encode(req)); err != nil {
			conn.Close()
			return pb.NatType_UNKNOWN_NAT, fmt.Errorf("hbb: nat test request: %w", err)
		}
		resp, err := readRendezvousMessage(conn)
		conn.Close()
		if err != nil {
			return pb.NatType_UNKNOWN_NAT, fmt.Errorf("hbb: nat test response: %w", err)
		}
		tnr := resp.GetTestNatResponse()
		if tnr == nil {
			return pb.NatType_UNKNOWN_NAT, fmt.Errorf("hbb: unexpected nat test response %T", resp.GetUnion())
		}
		if i == 0 {
			port1 = tnr.GetPort()
		} else {
			port2 = tnr.GetPort()
		}
	}
	if port1 <= 0 || port2 <= 0 {
		return pb.NatType_UNKNOWN_NAT, fmt.Errorf("hbb: invalid nat test ports %d/%d", port1, port2)
	}
	if port1 == port2 {
		return pb.NatType_ASYMMETRIC, nil
	}
	return pb.NatType_SYMMETRIC, nil
}

func reuseControl(network, address string, c syscall.RawConn) error {
	var setErr error
	err := c.Control(func(fd uintptr) {
		setErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	})
	if err != nil {
		return err
	}
	return setErr
}

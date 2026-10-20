package hbb

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func dialPunch(local, peer netip.AddrPort, timeout time.Duration) (net.Conn, error) {
	d := net.Dialer{Timeout: timeout, Control: reuseControl}
	if local.IsValid() && local.Port() > 0 && local.Addr().Unmap().Is4() == peer.Addr().Unmap().Is4() {
		d.LocalAddr = net.TCPAddrFromAddrPort(local)
	}
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
		// Keep the source port reserved until both probes finish.
		defer conn.Close()
		if timeout > 0 {
			_ = conn.SetDeadline(time.Now().Add(timeout))
		}
		if i == 0 {
			local = conn.LocalAddr().(*net.TCPAddr)
		}
		if _, err := writeFull(conn, Encode(req)); err != nil {
			return pb.NatType_UNKNOWN_NAT, fmt.Errorf("hbb: nat test request: %w", err)
		}
		resp, err := readRendezvousMessage(conn)
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

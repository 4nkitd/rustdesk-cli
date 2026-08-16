package hbb

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"time"

	"google.golang.org/protobuf/proto"

	pb "rdcli/internal/hbb/proto"
)

const DefaultRendezvousServer = "rs-ny.rustdesk.com:21116"

func normalizeServerAddr(server string) string {
	if server == "" {
		return DefaultRendezvousServer
	}
	if _, _, err := net.SplitHostPort(server); err != nil {
		return net.JoinHostPort(server, "21116")
	}
	return server
}

func readRendezvousMessage(conn net.Conn) (*pb.RendezvousMessage, error) {
	var readBuf []byte
	tmp := make([]byte, 4096)
	for {
		frame, ok := DecodeFrame(&readBuf)
		if ok {
			msg := &pb.RendezvousMessage{}
			if err := proto.Unmarshal(frame, msg); err != nil {
				return nil, fmt.Errorf("hbb: unmarshal rendezvous message: %w", err)
			}
			return msg, nil
		}
		n, err := conn.Read(tmp)
		if n > 0 {
			readBuf = append(readBuf, tmp[:n]...)
		}
		if err != nil {
			return nil, err
		}
	}
}

func writeRendezvousMessage(conn net.Conn, msg *pb.RendezvousMessage) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("hbb: marshal rendezvous message: %w", err)
	}
	framed := Encode(data)
	_, err = conn.Write(framed)
	return err
}

func PunchHole(
	server string,
	peerID string,
	password string,
	key string,
	forceRelay bool,
	connType pb.ConnType,
	timeout time.Duration,
) (peerAddr netip.AddrPort, pk []byte, relayServer string, natType pb.NatType, isLocal bool, err error) {
	addr := normalizeServerAddr(server)

	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return peerAddr, nil, "", pb.NatType_UNKNOWN_NAT, false, err
	}
	defer conn.Close()

	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}

	req := &pb.RendezvousMessage{
		Union: &pb.RendezvousMessage_PunchHoleRequest{
			PunchHoleRequest: &pb.PunchHoleRequest{
				Id:         peerID,
				NatType:    pb.NatType_UNKNOWN_NAT,
				LicenceKey: key,
				ConnType:   connType,
				ForceRelay: forceRelay,
			},
		},
	}

	if err := writeRendezvousMessage(conn, req); err != nil {
		return peerAddr, nil, "", pb.NatType_UNKNOWN_NAT, false, err
	}

	for {
		resp, err := readRendezvousMessage(conn)
		if err != nil {
			return peerAddr, nil, "", pb.NatType_UNKNOWN_NAT, false, err
		}

		phResp := resp.GetPunchHoleResponse()
		if phResp != nil {
			if len(phResp.GetSocketAddr()) > 0 {
				peerAddr = DecodeAddr(phResp.GetSocketAddr())
				pk = phResp.GetPk()
				relayServer = phResp.GetRelayServer()
				natType = phResp.GetNatType()
				isLocal = phResp.GetIsLocal()
				return peerAddr, pk, relayServer, natType, isLocal, nil
			}
			if phResp.GetOtherFailure() != "" {
				return peerAddr, nil, "", pb.NatType_UNKNOWN_NAT, false, errors.New(phResp.GetOtherFailure())
			}
			return peerAddr, nil, "", pb.NatType_UNKNOWN_NAT, false, fmt.Errorf("punch hole failed: %v", phResp.GetFailure())
		}
	}
}

// QueryOnline checks the online state of peers via the rendezvous server.
// It tries UDP on the rendezvous port first (classic hbbs behaviour), then
// falls back to TCP on port-1 (current hbbs OnlineRequest listener). Returns
// a per-id online map and the round-trip latency of the successful query.
func QueryOnline(server string, peerIDs []string, timeout time.Duration) (map[string]bool, time.Duration, error) {
	result := make(map[string]bool, len(peerIDs))
	for _, id := range peerIDs {
		result[id] = false
	}
	if len(peerIDs) == 0 {
		return result, 0, nil
	}

	host, port, err := net.SplitHostPort(normalizeServerAddr(server))
	if err != nil {
		return nil, 0, fmt.Errorf("hbb: invalid server %s: %w", server, err)
	}
	tcpPort := port
	if p, err := strconv.ParseUint(tcpPort, 10, 16); err == nil && p > 0 {
		tcpPort = strconv.FormatUint(p-1, 10)
	}
	tcpAddr := net.JoinHostPort(host, tcpPort)

	req := &pb.RendezvousMessage{
		Union: &pb.RendezvousMessage_OnlineRequest{
			OnlineRequest: &pb.OnlineRequest{Peers: peerIDs},
		},
	}
	data, err := proto.Marshal(req)
	if err != nil {
		return nil, 0, fmt.Errorf("hbb: marshal online request: %w", err)
	}
	// hbbs parses raw protobuf over UDP and BytesCodec frames over TCP.
	framed := Encode(data)

	apply := func(states []byte) {
		for i, id := range peerIDs {
			byteIdx := i / 8
			if byteIdx < len(states) {
				result[id] = states[byteIdx]&(0x01<<(7-i%8)) != 0
			}
		}
	}

	if states, latency, uErr := queryOnlineUDP(host, port, data, timeout); uErr == nil {
		apply(states)
		return result, latency, nil
	}

	states, latency, err := queryOnlineTCP(tcpAddr, framed, timeout)
	if err != nil {
		states, latency, err = queryOnlineTCP(normalizeServerAddr(server), framed, timeout)
		if err != nil {
			return nil, 0, err
		}
	}
	apply(states)
	return result, latency, nil
}

func queryOnlineUDP(host, port string, framed []byte, timeout time.Duration) ([]byte, time.Duration, error) {
	addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, 0, fmt.Errorf("hbb: resolve udp server: %w", err)
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return nil, 0, fmt.Errorf("hbb: udp online socket: %w", err)
	}
	defer conn.Close()
	// Legacy hbbs answered OnlineRequest over UDP; current hbbs only answers
	// on TCP port-1. Keep the UDP probe short so the TCP fallback is fast.
	udpTimeout := 700 * time.Millisecond
	if timeout > 0 && timeout < udpTimeout {
		udpTimeout = timeout
	}
	start := time.Now()
	for attempt := 0; attempt < 2; attempt++ {
		_ = conn.SetDeadline(time.Now().Add(udpTimeout))
		if _, err := conn.Write(framed); err != nil {
			return nil, 0, fmt.Errorf("hbb: udp online request: %w", err)
		}
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				break
			}
			msg := &pb.RendezvousMessage{}
			if err := proto.Unmarshal(buf[:n], msg); err != nil {
				continue
			}
			if ol := msg.GetOnlineResponse(); ol != nil {
				return ol.GetStates(), time.Since(start), nil
			}
		}
	}
	return nil, 0, errors.New("hbb: no online response over udp")
}

func queryOnlineTCP(addr string, framed []byte, timeout time.Duration) ([]byte, time.Duration, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, 0, fmt.Errorf("hbb: connect online server: %w", err)
	}
	defer conn.Close()
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	start := time.Now()
	if _, err := conn.Write(framed); err != nil {
		return nil, 0, fmt.Errorf("hbb: send online request: %w", err)
	}
	var readBuf []byte
	tmp := make([]byte, 4096)
	for {
		if frame, ok := DecodeFrame(&readBuf); ok {
			msg := &pb.RendezvousMessage{}
			if err := proto.Unmarshal(frame, msg); err != nil {
				return nil, 0, fmt.Errorf("hbb: unmarshal online response: %w", err)
			}
			if ol := msg.GetOnlineResponse(); ol != nil {
				return ol.GetStates(), time.Since(start), nil
			}
			continue
		}
		n, err := conn.Read(tmp)
		if n > 0 {
			readBuf = append(readBuf, tmp[:n]...)
		}
		if err != nil {
			return nil, 0, fmt.Errorf("hbb: read online response: %w", err)
		}
	}
}

func CheckOnline(server string, peerID string, timeout time.Duration) (bool, error) {
	res, _, err := QueryOnline(server, []string{peerID}, timeout)
	if err != nil {
		return false, err
	}
	return res[peerID], nil
}

package hbb

import (
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "rdcli/internal/hbb/proto"
)

func TestPunchHoleSuccess(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer listener.Close()

	targetAddrPort := netip.MustParseAddrPort("1.2.3.4:5678")
	encodedAddr := EncodeAddr(targetAddrPort)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		msg, err := readRendezvousMessage(conn)
		if err != nil {
			return
		}

		req := msg.GetPunchHoleRequest()
		if req == nil || req.Id != "test-peer" {
			return
		}

		resp := &pb.RendezvousMessage{
			Union: &pb.RendezvousMessage_PunchHoleResponse{
				PunchHoleResponse: &pb.PunchHoleResponse{
					SocketAddr:  encodedAddr,
					Pk:          []byte("peer-pub-key"),
					RelayServer: "relay.test:21117",
					Union: &pb.PunchHoleResponse_NatType{
						NatType: pb.NatType_ASYMMETRIC,
					},
				},
			},
		}
		_ = writeRendezvousMessage(conn, resp)
	}()

	peerAddr, pk, relay, natType, _, err := PunchHole(listener.Addr().String(), "test-peer", "", "key123", false, pb.ConnType_DEFAULT_CONN, 2*time.Second)
	if err != nil {
		t.Fatalf("PunchHole failed: %v", err)
	}
	if peerAddr != targetAddrPort {
		t.Fatalf("peerAddr got %v, want %v", peerAddr, targetAddrPort)
	}
	if string(pk) != "peer-pub-key" {
		t.Fatalf("pk got %s, want peer-pub-key", pk)
	}
	if relay != "relay.test:21117" {
		t.Fatalf("relay got %s, want relay.test:21117", relay)
	}
	if natType != pb.NatType_ASYMMETRIC {
		t.Fatalf("natType got %v, want ASYMMETRIC", natType)
	}
}

func TestCheckOnline(t *testing.T) {
	// hbbs layout: OnlineRequest is served on the TCP listener at port-1
	// relative to the rendezvous port (21115 vs 21116).
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("base listen failed: %v", err)
	}
	serverPort := base.Addr().(*net.TCPAddr).Port
	base.Close()

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", serverPort-1))
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer listener.Close()
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)

	go func() {
		for i := 0; i < 2; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			msg, err := readRendezvousMessage(conn)
			if err != nil {
				conn.Close()
				return
			}
			olReq := msg.GetOnlineRequest()
			if olReq == nil || len(olReq.Peers) == 0 {
				conn.Close()
				return
			}

			states := []byte{0}
			if olReq.Peers[0] == "online-peer" {
				states = []byte{0x80}
			}

			resp := &pb.RendezvousMessage{
				Union: &pb.RendezvousMessage_OnlineResponse{
					OnlineResponse: &pb.OnlineResponse{
						States: states,
					},
				},
			}
			_ = writeRendezvousMessage(conn, resp)
			conn.Close()
		}
	}()

	online, err := CheckOnline(serverAddr, "online-peer", 2*time.Second)
	if err != nil {
		t.Fatalf("CheckOnline online-peer failed: %v", err)
	}
	if !online {
		t.Fatalf("expected online true, got false")
	}

	offline, err := CheckOnline(serverAddr, "offline-peer", 2*time.Second)
	if err != nil {
		t.Fatalf("CheckOnline offline-peer failed: %v", err)
	}
	if offline {
		t.Fatalf("expected online false, got true")
	}
}

func TestCheckOnlineUDP(t *testing.T) {
	// Legacy hbbs answered OnlineRequest over UDP on the rendezvous port.
	udpLn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("udp listen failed: %v", err)
	}
	defer udpLn.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		n, addr, err := udpLn.ReadFrom(buf)
		if err != nil {
			return
		}
		msg := &pb.RendezvousMessage{}
		if err := proto.Unmarshal(buf[:n], msg); err != nil {
			return
		}
		olReq := msg.GetOnlineRequest()
		if olReq == nil || len(olReq.Peers) != 1 || olReq.Peers[0] != "udp-peer" {
			return
		}
		resp := &pb.RendezvousMessage{
			Union: &pb.RendezvousMessage_OnlineResponse{
				OnlineResponse: &pb.OnlineResponse{States: []byte{0x80}},
			},
		}
		data, _ := proto.Marshal(resp)
		_, _ = udpLn.WriteTo(data, addr)
	}()

	online, err := CheckOnline(udpLn.LocalAddr().String(), "udp-peer", 2*time.Second)
	if err != nil {
		t.Fatalf("CheckOnline udp failed: %v", err)
	}
	if !online {
		t.Fatalf("expected udp online true, got false")
	}
	<-done
}

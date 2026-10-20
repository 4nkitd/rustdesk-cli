package hbb

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

// runMockRelay implements a minimal hbbr: it accepts the client connection,
// then a "peer" connection carrying the same uuid, and pipes raw bytes between
// them (matching relay_server.rs make_pair_).
func runMockRelay(t *testing.T) (string, chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("relay listen: %v", err)
	}
	pairDone := make(chan struct{})
	go func() {
		defer close(pairDone)
		clientConn, err := ln.Accept()
		if err != nil {
			return
		}
		uuid := ""
		func() {
			defer clientConn.Close()
			msg, err := readRendezvousMessage(clientConn)
			if err != nil {
				return
			}
			rr := msg.GetRequestRelay()
			if rr == nil {
				return
			}
			uuid = rr.GetUuid()
			peerConn, err := ln.Accept()
			if err != nil {
				return
			}
			defer peerConn.Close()
			pmsg, err := readRendezvousMessage(peerConn)
			if err != nil {
				return
			}
			prr := pmsg.GetRequestRelay()
			if prr == nil || prr.GetUuid() != uuid {
				return
			}
			// both streams now raw; pipe
			go func() {
				io.Copy(peerConn, clientConn)
			}()
			io.Copy(clientConn, peerConn)
		}()
	}()
	return ln.Addr().String(), pairDone
}

func runMockRelayPeer(t *testing.T, peerEdPriv ed25519.PrivateKey, peerID, password, relayAddr, uuid string) {
	t.Helper()
	go func() {
		conn, err := net.Dial("tcp", relayAddr)
		if err != nil {
			return
		}
		defer conn.Close()
		req := &pb.RendezvousMessage{
			Union: &pb.RendezvousMessage_RequestRelay{
				RequestRelay: &pb.RequestRelay{Id: peerID, Uuid: uuid},
			},
		}
		if err := writeRendezvousMessage(conn, req); err != nil {
			return
		}
		// wait for the relay to pair the two connections so the SignedId is
		// not co-buffered with the RequestRelay by the relay's frame reader
		time.Sleep(100 * time.Millisecond)
		sc := NewSecureConn(conn)
		kp, _ := GenerateKeyPair()
		sid := mustSignIDPk(t, peerEdPriv, peerID, kp.Pub)
		data, _ := proto.Marshal(&pb.Message{Union: &pb.Message_SignedId{SignedId: &pb.SignedId{Id: sid}}})
		sc.Write(data)
		raw, err := sc.ReadMessage()
		if err != nil {
			return
		}
		in := &pb.Message{}
		if err := proto.Unmarshal(raw, in); err != nil {
			return
		}
		pk := in.GetPublicKey()
		if pk == nil {
			return
		}
		key, err := DecryptSymmetricKey(pk.GetSymmetricValue(), pk.GetAsymmetricValue(), kp.Priv)
		if err != nil {
			return
		}
		sc.SetKey(key)
		hash, _ := proto.Marshal(&pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Salt: "salt123", Challenge: "ch123"}}})
		sc.Write(hash)
		raw, err = sc.ReadMessage()
		if err != nil {
			return
		}
		lr := &pb.Message{}
		if err := proto.Unmarshal(raw, lr); err != nil {
			return
		}
		login := lr.GetLoginRequest()
		if login == nil || len(login.GetPassword()) == 0 {
			return
		}
		pi, _ := proto.Marshal(&pb.Message{Union: &pb.Message_LoginResponse{
			LoginResponse: &pb.LoginResponse{Union: &pb.LoginResponse_PeerInfo{PeerInfo: &pb.PeerInfo{Version: "1.4.1"}}},
		}})
		sc.Write(pi)
	}()
}

func TestConnectViaRelay(t *testing.T) {
	serverPub, serverPriv, _ := ed25519.GenerateKey(rand.Reader)
	peerPub, peerPriv, _ := ed25519.GenerateKey(rand.Reader)

	relayAddr, pairDone := runMockRelay(t)

	rvLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("rendezvous listen: %v", err)
	}
	go func() {
		for {
			conn, err := rvLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				msg, err := readRendezvousMessage(c)
				if err != nil {
					return
				}
				if msg.GetRequestRelay() != nil {
					signedPk := mustSignIDPk(t, serverPriv, "test-peer", peerPub)
					resp := &pb.RendezvousMessage{
						Union: &pb.RendezvousMessage_RelayResponse{
							RelayResponse: &pb.RelayResponse{RelayServer: relayAddr, Uuid: "relay-uuid-1", Union: &pb.RelayResponse_Pk{Pk: signedPk}},
						},
					}
					_ = writeRendezvousMessage(c, resp)
				}
				if ph := msg.GetPunchHoleRequest(); ph != nil {
					signedPk := mustSignIDPk(t, serverPriv, "test-peer", peerPub)
					resp := &pb.RendezvousMessage{
						Union: &pb.RendezvousMessage_RelayResponse{
							RelayResponse: &pb.RelayResponse{RelayServer: relayAddr, Uuid: "relay-uuid-1", Union: &pb.RelayResponse_Pk{Pk: signedPk}},
						},
					}
					_ = writeRendezvousMessage(c, resp)
				}
			}(conn)
		}
	}()

	runMockRelayPeer(t, peerPriv, "test-peer", "pw", relayAddr, "relay-uuid-1")

	c, err := Connect(DialOpts{
		Server:     rvLn.Addr().String(),
		PeerID:     "test-peer",
		Password:   "pw",
		Key:        base64.StdEncoding.EncodeToString(serverPub),
		ForceRelay: true,
		Timeout:    10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer c.Close()

	pi, err := c.Login(LoginTunnel, LoginSpec{Host: "localhost", Port: 80})
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	if pi.GetVersion() != "1.4.1" {
		t.Fatalf("peer version got %s", pi.GetVersion())
	}
	c.Close()
	select {
	case <-pairDone:
	case <-time.After(5 * time.Second):
		t.Fatalf("relay pairing did not complete")
	}
}

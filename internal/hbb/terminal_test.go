package hbb

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "rdcli/internal/hbb/proto"
)

func runMockTerminalPeer(t *testing.T, peerEdPriv ed25519.PrivateKey, peerID, password, version string) (*netip.AddrPort, chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("peer listen: %v", err)
	}
	addr := ln.Addr().(*net.TCPAddr).AddrPort()
	done := make(chan struct{})

	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		sc := NewSecureConn(conn)

		kp, err := GenerateKeyPair()
		if err != nil {
			return
		}
		sid := mustSignIDPk(t, peerEdPriv, peerID, kp.Pub)
		msg := &pb.Message{Union: &pb.Message_SignedId{SignedId: &pb.SignedId{Id: sid}}}
		data, _ := proto.Marshal(msg)
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

		hash := &pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Salt: "salt123", Challenge: "ch123"}}}
		data, _ = proto.Marshal(hash)
		sc.Write(data)

		raw, err = sc.ReadMessage()
		if err != nil {
			return
		}
		lr := &pb.Message{}
		if err := proto.Unmarshal(raw, lr); err != nil {
			return
		}

		pi := &pb.Message{Union: &pb.Message_LoginResponse{
			LoginResponse: &pb.LoginResponse{
				Union: &pb.LoginResponse_PeerInfo{
					PeerInfo: &pb.PeerInfo{Version: version, Platform: "Linux", Hostname: "mock"},
				},
			},
		}}
		data, _ = proto.Marshal(pi)
		sc.Write(data)

		// Wait for TerminalAction_Open
		raw, err = sc.ReadMessage()
		if err != nil {
			return
		}
		msg = &pb.Message{}
		if err := proto.Unmarshal(raw, msg); err != nil {
			return
		}

		ta := msg.GetTerminalAction()
		if ta == nil || ta.GetOpen() == nil {
			t.Errorf("expected OpenTerminal action")
			return
		}

		// Send TerminalResponse_Opened
		openedResp := &pb.Message{
			Union: &pb.Message_TerminalResponse{
				TerminalResponse: &pb.TerminalResponse{
					Union: &pb.TerminalResponse_Opened{
						Opened: &pb.TerminalOpened{
							TerminalId: 42,
							Success:    true,
						},
					},
				},
			},
		}
		data, _ = proto.Marshal(openedResp)
		sc.Write(data)

		// Loop to handle incoming TerminalActions (Data, Resize, Close)
		for {
			raw, err := sc.ReadMessage()
			if err != nil {
				return
			}
			msg := &pb.Message{}
			if err := proto.Unmarshal(raw, msg); err != nil {
				return
			}
			ta := msg.GetTerminalAction()
			if ta == nil {
				continue
			}

			switch u := ta.GetUnion().(type) {
			case *pb.TerminalAction_Data:
				// Echo data back wrapped in TerminalResponse_Data
				echo := &pb.Message{
					Union: &pb.Message_TerminalResponse{
						TerminalResponse: &pb.TerminalResponse{
							Union: &pb.TerminalResponse_Data{
								Data: &pb.TerminalData{
									TerminalId: u.Data.GetTerminalId(),
									Data:       u.Data.GetData(),
								},
							},
						},
					},
				}
				data, _ = proto.Marshal(echo)
				sc.Write(data)
			case *pb.TerminalAction_Resize:
				// Echo resize confirmation via data or just acknowledge
			case *pb.TerminalAction_Close:
				closed := &pb.Message{
					Union: &pb.Message_TerminalResponse{
						TerminalResponse: &pb.TerminalResponse{
							Union: &pb.TerminalResponse_Closed{
								Closed: &pb.TerminalClosed{
									TerminalId: u.Close.GetTerminalId(),
								},
							},
						},
					},
				}
				data, _ = proto.Marshal(closed)
				sc.Write(data)
				return
			}
		}
	}()

	return &addr, done
}

func TestTerminalSession(t *testing.T) {
	serverPub, serverPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen server key: %v", err)
	}
	peerPub, peerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen peer key: %v", err)
	}

	password := "secret"
	peerAddr, done := runMockTerminalPeer(t, peerPriv, "test-term-peer", password, "1.4.1")
	rvServer := runMockRendezvous(t, serverPriv, "test-term-peer", *peerAddr, peerPub)

	opts := DialOpts{
		Server:   rvServer,
		PeerID:   "test-term-peer",
		Password: password,
		Key:      base64.StdEncoding.EncodeToString(serverPub),
		Timeout:  5 * time.Second,
	}

	ts, err := OpenTerminal(opts, 24, 80)
	if err != nil {
		t.Fatalf("OpenTerminal failed: %v", err)
	}
	defer ts.Close()

	if ts.termID != 42 {
		t.Fatalf("expected termID 42, got %d", ts.termID)
	}

	// Test Write & Read echo
	input := []byte("hello terminal\n")
	n, err := ts.Write(input)
	if err != nil || n != len(input) {
		t.Fatalf("Write failed: n=%d err=%v", n, err)
	}

	buf := make([]byte, 64)
	n, err = ts.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if !bytes.Equal(buf[:n], input) {
		t.Fatalf("expected output %q, got %q", input, buf[:n])
	}

	// Test Resize
	if err := ts.Resize(40, 100); err != nil {
		t.Fatalf("Resize failed: %v", err)
	}

	// Test Close
	if err := ts.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Read after Close should return EOF
	_, err = ts.Read(buf)
	if err == nil || (err != io.EOF && err.Error() != "terminal closed") {
		// EOF or closed is expected
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("mock peer did not finish")
	}
}

func TestTerminalUnsupportedVersion(t *testing.T) {
	serverPub, serverPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen server key: %v", err)
	}
	peerPub, peerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen peer key: %v", err)
	}

	peerAddr, _ := runMockTerminalPeer(t, peerPriv, "old-peer", "", "1.3.9")
	rvServer := runMockRendezvous(t, serverPriv, "old-peer", *peerAddr, peerPub)

	opts := DialOpts{
		Server:   rvServer,
		PeerID:   "old-peer",
		Password: "",
		Key:      base64.StdEncoding.EncodeToString(serverPub),
		Timeout:  5 * time.Second,
	}

	_, err = OpenTerminal(opts, 24, 80)
	if err == nil {
		t.Fatalf("expected version rejection, got nil")
	}
	if !strings.Contains(err.Error(), "1.4.1") {
		t.Fatalf("error should mention 1.4.1, got %v", err)
	}
}

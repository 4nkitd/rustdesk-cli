package hbb

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "rdcli/internal/hbb/proto"
)

func mustSignIDPk(t *testing.T, serverEdPriv ed25519.PrivateKey, peerID string, pk []byte) []byte {
	t.Helper()
	idpk, err := proto.Marshal(&pb.IdPk{Id: peerID, Pk: pk})
	if err != nil {
		t.Fatalf("marshal idpk: %v", err)
	}
	sig := ed25519.Sign(serverEdPriv, idpk)
	return append(sig, idpk...)
}

func runMockPeer(t *testing.T, peerEdPriv ed25519.PrivateKey, peerID, password string) (*netip.AddrPort, chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("peer listen: %v", err)
	}
	addr := ln.Addr().(*net.TCPAddr).AddrPort()
	done := make(chan struct{})

	go func() {
		defer close(done)
		closed := false
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			ok := handleMockPeerConn(t, peerEdPriv, peerID, password, conn)
			if ok && !closed {
				closed = true
				close(done)
			}
		}
	}()
	_ = addr
	return &addr, done
}

// handleMockPeerConn serves one accepted connection on the controlled-side
// mock. Returns true when a full login exchange completed.
func handleMockPeerConn(t *testing.T, peerEdPriv ed25519.PrivateKey, peerID, password string, conn net.Conn) bool {
	defer conn.Close()
	sc := NewSecureConn(conn)

	// peer ephemeral x25519 keypair for this connection
	kp, err := GenerateKeyPair()
	if err != nil {
		return false
	}
	sid := mustSignIDPk(t, peerEdPriv, peerID, kp.Pub)
	msg := &pb.Message{Union: &pb.Message_SignedId{SignedId: &pb.SignedId{Id: sid}}}
	data, _ := proto.Marshal(msg)
	sc.Write(data)

	raw, err := sc.ReadMessage()
	if err != nil {
		return false
	}
	in := &pb.Message{}
	if err := proto.Unmarshal(raw, in); err != nil {
		return false
	}
	pk := in.GetPublicKey()
	if pk == nil {
		return false
	}
	key, err := DecryptSymmetricKey(pk.GetSymmetricValue(), pk.GetAsymmetricValue(), kp.Priv)
	if err != nil {
		return false
	}
	sc.SetKey(key)

	hash := &pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Salt: "salt123", Challenge: "ch123"}}}
	data, _ = proto.Marshal(hash)
	sc.Write(data)

	raw, err = sc.ReadMessage()
	if err != nil {
		return false
	}
	lr := &pb.Message{}
	if err := proto.Unmarshal(raw, lr); err != nil {
		return false
	}
	login := lr.GetLoginRequest()
	if login == nil {
		return false
	}
	want := hashPassword(password, "salt123", "ch123")
	if !bytes.Equal(login.GetPassword(), want) {
		return false
	}

	pi := &pb.Message{Union: &pb.Message_LoginResponse{
		LoginResponse: &pb.LoginResponse{
			Union: &pb.LoginResponse_PeerInfo{
				PeerInfo: &pb.PeerInfo{Version: "1.4.1", Platform: "Linux", Hostname: "mock"},
			},
		},
	}}
	data, _ = proto.Marshal(pi)
	sc.Write(data)

	sc.SetRaw()
	buf := make([]byte, 256)
	for {
		n, err := sc.Read(buf)
		if n > 0 {
			sc.Write(buf[:n])
		}
		if err != nil {
			return true
		}
	}
}

func runMockRendezvous(t *testing.T, serverEdPriv ed25519.PrivateKey, peerID string, peerAddr netip.AddrPort, peerEdPub []byte) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("rendezvous listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				msg, err := readRendezvousMessage(conn)
				if err != nil {
					return
				}
				pr := msg.GetPunchHoleRequest()
				if pr != nil {
					signedPk := mustSignIDPk(t, serverEdPriv, peerID, peerEdPub)
					resp := &pb.RendezvousMessage{
						Union: &pb.RendezvousMessage_PunchHoleResponse{
							PunchHoleResponse: &pb.PunchHoleResponse{
								SocketAddr:  EncodeAddr(peerAddr),
								Pk:          signedPk,
								RelayServer: "",
								Union:       &pb.PunchHoleResponse_IsLocal{IsLocal: true},
							},
						},
					}
					_ = writeRendezvousMessage(conn, resp)
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func TestConnectAndLogin(t *testing.T) {
	serverPub, serverPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen server key: %v", err)
	}
	peerPub, peerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen peer key: %v", err)
	}

	password := "hunter2"
	peerAddr, done := runMockPeer(t, peerPriv, "test-peer", password)
	rvServer := runMockRendezvous(t, serverPriv, "test-peer", *peerAddr, peerPub)

	c, err := Connect(DialOpts{
		Server:   rvServer,
		PeerID:   "test-peer",
		Password: password,
		Key:      base64.StdEncoding.EncodeToString(serverPub),
		Timeout:  10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer c.Close()

	pi, err := c.Login(LoginTunnel, LoginSpec{Host: "localhost", Port: 22})
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	if pi.GetVersion() != "1.4.1" {
		t.Fatalf("peer version got %s", pi.GetVersion())
	}

	c.SetRaw()
	if _, err := c.WriteRaw([]byte("ping-through-tunnel")); err != nil {
		t.Fatalf("WriteRaw: %v", err)
	}
	buf := make([]byte, 64)
	n, err := c.ReadRaw(buf)
	if err != nil {
		t.Fatalf("ReadRaw: %v", err)
	}
	if !bytes.Equal(buf[:n], []byte("ping-through-tunnel")) {
		t.Fatalf("echo mismatch: %q", buf[:n])
	}
	c.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("mock peer did not finish")
	}
}

func TestConnectInsecureFallback(t *testing.T) {
	// Without a valid --key, the client must fall back to a plaintext
	// connection and still complete login.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("peer listen: %v", err)
	}
	done := make(chan struct{})
	var doneOnce sync.Once
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				sc := NewSecureConn(conn)
				raw, err := sc.ReadMessage()
				if err != nil {
					return
				}
				in := &pb.Message{}
				_ = proto.Unmarshal(raw, in)
				if in.GetPublicKey() != nil {
					return
				}
				// peer accepts insecure: send hash and expect login
				hash, _ := proto.Marshal(&pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Salt: "s", Challenge: "c"}}})
				sc.Write(hash)
				raw, err = sc.ReadMessage()
				if err != nil {
					return
				}
				lr := &pb.Message{}
				if err := proto.Unmarshal(raw, lr); err != nil {
					return
				}
				if lr.GetLoginRequest() == nil {
					return
				}
				pi, _ := proto.Marshal(&pb.Message{Union: &pb.Message_LoginResponse{
					LoginResponse: &pb.LoginResponse{Union: &pb.LoginResponse_PeerInfo{PeerInfo: &pb.PeerInfo{Version: "1.4.1"}}},
				}})
				sc.Write(pi)
				doneOnce.Do(func() { close(done) })
			}()
		}
	}()

	// mock rendezvous that returns an unsigned/empty pk so the client cannot
	// verify the peer identity
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
			go func(conn net.Conn) {
				defer conn.Close()
				msg, err := readRendezvousMessage(conn)
				if err != nil {
					return
				}
				if msg.GetTestNatRequest() != nil {
					port := int32(conn.RemoteAddr().(*net.TCPAddr).Port)
					_ = writeRendezvousMessage(conn, &pb.RendezvousMessage{
						Union: &pb.RendezvousMessage_TestNatResponse{
							TestNatResponse: &pb.TestNatResponse{Port: port},
						},
					})
					return
				}
				addr := ln.Addr().(*net.TCPAddr).AddrPort()
				resp := &pb.RendezvousMessage{
					Union: &pb.RendezvousMessage_PunchHoleResponse{
						PunchHoleResponse: &pb.PunchHoleResponse{
							SocketAddr: EncodeAddr(addr),
							Union:      &pb.PunchHoleResponse_IsLocal{IsLocal: true},
						},
					},
				}
				_ = writeRendezvousMessage(conn, resp)
			}(conn)
		}
	}()

	c, err := Connect(DialOpts{
		Server:   rvLn.Addr().String(),
		PeerID:   "test-peer",
		Password: "",
		Key:      "",
		Timeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer c.Close()
	if c.sc.IsSecured() {
		t.Fatalf("expected insecure connection")
	}
	pi, err := c.Login(LoginTunnel, LoginSpec{Host: "localhost", Port: 80})
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	if pi.GetVersion() != "1.4.1" {
		t.Fatalf("peer version got %s", pi.GetVersion())
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("mock peer did not finish")
	}
}

func TestIsDirectIP(t *testing.T) {
	cases := map[string]bool{
		"1.2.3.4":         true,
		"1.2.3.4:21116":   true,
		"[::1]:21116":     true,
		"1492090403":      false,
		"test-peer":       false,
		"example.com:443": false,
		"":                false,
	}
	for in, want := range cases {
		if got := isDirectIP(in); got != want {
			t.Errorf("isDirectIP(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestConnectDirectRequiresYes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	_, err = Connect(DialOpts{PeerID: addr, Timeout: time.Second})
	if err == nil {
		t.Fatalf("expected error without --yes")
	}

	go func() {
		c, aErr := ln.Accept()
		if aErr != nil {
			return
		}
		sc := NewSecureConn(c)
		raw, rErr := sc.ReadMessage()
		if rErr != nil {
			return
		}
		_ = raw
		msg, _ := proto.Marshal(&pb.Message{Union: &pb.Message_LoginResponse{
			LoginResponse: &pb.LoginResponse{Union: &pb.LoginResponse_PeerInfo{PeerInfo: &pb.PeerInfo{Version: "1.4.1"}}},
		}})
		_, _ = sc.Write(msg)
	}()

	conn, err := Connect(DialOpts{PeerID: addr, Yes: true, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Connect direct with --yes failed: %v", err)
	}
	defer conn.Close()
	if !conn.Direct() {
		t.Fatalf("expected direct connection")
	}
	if conn.sc.IsSecured() {
		t.Fatalf("expected plaintext (insecure) direct connection")
	}
}

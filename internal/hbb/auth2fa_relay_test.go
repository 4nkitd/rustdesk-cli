package hbb

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func TestAuth2FARelayFallback(t *testing.T) {
	serverPub, serverPriv, _ := ed25519.GenerateKey(rand.Reader)
	peerPub, peerPriv, _ := ed25519.GenerateKey(rand.Reader)
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	signedPeer := mustSignIDPk(t, serverPriv, "peer", peerPub)
	signedSession := mustSignIDPk(t, peerPriv, "peer", kp.Pub)
	rv, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer rv.Close()
	relay, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	uuid := make(chan string, 1)
	rvDone := make(chan error, 1)
	go func() {
		rvDone <- func() error {
			for i := 0; i < 2; i++ {
				conn, err := rv.Accept()
				if err != nil {
					return err
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(2 * time.Second))
				msg, err := readRendezvousMessage(conn)
				if err != nil {
					return err
				}
				if i == 0 {
					ph := msg.GetPunchHoleRequest()
					if ph == nil || !ph.GetForceRelay() || ph.GetNatType() != pb.NatType_SYMMETRIC || ph.GetConnType() != pb.ConnType_TERMINAL {
						return errors.New("incorrect forced relay punch")
					}
					if err := writeRendezvousMessage(conn, &pb.RendezvousMessage{Union: &pb.RendezvousMessage_PunchHoleResponse{
						PunchHoleResponse: &pb.PunchHoleResponse{
							SocketAddr: EncodeAddr(netip.MustParseAddrPort("127.0.0.1:9")), RelayServer: relay.Addr().String(), Pk: signedPeer,
						},
					}}); err != nil {
						return err
					}
				} else {
					rr := msg.GetRequestRelay()
					if rr == nil || rr.GetUuid() == "" || rr.GetRelayServer() != relay.Addr().String() || !rr.GetSecure() || rr.GetConnType() != pb.ConnType_DEFAULT_CONN {
						return errors.New("incorrect rendezvous relay request")
					}
					uuid <- rr.GetUuid()
					if err := writeRendezvousMessage(conn, &pb.RendezvousMessage{Union: &pb.RendezvousMessage_RelayResponse{
						RelayResponse: &pb.RelayResponse{Uuid: "must-not-replace-client-uuid"},
					}}); err != nil {
						return err
					}
				}
			}
			return nil
		}()
	}()
	peerDone := make(chan error, 1)
	go func() {
		peerDone <- func() error {
			conn, err := relay.Accept()
			if err != nil {
				return err
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(2 * time.Second))
			msg, err := readRendezvousMessage(conn)
			if err != nil {
				return err
			}
			var expectedUUID string
			select {
			case expectedUUID = <-uuid:
			case <-time.After(time.Second):
				return errors.New("missing rendezvous relay request")
			}
			rr := msg.GetRequestRelay()
			if rr == nil || rr.GetUuid() != expectedUUID || rr.GetConnType() != pb.ConnType_TERMINAL {
				return errors.New("relay pairing lost client UUID or terminal connection type")
			}
			sc := NewSecureConn(conn)
			if err := writeProtoMessage(sc, &pb.Message{Union: &pb.Message_SignedId{SignedId: &pb.SignedId{Id: signedSession}}}); err != nil {
				return err
			}
			keyMsg, err := readProtoMessage(sc)
			if err != nil {
				return err
			}
			pk := keyMsg.GetPublicKey()
			if pk == nil {
				return errors.New("missing encrypted handshake")
			}
			key, err := DecryptSymmetricKey(pk.GetSymmetricValue(), pk.GetAsymmetricValue(), kp.Priv)
			if err != nil {
				return err
			}
			sc.SetKey(key)
			if err := writeProtoMessage(sc, auth2FAHash("challenge")); err != nil {
				return err
			}
			if err := auth2FACheckLogin(sc, LoginTerminal); err != nil {
				return err
			}
			if err := writeProtoMessage(sc, auth2FAError("2FA Required")); err != nil {
				return err
			}
			if err := auth2FACheckCode(sc); err != nil {
				return err
			}
			if err := writeProtoMessage(sc, auth2FASuccess()); err != nil {
				return err
			}
			request, err := readProtoMessage(sc)
			if err != nil {
				return err
			}
			return writeProtoMessage(sc, request)
		}()
	}()
	c, err := Connect(DialOpts{
		Server: rv.Addr().String(), PeerID: "peer", Key: base64.StdEncoding.EncodeToString(serverPub),
		Password: "password", OTP: "012345", ForceRelay: true, ConnType: pb.ConnType_TERMINAL, Timeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Login(LoginTerminal, LoginSpec{}); err != nil {
		t.Fatal(err)
	}
	if c.Direct() || !c.sc.IsSecured() {
		t.Fatal("expected authenticated encrypted relay")
	}
	time.Sleep(250 * time.Millisecond)
	if err := c.Send(&pb.Message{Union: &pb.Message_TestDelay{TestDelay: &pb.TestDelay{Time: 42}}}); err != nil {
		t.Fatal("relay session retained handshake deadline:", err)
	}
	msg, err := c.Recv()
	if err != nil || msg.GetTestDelay().GetTime() != 42 {
		t.Fatal("relay session expired after login:", err)
	}
	if err := <-rvDone; err != nil {
		t.Fatal(err)
	}
	if err := <-peerDone; err != nil {
		t.Fatal(err)
	}
}

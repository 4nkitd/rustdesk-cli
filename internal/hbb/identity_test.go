package hbb

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net"
	"testing"
	"time"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func TestRejectInvalidIdentityEvenWithYes(t *testing.T) {
	serverPub, serverPriv, _ := ed25519.GenerateKey(rand.Reader)
	peerPub, peerPriv, _ := ed25519.GenerateKey(rand.Reader)
	key := base64.StdEncoding.EncodeToString(serverPub)
	for _, kind := range []string{"server signature", "server id", "missing signed id", "peer signature", "peer id"} {
		t.Run(kind, func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			a.SetDeadline(time.Now().Add(time.Second))
			b.SetDeadline(time.Now().Add(time.Second))
			blob := mustSignIDPk(t, serverPriv, "peer", peerPub)
			if kind == "server signature" {
				blob[0] ^= 1
			}
			if kind == "server id" {
				blob = mustSignIDPk(t, serverPriv, "other", peerPub)
			}
			kp, _ := GenerateKeyPair()
			sid := mustSignIDPk(t, peerPriv, "peer", kp.Pub)
			if kind == "peer signature" {
				sid[0] ^= 1
			}
			if kind == "peer id" {
				sid = mustSignIDPk(t, peerPriv, "other", kp.Pub)
			}
			msg := &pb.Message{Union: &pb.Message_SignedId{SignedId: &pb.SignedId{Id: sid}}}
			if kind == "missing signed id" {
				msg = &pb.Message{}
			}
			if kind != "server signature" && kind != "server id" {
				go writeProtoMessage(NewSecureConn(b), msg)
			}
			c := &Conn{sc: NewSecureConn(a), peerID: "peer", opts: DialOpts{Yes: true}}
			if err := c.secureHandshake(blob, key); err == nil {
				t.Fatal("invalid identity accepted")
			}
			if c.sc.IsSecured() {
				t.Fatal("invalid identity marked secure")
			}
		})
	}
}

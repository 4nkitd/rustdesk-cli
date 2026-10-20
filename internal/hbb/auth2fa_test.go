package hbb

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func auth2FAError(text string) *pb.Message {
	return &pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: &pb.LoginResponse{
		Union: &pb.LoginResponse_Error{Error: text}, EnableTrustedDevices: true,
	}}}
}

func auth2FAHash(challenge string) *pb.Message {
	return &pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Salt: "salt", Challenge: challenge}}}
}

func auth2FASuccess() *pb.Message {
	return &pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: &pb.LoginResponse{
		Union: &pb.LoginResponse_PeerInfo{PeerInfo: &pb.PeerInfo{Version: "1.4.1"}},
	}}}
}

func auth2FACheckLogin(sc *SecureConn, kind LoginKind) error {
	msg, err := readProtoMessage(sc)
	if err != nil {
		return err
	}
	lr := msg.GetLoginRequest()
	if lr == nil || lr.GetUsername() != "peer" || !bytes.Equal(lr.GetPassword(), hashPassword("password", "salt", "challenge")) || len(lr.GetHwid()) != 0 {
		return errors.New("invalid login request or unexpected device enrollment")
	}
	if lr.GetOption() != nil {
		return errors.New("default login options changed")
	}
	switch kind {
	case LoginDefault:
		if lr.GetUnion() != nil {
			return errors.New("unexpected default login kind")
		}
	case LoginFileTransfer:
		if lr.GetFileTransfer() == nil || lr.GetFileTransfer().GetDir() != "/mock" {
			return errors.New("file login kind lost")
		}
	case LoginTunnel:
		if lr.GetPortForward() == nil || lr.GetPortForward().GetHost() != "localhost" || lr.GetPortForward().GetPort() != 22 {
			return errors.New("tunnel login kind lost")
		}
	case LoginTerminal:
		if lr.GetTerminal() == nil {
			return errors.New("terminal login kind lost")
		}
	}
	return nil
}

func auth2FACheckCode(sc *SecureConn) error {
	msg, err := readProtoMessage(sc)
	if err != nil {
		return err
	}
	auth := msg.GetAuth_2Fa()
	if auth == nil || auth.GetCode() != "012345" || len(auth.GetHwid()) != 0 {
		return errors.New("incorrect Auth2FA response or unexpected device enrollment")
	}
	return nil
}

func TestAuth2FALogin(t *testing.T) {
	for _, kind := range []LoginKind{LoginDefault, LoginFileTransfer, LoginTunnel, LoginTerminal} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			runAuth2FAMock(t, "012345", kind, "", func(sc *SecureConn) error {
				if err := writeProtoMessage(sc, auth2FAHash("challenge")); err != nil {
					return err
				}
				if err := auth2FACheckLogin(sc, kind); err != nil {
					return err
				}
				if err := writeProtoMessage(sc, auth2FAError("2FA Required")); err != nil {
					return err
				}
				if err := auth2FACheckCode(sc); err != nil {
					return err
				}
				delay := &pb.Message{Union: &pb.Message_TestDelay{TestDelay: &pb.TestDelay{Time: 42}}}
				if err := writeProtoMessage(sc, delay); err != nil {
					return err
				}
				msg, err := readProtoMessage(sc)
				if err != nil || msg.GetTestDelay().GetTime() != 42 {
					return errors.New("login keepalive was not echoed")
				}
				return writeProtoMessage(sc, auth2FASuccess())
			})
		})
	}
}

func TestAuth2FAFailures(t *testing.T) {
	for _, tc := range []struct {
		name, otp, challenge, want string
		login, sendOTP             bool
		last                       *pb.Message
	}{
		{"missing code", "", "challenge", "supply --otp or RDC_OTP", true, false, auth2FAError("2FA Required")},
		{"wrong code", "012345", "challenge", "peer rejected 2FA code", true, true, auth2FAError("Wrong 2FA Code")},
		{"replayed 2FA challenge", "012345", "challenge", "repeated peer 2FA challenge", true, true, auth2FAError("2FA Required")},
		{"replayed hash after OTP", "012345", "challenge", "repeated login challenge", true, true, auth2FAHash("challenge")},
		{"changed hash after login", "012345", "challenge", "repeated login challenge", true, false, auth2FAHash("different")},
		{"empty hash", "012345", "", "missing or repeated login challenge", false, false, nil},
		{"unsolicited challenge", "012345", "", "before login challenge", false, false, auth2FAError("2FA Required")},
		{"unsolicited success", "012345", "", "before login challenge", false, false, auth2FASuccess()},
		{"unexpected Auth2FA", "012345", "challenge", "unexpected peer 2FA message", true, false, &pb.Message{Union: &pb.Message_Auth_2Fa{Auth_2Fa: &pb.Auth2FA{}}}},
		{"password rejected", "012345", "challenge", "Wrong Password", true, false, auth2FAError("Wrong Password")},
		{"no 2FA needed", "012345", "challenge", "", true, false, auth2FASuccess()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runAuth2FAMock(t, tc.otp, LoginDefault, tc.want, func(sc *SecureConn) error {
				if tc.login || tc.last == nil {
					if err := writeProtoMessage(sc, auth2FAHash(tc.challenge)); err != nil {
						return err
					}
				}
				if tc.login {
					if err := auth2FACheckLogin(sc, LoginDefault); err != nil {
						return err
					}
				}
				if tc.sendOTP {
					if err := writeProtoMessage(sc, auth2FAError("2FA Required")); err != nil {
						return err
					}
					if err := auth2FACheckCode(sc); err != nil {
						return err
					}
				}
				if tc.last != nil {
					return writeProtoMessage(sc, tc.last)
				}
				return nil
			})
		})
	}
}

func runAuth2FAMock(t *testing.T, otp string, kind LoginKind, wantErr string, script func(*SecureConn) error) error {
	t.Helper()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	client, peer := NewSecureConn(a), NewSecureConn(b)
	key := [32]byte{1, 2, 3}
	client.SetKey(key)
	peer.SetKey(key)
	if err := b.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		if err := script(peer); err != nil {
			done <- err
			b.Close()
			return
		}
		if _, err := readProtoMessage(peer); err == nil {
			done <- errors.New("unexpected extra login or OTP submission")
			return
		}
		done <- nil
	}()
	c := &Conn{sc: client, peerID: "peer", opts: DialOpts{Password: "password", Token: "fixture-token", OTP: otp, Timeout: time.Second}}
	pi, err := c.Login(kind, LoginSpec{Dir: "/mock", Host: "localhost", Port: 22})
	c.Close()
	if wantErr == "" {
		if err != nil || pi.GetVersion() != "1.4.1" || !c.sc.IsSecured() {
			t.Errorf("login failed: %v", err)
		}
	} else if err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Errorf("got error %v, want %q", err, wantErr)
	}
	if c.opts.OTP != "" {
		t.Error("connection retained OTP")
	}
	if err != nil && otp != "" && strings.Contains(err.Error(), otp) {
		t.Error("error disclosed OTP")
	}
	if err := <-done; err != nil {
		t.Error(err)
	}
	return err
}

func TestAuth2FAReflectedSecrets(t *testing.T) {
	for _, kind := range []string{"login error", "close reason"} {
		for _, secret := range []string{"012345", "password", "fixture-token"} {
			t.Run(kind+"/"+fmt.Sprint(len(secret)), func(t *testing.T) {
				want := "hbb: peer rejected login"
				reflected := "invalid code or credential: " + secret
				last := auth2FAError(reflected)
				if kind == "close reason" {
					want = "hbb: peer closed connection during login"
					last = &pb.Message{Union: &pb.Message_Misc{Misc: &pb.Misc{
						Union: &pb.Misc_CloseReason{CloseReason: reflected},
					}}}
				}
				err := runAuth2FAMock(t, "012345", LoginDefault, want, func(sc *SecureConn) error {
					if err := writeProtoMessage(sc, auth2FAHash("challenge")); err != nil {
						return err
					}
					if err := auth2FACheckLogin(sc, LoginDefault); err != nil {
						return err
					}
					if err := writeProtoMessage(sc, auth2FAError("2FA Required")); err != nil {
						return err
					}
					if err := auth2FACheckCode(sc); err != nil {
						return err
					}
					return writeProtoMessage(sc, last)
				})
				if err == nil || err.Error() != want {
					t.Fatal("authentication failure was not replaced with a fixed safe message")
				}
			})
		}
	}
}

func TestAuth2FAInvalidCode(t *testing.T) {
	for _, otp := range []string{"12345", "123456789", "abcdef", "１２３４５６", "12345 ", "123\n56"} {
		t.Run(fmt.Sprint(len(otp)), func(t *testing.T) {
			runAuth2FAMock(t, otp, LoginDefault, "6 to 8 ASCII digits", func(sc *SecureConn) error {
				if err := writeProtoMessage(sc, auth2FAHash("challenge")); err != nil {
					return err
				}
				if err := auth2FACheckLogin(sc, LoginDefault); err != nil {
					return err
				}
				return writeProtoMessage(sc, auth2FAError("2FA Required"))
			})
		})
	}
}

func TestAuth2FALoginTimeout(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	c := &Conn{sc: NewSecureConn(a), opts: DialOpts{OTP: "012345", Timeout: 20 * time.Millisecond}}
	_, err := c.Login(LoginDefault, LoginSpec{})
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("missing challenge must time out, got %v", err)
	}
	if c.opts.OTP != "" {
		t.Fatal("connection retained OTP after timeout")
	}
}

func TestAuth2FAMessageLimit(t *testing.T) {
	runAuth2FAMock(t, "012345", LoginDefault, "too many messages", func(sc *SecureConn) error {
		for i := 0; i < 128; i++ {
			if err := writeProtoMessage(sc, &pb.Message{}); err != nil {
				return err
			}
		}
		return nil
	})
}

func TestAuth2FARequiresEncryptionEvenWithYes(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	done := make(chan error, 1)
	go func() {
		sc := NewSecureConn(b)
		if err := writeProtoMessage(sc, auth2FAHash("challenge")); err != nil {
			done <- err
			return
		}
		if err := auth2FACheckLogin(sc, LoginDefault); err != nil {
			done <- err
			return
		}
		if err := writeProtoMessage(sc, auth2FAError("2FA Required")); err != nil {
			done <- err
			return
		}
		if _, err := readProtoMessage(sc); err == nil {
			done <- errors.New("OTP sent without encryption")
			return
		}
		done <- nil
	}()
	c := &Conn{sc: NewSecureConn(a), peerID: "peer", opts: DialOpts{Password: "password", OTP: "012345", Yes: true, Timeout: time.Second}}
	_, err := c.Login(LoginDefault, LoginSpec{})
	c.Close()
	if err == nil || !strings.Contains(err.Error(), "authenticated encrypted connection") {
		t.Errorf("unencrypted OTP was not rejected: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAuth2FAHandshakeTimeout(t *testing.T) {
	serverPub, serverPriv, _ := ed25519.GenerateKey(rand.Reader)
	peerPub, _, _ := ed25519.GenerateKey(rand.Reader)
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	c := &Conn{sc: NewSecureConn(a), peerID: "peer", opts: DialOpts{Timeout: 20 * time.Millisecond}}
	err := c.secureHandshake(mustSignIDPk(t, serverPriv, "peer", peerPub), base64.StdEncoding.EncodeToString(serverPub))
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("stalled handshake must time out, got %v", err)
	}
}

func TestAuth2FASessionOutlivesLoginDeadline(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	client, peer := NewSecureConn(a), NewSecureConn(b)
	key := [32]byte{1, 2, 3}
	client.SetKey(key)
	peer.SetKey(key)
	b.SetDeadline(time.Now().Add(2 * time.Second))
	done := make(chan error, 1)
	go func() {
		if err := writeProtoMessage(peer, auth2FAHash("challenge")); err != nil {
			done <- err
			return
		}
		if err := auth2FACheckLogin(peer, LoginDefault); err != nil {
			done <- err
			return
		}
		if err := writeProtoMessage(peer, auth2FASuccess()); err != nil {
			done <- err
			return
		}
		msg, err := readProtoMessage(peer)
		if err != nil {
			done <- err
			return
		}
		done <- writeProtoMessage(peer, msg)
	}()
	c := &Conn{sc: client, peerID: "peer", opts: DialOpts{Password: "password", Timeout: 50 * time.Millisecond}}
	if _, err := c.Login(LoginDefault, LoginSpec{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := c.Send(&pb.Message{Union: &pb.Message_TestDelay{TestDelay: &pb.TestDelay{Time: 42}}}); err != nil {
		t.Fatal("session write retained login deadline:", err)
	}
	msg, err := c.Recv()
	if err != nil || msg.GetTestDelay().GetTime() != 42 {
		t.Fatal("session read retained login deadline:", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAuth2FALoginOptions(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	options := &pb.OptionMessage{
		DisableAudio: pb.OptionMessage_Yes, DisableClipboard: pb.OptionMessage_Yes,
		DisableKeyboard: pb.OptionMessage_Yes, DisableCamera: pb.OptionMessage_Yes,
		EnableFileTransfer: pb.OptionMessage_No,
	}
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			sc := NewSecureConn(b)
			if err := writeProtoMessage(sc, auth2FAHash("challenge")); err != nil {
				return err
			}
			msg, err := readProtoMessage(sc)
			if err != nil {
				return err
			}
			if !proto.Equal(msg.GetLoginRequest().GetOption(), options) {
				return errors.New("login privacy options were not preserved")
			}
			return writeProtoMessage(sc, auth2FASuccess())
		}()
	}()
	c := &Conn{sc: NewSecureConn(a), peerID: "peer", opts: DialOpts{Timeout: time.Second}}
	if _, err := c.Login(LoginDefault, LoginSpec{Option: options}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

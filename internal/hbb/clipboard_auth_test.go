package hbb

import (
	"errors"
	"strings"
	"testing"
	"time"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func clipboardAuthError(text string) *pb.Message {
	return &pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: &pb.LoginResponse{
		Union: &pb.LoginResponse_Error{Error: text}, EnableTrustedDevices: true,
	}}}
}

func clipboardAuthHash(challenge string) *pb.Message {
	return &pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Challenge: challenge, Salt: "salt"}}}
}

func clipboardAuthSuccess() *pb.Message {
	return &pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: &pb.LoginResponse{
		Union: &pb.LoginResponse_PeerInfo{PeerInfo: &pb.PeerInfo{Version: "1.5.0"}},
	}}}
}

func TestClipboardOTPLogin(t *testing.T) {
	const password = "clipboard-password-canary"
	const token = "clipboard-token-canary"
	reflectedClose := &pb.Message{Union: &pb.Message_Misc{Misc: &pb.Misc{Union: &pb.Misc_CloseReason{
		CloseReason: "reflected 012345 " + password + " " + token,
	}}}}
	for _, test := range []struct {
		name    string
		otp     string
		plain   bool
		login   bool
		sendOTP bool
		last    *pb.Message
		want    string
	}{
		{"encrypted success", "012345", false, true, true, clipboardAuthSuccess(), ""},
		{"no OTP needed", "012345", false, true, false, clipboardAuthSuccess(), ""},
		{"missing code", "", false, true, false, clipboardAuthError("2FA Required"), "supply --otp or RDC_OTP"},
		{"wrong code", "012345", false, true, true, clipboardAuthError("Wrong 2FA Code"), "peer rejected 2FA code"},
		{"repeated OTP challenge", "012345", false, true, true, clipboardAuthError("2FA Required"), "repeated peer 2FA challenge"},
		{"repeated hash after OTP", "012345", false, true, true, clipboardAuthHash("again"), "repeated login challenge"},
		{"repeated hash before OTP", "012345", false, true, false, clipboardAuthHash("again"), "repeated login challenge"},
		{"unsolicited OTP challenge", "012345", false, false, false, clipboardAuthError("2FA Required"), "before login challenge"},
		{"unsolicited success", "012345", false, false, false, clipboardAuthSuccess(), "before login challenge"},
		{"empty challenge", "012345", false, false, false, clipboardAuthHash(""), "missing or repeated login challenge"},
		{"unencrypted despite yes", "012345", true, true, false, clipboardAuthError("2FA Required"), "authenticated encrypted connection"},
		{"no PeerInfo after OTP", "012345", false, true, true, nil, "timeout"},
		{"reflected credentials before OTP", "012345", false, true, false, reflectedClose, "hbb: peer closed clipboard session"},
		{"reflected credentials after OTP", "012345", false, true, true, reflectedClose, "hbb: peer closed clipboard session"},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := [32]byte{4, 2, 1}
			timeout := time.Second
			if test.last == nil {
				timeout = 30 * time.Millisecond
			}
			c := clipboardPipe(t, timeout, func(peer *Conn) error {
				if !test.plain {
					peer.sc.SetKey(key)
				}
				if test.login {
					if err := peer.Send(clipboardAuthHash("challenge")); err != nil {
						return err
					}
					msg, err := peer.Recv()
					if err != nil {
						return err
					}
					lr := msg.GetLoginRequest()
					option := lr.GetOption()
					if lr == nil || lr.GetUnion() != nil || len(lr.GetHwid()) != 0 || option.GetDisableClipboard() != pb.OptionMessage_Yes || option.GetDisableKeyboard() != pb.OptionMessage_Yes || option.GetDisableAudio() != pb.OptionMessage_Yes || option.GetEnableFileTransfer() != pb.OptionMessage_No {
						return errors.New("clipboard login lost privacy options or included device enrollment")
					}
				}
				if test.sendOTP {
					if err := peer.Send(clipboardAuthError("2FA Required")); err != nil {
						return err
					}
					msg, err := peer.Recv()
					if err != nil {
						return err
					}
					auth := msg.GetAuth_2Fa()
					if auth == nil || auth.GetCode() != test.otp || len(auth.GetHwid()) != 0 {
						return errors.New("incorrect clipboard OTP response or unexpected device enrollment")
					}
				}
				if test.last != nil {
					if err := peer.Send(test.last); err != nil {
						return err
					}
				}
				if test.want == "" {
					msg, err := peer.Recv()
					if err != nil {
						return err
					}
					displays := msg.GetMisc().GetCaptureDisplays()
					if displays == nil || len(displays.GetSet()) != 0 || len(displays.GetAdd()) != 0 {
						return errors.New("clipboard login did not unsubscribe capture")
					}
				}
				if _, err := peer.Recv(); err == nil {
					return errors.New("unexpected extra login, OTP, or clipboard subscription")
				}
				return nil
			})
			if !test.plain {
				c.conn.sc.SetKey(key)
			}
			c.conn.opts.OTP = test.otp
			c.conn.opts.Password = password
			c.conn.opts.Token = token
			c.conn.opts.Yes = true
			if err := c.begin(); err != nil {
				t.Fatal(err)
			}
			err := c.login()
			retained := c.conn.opts.OTP != ""
			c.Close()
			if test.want == "" {
				if err != nil || c.conn.PeerInfo().GetVersion() != "1.5.0" {
					t.Fatalf("clipboard login failed: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("clipboard login error = %v, want %q", err, test.want)
			}
			if retained {
				t.Fatal("clipboard login retained OTP")
			}
			for _, secret := range []string{test.otp, password, token} {
				if err != nil && secret != "" && strings.Contains(err.Error(), secret) {
					t.Fatal("clipboard error disclosed a credential")
				}
			}
			if test.last == reflectedClose && err.Error() != "hbb: peer closed clipboard session" {
				t.Fatal("clipboard close error included peer-controlled details")
			}
		})
	}
}

func TestClipboardLateLoginErrorAfterOTP(t *testing.T) {
	for _, operation := range []string{"read", "write"} {
		t.Run(operation, func(t *testing.T) {
			const otp = "012345"
			const password = "clipboard-password-canary"
			const token = "clipboard-token-canary"
			key := [32]byte{3, 4, 5}
			c := clipboardPipe(t, time.Second, func(peer *Conn) error {
				peer.sc.SetKey(key)
				if err := peer.Send(clipboardAuthHash("challenge")); err != nil {
					return err
				}
				msg, err := peer.Recv()
				if err != nil || msg.GetLoginRequest() == nil {
					return errors.New("missing clipboard login request")
				}
				if err := peer.Send(clipboardAuthError("2FA Required")); err != nil {
					return err
				}
				msg, err = peer.Recv()
				if err != nil || msg.GetAuth_2Fa().GetCode() != otp {
					return errors.New("missing clipboard OTP response")
				}
				if err := peer.Send(clipboardAuthSuccess()); err != nil {
					return err
				}
				msg, err = peer.Recv()
				if err != nil || msg.GetMisc().GetCaptureDisplays() == nil {
					return errors.New("missing capture unsubscribe")
				}
				msg, err = peer.Recv()
				if err != nil {
					return err
				}
				if operation == "read" && msg.GetMisc().GetOption().GetDisableClipboard() != pb.OptionMessage_No {
					return errors.New("missing explicit clipboard receive subscription")
				}
				if operation == "write" && !msg.GetTestDelay().GetFromClient() {
					return errors.New("missing clipboard write preflight")
				}
				return peer.Send(clipboardAuthError("reflected " + otp + " " + password + " " + token))
			})
			c.conn.sc.SetKey(key)
			c.conn.opts.OTP = otp
			c.conn.opts.Password = password
			c.conn.opts.Token = token
			if err := c.begin(); err != nil {
				t.Fatal(err)
			}
			if err := c.login(); err != nil {
				t.Fatal(err)
			}
			if c.conn.opts.OTP != "" {
				t.Fatal("clipboard retained OTP after authentication")
			}
			var err error
			if operation == "read" {
				_, err = c.ReadText()
			} else {
				err = c.WriteText("explicit text")
			}
			if err == nil || err.Error() != "hbb: peer rejected clipboard session" {
				t.Fatal("late clipboard login error must use fixed text without reflected credentials")
			}
		})
	}
}

package hbb

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
	"google.golang.org/protobuf/proto"
)

func defaultPolicyNoise(sc *SecureConn) error {
	for _, msg := range []*pb.Message{
		{Union: &pb.Message_Clipboard{Clipboard: &pb.Clipboard{Content: []byte{0xff}, Compress: true}}},
		{Union: &pb.Message_MultiClipboards{MultiClipboards: &pb.MultiClipboards{Clipboards: []*pb.Clipboard{{Content: []byte{0xff}, Compress: true}}}}},
		{Union: &pb.Message_ScreenshotResponse{ScreenshotResponse: &pb.ScreenshotResponse{Sid: "unsolicited", Msg: "not this operation"}}},
		{Union: &pb.Message_VideoFrame{VideoFrame: &pb.VideoFrame{}}},
		{Union: &pb.Message_AudioFrame{AudioFrame: &pb.AudioFrame{}}},
		{Union: &pb.Message_FileResponse{FileResponse: &pb.FileResponse{}}},
		{Union: &pb.Message_Misc{Misc: &pb.Misc{Union: &pb.Misc_Option{Option: &pb.OptionMessage{
			DisableClipboard: pb.OptionMessage_No, DisableAudio: pb.OptionMessage_No,
			DisableKeyboard: pb.OptionMessage_No, EnableFileTransfer: pb.OptionMessage_Yes,
		}}}}},
	} {
		if err := writeProtoMessage(sc, msg); err != nil {
			return err
		}
	}
	return nil
}

func defaultPolicyLogin(sc *SecureConn) error {
	if err := writeProtoMessage(sc, auth2FAHash("challenge")); err != nil {
		return err
	}
	msg, err := readProtoMessage(sc)
	if err != nil {
		return err
	}
	lr := msg.GetLoginRequest()
	if lr == nil || lr.GetUnion() != nil || len(lr.GetHwid()) != 0 ||
		!bytes.Equal(lr.GetPassword(), hashPassword("password", "salt", "challenge")) {
		return errors.New("expected default password login before OTP or feature activation")
	}
	o := lr.GetOption()
	if o == nil || o.GetDisableClipboard() != pb.OptionMessage_Yes ||
		o.GetDisableAudio() != pb.OptionMessage_Yes || o.GetDisableKeyboard() != pb.OptionMessage_Yes ||
		o.GetEnableFileTransfer() != pb.OptionMessage_No {
		return fmt.Errorf("login enabled unrelated services: %v", o)
	}
	return nil
}

func defaultPolicyRun(t *testing.T, command string, secured bool, script func(*SecureConn) error) error {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	cs, ss := NewSecureConn(client), NewSecureConn(server)
	if secured {
		key := [32]byte{9, 7, 3}
		cs.SetKey(key)
		ss.SetKey(key)
	}
	if err := server.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		if err := script(ss); err != nil {
			done <- err
			return
		}
		msg, err := readProtoMessage(ss)
		if !errors.Is(err, io.EOF) {
			done <- fmt.Errorf("expected session close without extra feature/OTP messages, got %v, %v", msg, err)
			return
		}
		done <- nil
	}()
	opts := DialOpts{Password: "password", OTP: "012345", Yes: true, Timeout: time.Second}
	c := &Conn{sc: cs, peerID: "mock", opts: opts}
	var err error
	switch command {
	case "screenshot":
		var shot *Screenshot
		shot, err = c.takeScreenshot(1, time.Second)
		if err == nil && (shot.Width != 3 || shot.Height != 2 || !bytes.Equal(shot.Data, screenshotTestPNG(t))) {
			err = errors.New("screenshot returned unsolicited data")
		}
	case "clipboard-get", "clipboard-set":
		clip := &Clipboard{conn: c, timeout: time.Second, allowed: true, keyboard: true}
		err = clip.begin()
		if err == nil {
			err = clip.login()
		}
		if err == nil {
			if command == "clipboard-set" {
				err = clip.WriteText("explicit fixture text")
			} else {
				var text string
				text, err = clip.ReadText()
				if err == nil && text != "requested fixture text" {
					err = fmt.Errorf("clipboard read returned unsolicited text: %q", text)
				}
			}
		}
	case "diagnostics":
		result := probeWithConnector(opts, func(opts DialOpts) (*Conn, error) {
			if opts.ConnType != pb.ConnType_DEFAULT_CONN {
				return nil, errors.New("diagnostics requested a non-default connection")
			}
			return c, nil
		})
		if result.Error != nil {
			err = result.Error
		} else if !result.OK || result.Peer == nil || result.Peer.Version == nil || *result.Peer.Version != "1.4.1" {
			err = fmt.Errorf("diagnostics did not complete authenticated observation: %+v", result)
		}
	default:
		panic("unknown default-session command")
	}
	c.Close()
	if c.opts.OTP != "" {
		t.Error("command retained OTP after authentication")
	}
	if peerErr := <-done; peerErr != nil {
		t.Errorf("mock peer: %v", peerErr)
	}
	return err
}

func TestDefaultSessionCrossFeatureIsolation(t *testing.T) {
	t.Setenv("RDC_DEBUG", "")
	data := screenshotTestPNG(t)
	for _, command := range []string{"screenshot", "clipboard-get", "clipboard-set", "diagnostics"} {
		t.Run(command, func(t *testing.T) {
			attempts := 1
			if command == "screenshot" {
				attempts = 2
			}
			previousSID := ""
			for attempt := 0; attempt < attempts; attempt++ {
				err := defaultPolicyRun(t, command, true, func(sc *SecureConn) error {
					if err := defaultPolicyNoise(sc); err != nil {
						return err
					}
					if err := defaultPolicyLogin(sc); err != nil {
						return err
					}
					if err := defaultPolicyNoise(sc); err != nil {
						return err
					}
					if err := writeProtoMessage(sc, auth2FAError("2FA Required")); err != nil {
						return err
					}
					if err := auth2FACheckCode(sc); err != nil {
						return err
					}
					if err := defaultPolicyNoise(sc); err != nil {
						return err
					}
					if err := writeProtoMessage(sc, &pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: &pb.LoginResponse{
						Union: &pb.LoginResponse_PeerInfo{PeerInfo: screenshotTestInfo("1.4.1")},
					}}}); err != nil {
						return err
					}
					switch command {
					case "screenshot":
						req, err := screenshotTestRequest(sc)
						if err != nil {
							return err
						}
						if req.GetDisplay() != 1 || req.GetSid() == previousSID {
							return errors.New("screenshot did not select the requested display with a fresh Sid")
						}
						if previousSID != "" {
							if err := screenshotTestResponse(sc, previousSID, "replayed previous capture", nil); err != nil {
								return err
							}
						}
						previousSID = req.GetSid()
						if err := defaultPolicyNoise(sc); err != nil {
							return err
						}
						return screenshotTestResponse(sc, req.GetSid(), "", data)
					case "clipboard-get", "clipboard-set":
						msg, err := readProtoMessage(sc)
						if err != nil {
							return err
						}
						if !proto.Equal(msg, &pb.Message{Union: &pb.Message_Misc{Misc: &pb.Misc{
							Union: &pb.Misc_CaptureDisplays{CaptureDisplays: &pb.CaptureDisplays{}},
						}}}) {
							return errors.New("clipboard must unsubscribe every display before its operation")
						}
						if command == "clipboard-set" {
							return defaultPolicyClipboardSet(sc)
						}
						msg, err = readProtoMessage(sc)
						if err != nil {
							return err
						}
						if !proto.Equal(msg, &pb.Message{Union: &pb.Message_Misc{Misc: &pb.Misc{
							Union: &pb.Misc_Option{Option: &pb.OptionMessage{DisableClipboard: pb.OptionMessage_No, DisableKeyboard: pb.OptionMessage_No}},
						}}}) {
							return errors.New("clipboard get must enable only clipboard receive and keyboard")
						}
						if err := screenshotTestResponse(sc, "unsolicited", "ignore cross-feature failure", nil); err != nil {
							return err
						}
						return writeProtoMessage(sc, &pb.Message{Union: &pb.Message_Clipboard{Clipboard: &pb.Clipboard{Content: []byte("requested fixture text")}}})
					case "diagnostics":
						return defaultPolicyNoise(sc)
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func defaultPolicyClipboardSet(sc *SecureConn) error {
	for barrier := 0; barrier < 2; barrier++ {
		ping, err := readProtoMessage(sc)
		if err != nil {
			return err
		}
		if ping.GetTestDelay() == nil || !ping.GetTestDelay().GetFromClient() {
			return fmt.Errorf("clipboard set must use barriers without receive subscription or readback, got %v", ping)
		}
		if err := defaultPolicyNoise(sc); err != nil {
			return err
		}
		if err := writeProtoMessage(sc, ping); err != nil {
			return err
		}
		if barrier == 0 {
			msg, err := readProtoMessage(sc)
			if err != nil {
				return err
			}
			if !proto.Equal(msg, &pb.Message{Union: &pb.Message_Misc{Misc: &pb.Misc{
				Union: &pb.Misc_Option{Option: &pb.OptionMessage{DisableClipboard: pb.OptionMessage_No, DisableKeyboard: pb.OptionMessage_Yes}},
			}}}) {
				return fmt.Errorf("clipboard set must enable writes while keeping receive disabled: %v", msg)
			}
			msg, err = readProtoMessage(sc)
			if err != nil {
				return err
			}
			if !proto.Equal(msg, &pb.Message{Union: &pb.Message_Clipboard{Clipboard: &pb.Clipboard{Content: []byte("explicit fixture text")}}}) {
				return fmt.Errorf("clipboard set sent something other than explicit text: %v", msg)
			}
		}
	}
	return nil
}

func TestDefaultSessionOTPRequiresLoginAndEncryption(t *testing.T) {
	t.Setenv("RDC_DEBUG", "")
	for _, command := range []string{"screenshot", "clipboard-set", "diagnostics"} {
		for _, loginSent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/login=%v", command, loginSent), func(t *testing.T) {
				err := defaultPolicyRun(t, command, !loginSent, func(sc *SecureConn) error {
					if err := defaultPolicyNoise(sc); err != nil {
						return err
					}
					if loginSent {
						if err := defaultPolicyLogin(sc); err != nil {
							return err
						}
						if err := defaultPolicyNoise(sc); err != nil {
							return err
						}
					}
					return writeProtoMessage(sc, auth2FAError("2FA Required"))
				})
				if err == nil {
					t.Fatal("command accepted OTP challenge without prior login and encryption")
				}
				if command == "diagnostics" {
					want := "unexpected_response"
					if loginSent {
						want = "otp_failed"
					}
					var de *DiagnosticError
					if !errors.As(err, &de) || de.Code != want {
						t.Fatalf("diagnostic rejection = %v, want %s", err, want)
					}
				} else {
					want := "before login challenge"
					if loginSent {
						want = "authenticated encrypted connection"
					}
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("authentication rejection = %v, want %q", err, want)
					}
				}
			})
		}
	}
}

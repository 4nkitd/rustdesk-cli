package hbb

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/proto"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func clipboardPipe(t *testing.T, timeout time.Duration, peer func(*Conn) error) *Clipboard {
	t.Helper()
	a, b := net.Pipe()
	c := &Clipboard{conn: &Conn{sc: NewSecureConn(a), peerID: "123456789"}, timeout: timeout, allowed: true, keyboard: true}
	done := make(chan error, 1)
	go func() {
		defer b.Close()
		_ = b.SetDeadline(time.Now().Add(2 * time.Second))
		done <- peer(&Conn{sc: NewSecureConn(b)})
	}()
	t.Cleanup(func() {
		c.Close()
		if err := <-done; err != nil {
			t.Errorf("fake clipboard peer: %v", err)
		}
	})
	return c
}

func clipboardPermission(permission pb.PermissionInfo_Permission, enabled bool) *pb.Message {
	return &pb.Message{Union: &pb.Message_Misc{Misc: &pb.Misc{
		Union: &pb.Misc_PermissionInfo{PermissionInfo: &pb.PermissionInfo{Permission: permission, Enabled: enabled}},
	}}}
}

func TestClipboardDefaultLoginAndExplicitSubscription(t *testing.T) {
	c := clipboardPipe(t, time.Second, func(peer *Conn) error {
		if err := peer.Send(&pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Salt: "salt", Challenge: "challenge"}}}); err != nil {
			return err
		}
		msg, err := peer.Recv()
		if err != nil {
			return err
		}
		lr := msg.GetLoginRequest()
		if lr == nil || lr.GetUnion() != nil || lr.GetVersion() != Version || lr.GetOption().GetDisableClipboard() != pb.OptionMessage_Yes || lr.GetOption().GetDisableAudio() != pb.OptionMessage_Yes || lr.GetOption().GetEnableFileTransfer() != pb.OptionMessage_No {
			return fmt.Errorf("unexpected default-session login: %v", lr)
		}
		if err := peer.Send(&pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: &pb.LoginResponse{
			Union: &pb.LoginResponse_PeerInfo{PeerInfo: &pb.PeerInfo{WindowsSessions: &pb.WindowsSessions{CurrentSid: 7}}},
		}}}); err != nil {
			return err
		}
		msg, err = peer.Recv()
		if err != nil || msg.GetMisc().GetSelectedSid() != 7 {
			return fmt.Errorf("current session confirmation: %v %v", msg, err)
		}
		msg, err = peer.Recv()
		if err != nil || msg.GetMisc().GetCaptureDisplays() == nil || len(msg.GetMisc().GetCaptureDisplays().GetSet()) != 0 {
			return fmt.Errorf("capture unsubscribe: %v %v", msg, err)
		}
		msg, err = peer.Recv()
		if err != nil {
			return err
		}
		opts := msg.GetMisc().GetOption()
		if opts == nil || opts.GetDisableClipboard() != pb.OptionMessage_No || opts.GetDisableKeyboard() != pb.OptionMessage_No {
			return fmt.Errorf("missing clipboard receive subscription: %v", msg)
		}
		ping := &pb.Message{Union: &pb.Message_TestDelay{TestDelay: &pb.TestDelay{Time: 42}}}
		if err := peer.Send(ping); err != nil {
			return err
		}
		msg, err = peer.Recv()
		if err != nil || !proto.Equal(msg, ping) {
			return fmt.Errorf("keepalive reply: %v %v", msg, err)
		}
		return peer.Send(&pb.Message{Union: &pb.Message_MultiClipboards{MultiClipboards: &pb.MultiClipboards{Clipboards: []*pb.Clipboard{
			{Format: pb.ClipboardFormat_Html, Content: []byte("<b>ignored</b>")},
			{Format: pb.ClipboardFormat_Text, Content: []byte("hello\n世界")},
		}}}})
	})
	if err := c.begin(); err != nil {
		t.Fatal(err)
	}
	if err := c.login(); err != nil {
		t.Fatal(err)
	}
	text, err := c.ReadText()
	if err != nil || text != "hello\n世界" {
		t.Fatalf("ReadText = %q, %v", text, err)
	}
}

func TestClipboardPermissionAndCloseErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		msg  *pb.Message
		want string
	}{
		{"clipboard", clipboardPermission(pb.PermissionInfo_Clipboard, false), "clipboard permission denied"},
		{"keyboard", clipboardPermission(pb.PermissionInfo_Keyboard, false), "keyboard permission denied"},
		{"closed", &pb.Message{Union: &pb.Message_Misc{Misc: &pb.Misc{Union: &pb.Misc_CloseReason{CloseReason: "denied"}}}}, "hbb: peer closed clipboard session"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := clipboardPipe(t, time.Second, func(peer *Conn) error {
				if _, err := peer.Recv(); err != nil {
					return err
				}
				return peer.Send(test.msg)
			})
			if _, err := c.ReadText(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
	}
}

func TestClipboardPermissionBeforeLoginResponse(t *testing.T) {
	c := clipboardPipe(t, time.Second, func(peer *Conn) error {
		return peer.Send(clipboardPermission(pb.PermissionInfo_Clipboard, false))
	})
	if err := c.begin(); err != nil {
		t.Fatal(err)
	}
	if err := c.login(); err == nil || !strings.Contains(err.Error(), "clipboard permission denied") {
		t.Fatalf("login permission error = %v", err)
	}
}

func TestClipboardSetRustDesk150WritePredicate(t *testing.T) {
	for _, test := range []struct {
		text     string
		keyboard bool
	}{
		{"", false}, {"explicit\ntext", false}, {"", true}, {"explicit\ntext", true},
	} {
		t.Run(fmt.Sprintf("length%d_keyboard%v", len(test.text), test.keyboard), func(t *testing.T) {
			c := clipboardPipe(t, time.Second, func(peer *Conn) error {
				if err := peer.Send(clipboardAuthHash("challenge")); err != nil {
					return err
				}
				msg, err := peer.Recv()
				if err != nil {
					return err
				}
				lr := msg.GetLoginRequest()
				remote := lr != nil && lr.GetUnion() == nil
				disableClipboard := lr.GetOption().GetDisableClipboard() == pb.OptionMessage_Yes
				disableKeyboard := lr.GetOption().GetDisableKeyboard() == pb.OptionMessage_Yes
				if !remote || !disableClipboard || !disableKeyboard {
					return errors.New("missing privacy options on default login")
				}
				if err := peer.Send(clipboardAuthSuccess()); err != nil {
					return err
				}
				msg, err = peer.Recv()
				if err != nil || msg.GetMisc().GetCaptureDisplays() == nil {
					return errors.New("missing capture unsubscribe after login")
				}
				ping, err := peer.Recv()
				if err != nil || !ping.GetTestDelay().GetFromClient() {
					return fmt.Errorf("missing preflight ping: %v", err)
				}
				if err := peer.Send(clipboardPermission(pb.PermissionInfo_Keyboard, test.keyboard)); err != nil {
					return err
				}
				if err := peer.Send(&pb.Message{Union: &pb.Message_Clipboard{Clipboard: &pb.Clipboard{Content: []byte("unsolicited private text")}}}); err != nil {
					return err
				}
				if err := peer.Send(ping); err != nil {
					return err
				}
				msg, err = peer.Recv()
				if err != nil {
					return err
				}
				option := msg.GetMisc().GetOption()
				if option == nil || option.GetDisableClipboard() != pb.OptionMessage_No || option.GetDisableKeyboard() != pb.OptionMessage_Yes {
					return errors.New("write must enable clipboard and disable keyboard before text submission")
				}
				disableClipboard = option.GetDisableClipboard() == pb.OptionMessage_Yes
				disableKeyboard = option.GetDisableKeyboard() == pb.OptionMessage_Yes
				clipboardPermission := true
				clipboardEnabled := clipboardPermission && !disableClipboard
				canSubscribe := clipboardEnabled && test.keyboard && !disableKeyboard
				if canSubscribe {
					return errors.New("write enabled incoming clipboard subscription")
				}
				msg, err = peer.Recv()
				clip := msg.GetClipboard()
				if !remote || !clipboardEnabled {
					return errors.New("RustDesk 1.5.0 would ignore clipboard submission")
				}
				if err != nil || clip == nil || string(clip.GetContent()) != test.text || clip.GetCompress() || clip.GetFormat() != pb.ClipboardFormat_Text {
					return fmt.Errorf("clipboard write: %v %v", msg, err)
				}
				ping, err = peer.Recv()
				if err != nil || !ping.GetTestDelay().GetFromClient() {
					return fmt.Errorf("missing post-write ping: %v", err)
				}
				return peer.Send(ping)
			})
			if err := c.begin(); err != nil {
				t.Fatal(err)
			}
			if err := c.login(); err != nil {
				t.Fatal(err)
			}
			if err := c.WriteText(test.text); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClipboardSetRefusesQueuedDenial(t *testing.T) {
	c := clipboardPipe(t, time.Second, func(peer *Conn) error {
		if _, err := peer.Recv(); err != nil {
			return err
		}
		if err := peer.Send(clipboardPermission(pb.PermissionInfo_Clipboard, false)); err != nil {
			return err
		}
		if _, err := peer.Recv(); err == nil {
			return errors.New("clipboard option or text sent after permission denial")
		}
		return nil
	})
	err := c.WriteText("must not be sent")
	c.Close()
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("WriteText = %v", err)
	}
}

func TestClipboardReadTimeout(t *testing.T) {
	c := clipboardPipe(t, 20*time.Millisecond, func(peer *Conn) error {
		if _, err := peer.Recv(); err != nil {
			return err
		}
		_, err := peer.Recv()
		if err == nil {
			return errors.New("unexpected clipboard request")
		}
		return nil
	})
	start := time.Now()
	if _, err := c.ReadText(); err == nil || !strings.Contains(err.Error(), "no remote clipboard text update received") {
		t.Fatalf("ReadText = %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("clipboard wait exceeded deadline")
	}
}

func TestClipboardDeadlineWithEndlessUnrelatedMessages(t *testing.T) {
	for _, operation := range []string{"get", "set"} {
		t.Run(operation, func(t *testing.T) {
			c := clipboardPipe(t, 20*time.Millisecond, func(peer *Conn) error {
				if _, err := peer.Recv(); err != nil {
					return err
				}
				msg := &pb.Message{Union: &pb.Message_VideoFrame{VideoFrame: &pb.VideoFrame{}}}
				for {
					if err := peer.Send(msg); err != nil {
						return nil
					}
				}
			})
			start := time.Now()
			var err error
			if operation == "get" {
				_, err = c.ReadText()
			} else {
				err = c.WriteText("explicit")
			}
			if err == nil {
				t.Fatal("expected deadline error")
			}
			if time.Since(start) > time.Second {
				t.Fatal("unrelated messages extended the operation deadline")
			}
		})
	}
}

func TestClipboardPostSubmissionFailureIsUnconfirmed(t *testing.T) {
	c := clipboardPipe(t, time.Second, func(peer *Conn) error {
		ping, err := peer.Recv()
		if err != nil {
			return err
		}
		if err := peer.Send(ping); err != nil {
			return err
		}
		msg, err := peer.Recv()
		option := msg.GetMisc().GetOption()
		if err != nil || option == nil || option.GetDisableClipboard() != pb.OptionMessage_No || option.GetDisableKeyboard() != pb.OptionMessage_Yes {
			return errors.New("missing clipboard write options")
		}
		msg, err = peer.Recv()
		if err != nil || msg.GetClipboard() == nil {
			return fmt.Errorf("expected clipboard submission: %v", err)
		}
		return nil
	})
	if err := c.WriteText("explicit"); err == nil || !strings.Contains(err.Error(), "submitted") || !strings.Contains(err.Error(), "unconfirmed") {
		t.Fatalf("WriteText = %v", err)
	}
}

func TestClipboardDecodeLimitsAndFormats(t *testing.T) {
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	for _, test := range []struct {
		name string
		data []byte
		zip  bool
		fail bool
	}{
		{"utf8", []byte("你好\n"), false, false},
		{"compressed", enc.EncodeAll([]byte("你好\n"), nil), true, false},
		{"boundary", bytes.Repeat([]byte("a"), MaxClipboardTextBytes), false, false},
		{"too large", bytes.Repeat([]byte("a"), MaxClipboardTextBytes+1), false, true},
		{"bomb", enc.EncodeAll(bytes.Repeat([]byte("a"), MaxClipboardTextBytes+1), nil), true, true},
		{"bad compression", []byte("not zstd"), true, true},
		{"bad utf8", []byte{0xff}, false, true},
		{"compressed bad utf8", enc.EncodeAll([]byte{0xff}, nil), true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			text, err := decodeClipboardText(&pb.Clipboard{Content: test.data, Compress: test.zip})
			if (err != nil) != test.fail {
				t.Fatalf("decode error = %v, expected failure %v", err, test.fail)
			}
			if !test.fail && len(text) == 0 {
				t.Fatal("lost clipboard contents")
			}
		})
	}
}

func TestClipboardRejectsOversizedFrameHeader(t *testing.T) {
	c := clipboardPipe(t, time.Second, func(peer *Conn) error {
		if _, err := peer.Recv(); err != nil {
			return err
		}
		_, err := peer.sc.conn.Write([]byte{0xff, 0xff, 0xff, 0xff})
		return err
	})
	if _, err := c.ReadText(); err == nil || !strings.Contains(err.Error(), "frame exceeds") {
		t.Fatalf("ReadText = %v", err)
	}
}

func TestClipboardMultiTextValidation(t *testing.T) {
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	for _, test := range []struct {
		name string
		data []byte
		zip  bool
		want string
		fail bool
	}{
		{"format zero", []byte("text\n世界"), false, "text\n世界", false},
		{"compressed", enc.EncodeAll([]byte("text\n世界"), nil), true, "text\n世界", false},
		{"invalid utf8", []byte{0xff}, false, "", true},
		{"compressed invalid utf8", enc.EncodeAll([]byte{0xff}, nil), true, "", true},
		{"wire limit", bytes.Repeat([]byte("a"), MaxClipboardTextBytes+1), false, "", true},
		{"decompressed limit", enc.EncodeAll(bytes.Repeat([]byte("a"), MaxClipboardTextBytes+1), nil), true, "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := clipboardPipe(t, time.Second, func(peer *Conn) error {
				if _, err := peer.Recv(); err != nil {
					return err
				}
				return peer.Send(&pb.Message{Union: &pb.Message_MultiClipboards{MultiClipboards: &pb.MultiClipboards{Clipboards: []*pb.Clipboard{
					{Format: pb.ClipboardFormat_Html, Content: []byte("ignored")},
					{Format: 0, Content: test.data, Compress: test.zip},
				}}}})
			})
			got, err := c.ReadText()
			if (err != nil) != test.fail || (!test.fail && got != test.want) {
				t.Fatalf("ReadText = %q, %v", got, err)
			}
		})
	}
}

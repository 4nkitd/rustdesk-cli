package hbb

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

const MaxClipboardTextBytes = 1 << 20
const clipboardMaxFrameBytes = 4 << 20

type Clipboard struct {
	conn     *Conn
	timeout  time.Duration
	deadline time.Time
	allowed  bool
	keyboard bool
}

func NewClipboard(opts DialOpts) (*Clipboard, error) {
	opts.ConnType = pb.ConnType_DEFAULT_CONN
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	c, err := Connect(opts)
	if err != nil {
		return nil, err
	}
	clip := &Clipboard{conn: c, timeout: opts.Timeout, allowed: true, keyboard: true}
	if err := clip.begin(); err != nil {
		c.Close()
		return nil, err
	}
	if err := clip.login(); err != nil {
		c.Close()
		return nil, fmt.Errorf("clipboard login: %w", err)
	}
	return clip, nil
}

func (c *Clipboard) Close() error { return c.conn.Close() }

func (c *Clipboard) begin() error {
	c.deadline = time.Now().Add(c.timeout)
	return c.conn.sc.SetDeadline(c.deadline)
}

func (c *Clipboard) login() error {
	defer func() { c.conn.opts.OTP = "" }()
	loginSent := false
	for received := 0; received < 128; received++ {
		msg, err := c.recv()
		if err != nil {
			return err
		}
		if lr := msg.GetLoginResponse(); lr != nil {
			if !loginSent {
				return errors.New("hbb: login response before login challenge")
			}
			if message := lr.GetError(); message != "" {
				if err := c.conn.HandleLoginError(message); err != nil {
					return err
				}
				continue
			}
		}
		if err := c.control(msg); err != nil {
			return err
		}
		switch u := msg.GetUnion().(type) {
		case *pb.Message_Hash:
			if loginSent || u.Hash.GetChallenge() == "" {
				return errors.New("hbb: missing or repeated login challenge")
			}
			lr := &pb.LoginRequest{
				Username: c.conn.peerID, MyId: randomPeerID(), MyName: "rdcli",
				MyPlatform: "Linux", Version: Version,
				Option: &pb.OptionMessage{
					DisableClipboard: pb.OptionMessage_Yes, DisableKeyboard: pb.OptionMessage_Yes,
					DisableAudio: pb.OptionMessage_Yes, EnableFileTransfer: pb.OptionMessage_No,
				},
			}
			if c.conn.opts.Password != "" {
				lr.Password = hashPassword(c.conn.opts.Password, u.Hash.GetSalt(), u.Hash.GetChallenge())
			}
			if err := c.conn.Send(&pb.Message{Union: &pb.Message_LoginRequest{LoginRequest: lr}}); err != nil {
				return err
			}
			loginSent = true
		case *pb.Message_Auth_2Fa:
			return errors.New("hbb: unexpected peer 2FA message")
		case *pb.Message_LoginResponse:
			pi := u.LoginResponse.GetPeerInfo()
			if pi == nil {
				return errors.New("empty peer info in login response")
			}
			c.conn.peerInfo = pi
			if sessions := pi.GetWindowsSessions(); sessions != nil {
				if err := c.conn.Send(&pb.Message{Union: &pb.Message_Misc{Misc: &pb.Misc{
					Union: &pb.Misc_SelectedSid{SelectedSid: sessions.GetCurrentSid()},
				}}}); err != nil {
					return err
				}
			}
			return c.conn.Send(&pb.Message{Union: &pb.Message_Misc{Misc: &pb.Misc{
				Union: &pb.Misc_CaptureDisplays{CaptureDisplays: &pb.CaptureDisplays{}},
			}}})
		}
	}
	return errors.New("hbb: too many messages before clipboard login completed")
}

// RustDesk 1.4.1 clipboard_service.rs subscribes without a snapshot. There is
// no text-read request; this returns the next broadcast, which may never arrive.
func (c *Clipboard) ReadText() (string, error) {
	if !c.allowed {
		return "", errors.New("remote clipboard permission denied")
	}
	if !c.keyboard {
		return "", errors.New("remote keyboard permission denied; RustDesk requires it for clipboard receive")
	}
	if err := c.begin(); err != nil {
		return "", err
	}
	if err := c.conn.Send(&pb.Message{Union: &pb.Message_Misc{Misc: &pb.Misc{
		Union: &pb.Misc_Option{Option: &pb.OptionMessage{
			DisableClipboard: pb.OptionMessage_No, DisableKeyboard: pb.OptionMessage_No,
		}},
	}}}); err != nil {
		return "", err
	}
	for {
		msg, err := c.recv()
		if err != nil {
			return "", fmt.Errorf("no remote clipboard text update received (RustDesk publishes changes; no snapshot is guaranteed, and one-way policy can suppress updates): %w", err)
		}
		if err := c.control(msg); err != nil {
			return "", err
		}
		if !c.keyboard {
			return "", errors.New("remote keyboard permission denied; RustDesk requires it for clipboard receive")
		}
		var clips []*pb.Clipboard
		if clip := msg.GetClipboard(); clip != nil {
			clips = []*pb.Clipboard{clip}
		} else if multi := msg.GetMultiClipboards(); multi != nil {
			clips = multi.GetClipboards()
		}
		for _, clip := range clips {
			if clip.GetFormat() == pb.ClipboardFormat_Text {
				return decodeClipboardText(clip)
			}
		}
	}
}

// RustDesk 1.5.0 requires clipboard enabled for writes. Keeping keyboard disabled
// prevents clipboard subscriptions. OS clipboard writes have no acknowledgement.
func (c *Clipboard) WriteText(text string) error {
	if err := ValidateClipboardText(text); err != nil {
		return err
	}
	if !c.allowed {
		return errors.New("remote clipboard permission denied")
	}
	if err := c.begin(); err != nil {
		return err
	}
	if err := c.barrier(); err != nil {
		return err
	}
	if err := c.conn.Send(&pb.Message{Union: &pb.Message_Misc{Misc: &pb.Misc{
		Union: &pb.Misc_Option{Option: &pb.OptionMessage{
			DisableClipboard: pb.OptionMessage_No, DisableKeyboard: pb.OptionMessage_Yes,
		}},
	}}}); err != nil {
		return err
	}
	if err := c.conn.Send(&pb.Message{Union: &pb.Message_Clipboard{Clipboard: &pb.Clipboard{
		Content: []byte(text), Format: pb.ClipboardFormat_Text,
	}}}); err != nil {
		return err
	}
	if err := c.barrier(); err != nil {
		return fmt.Errorf("clipboard message submitted, but session check failed; application unconfirmed: %w", err)
	}
	return nil
}

func (c *Clipboard) barrier() error {
	stamp := time.Now().UnixNano()
	if err := c.conn.Send(&pb.Message{Union: &pb.Message_TestDelay{TestDelay: &pb.TestDelay{
		Time: stamp, FromClient: true,
	}}}); err != nil {
		return err
	}
	for {
		msg, err := c.recv()
		if err != nil {
			return err
		}
		if err := c.control(msg); err != nil {
			return err
		}
		if td := msg.GetTestDelay(); td != nil && td.GetFromClient() && td.GetTime() == stamp {
			return nil
		}
	}
}

func (c *Clipboard) control(msg *pb.Message) error {
	if td := msg.GetTestDelay(); td != nil && !td.GetFromClient() {
		return c.conn.Send(msg)
	}
	if lr := msg.GetLoginResponse(); lr != nil && lr.GetError() != "" {
		return errors.New("hbb: peer rejected clipboard session")
	}
	if misc := msg.GetMisc(); misc != nil {
		if _, ok := misc.GetUnion().(*pb.Misc_CloseReason); ok {
			return errors.New("hbb: peer closed clipboard session")
		}
		if p := misc.GetPermissionInfo(); p != nil {
			switch p.GetPermission() {
			case pb.PermissionInfo_Clipboard:
				c.allowed = p.GetEnabled()
			case pb.PermissionInfo_Keyboard:
				c.keyboard = p.GetEnabled()
			}
		}
	}
	if !c.allowed {
		return errors.New("remote clipboard permission denied")
	}
	return nil
}

func (c *Clipboard) recv() (*pb.Message, error) {
	sc := c.conn.sc
	for {
		if !time.Now().Before(c.deadline) {
			return nil, errors.New("clipboard operation timed out")
		}
		if len(sc.readBuf) > 0 {
			headerLen := int(sc.readBuf[0]&3) + 1
			if len(sc.readBuf) >= headerLen {
				var header uint32
				for i := 0; i < headerLen; i++ {
					header |= uint32(sc.readBuf[i]) << (8 * i)
				}
				if header>>2 > clipboardMaxFrameBytes {
					return nil, errors.New("clipboard session frame exceeds 4 MiB limit")
				}
			}
		}
		if frame, ok := DecodeFrame(&sc.readBuf); ok {
			if sc.mode == modeEncrypted && len(frame) > 1 {
				var err error
				frame, err = sc.encryptor.Decrypt(frame)
				if err != nil {
					return nil, err
				}
			}
			msg := &pb.Message{}
			if err := decodePeerMessage(frame, msg); err != nil {
				return nil, fmt.Errorf("invalid clipboard session message: %w", err)
			}
			return msg, nil
		}
		var buf [4096]byte
		n, err := sc.conn.Read(buf[:])
		if n > 0 {
			sc.readBuf = append(sc.readBuf, buf[:n]...)
			continue
		}
		if err != nil {
			return nil, err
		}
	}
}

func ValidateClipboardText(text string) error {
	if len(text) > MaxClipboardTextBytes {
		return errors.New("clipboard text exceeds 1 MiB limit")
	}
	if !utf8.ValidString(text) {
		return errors.New("clipboard text must be valid UTF-8")
	}
	return nil
}

func decodeClipboardText(clip *pb.Clipboard) (string, error) {
	data := clip.GetContent()
	if len(data) > MaxClipboardTextBytes {
		return "", errors.New("clipboard content exceeds 1 MiB limit")
	}
	if clip.GetCompress() {
		dec, err := zstd.NewReader(bytes.NewReader(data), zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderMaxMemory(8<<20), zstd.WithDecoderMaxWindow(8<<20))
		if err != nil {
			return "", fmt.Errorf("clipboard decompression: %w", err)
		}
		defer dec.Close()
		data, err = io.ReadAll(io.LimitReader(dec, MaxClipboardTextBytes+1))
		if err != nil {
			return "", fmt.Errorf("clipboard decompression: %w", err)
		}
	}
	text := string(data)
	if err := ValidateClipboardText(text); err != nil {
		return "", err
	}
	return text, nil
}

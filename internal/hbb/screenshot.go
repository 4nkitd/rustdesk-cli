package hbb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"time"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

const (
	MaxScreenshotBytes       = 64 << 20
	maxScreenshotFrame       = MaxScreenshotBytes + (64 << 10)
	maxScreenshotPixels      = 32 << 20
	maxScreenshotDimension   = 16 << 10
	maxScreenshotDecodeBytes = 256 << 20
)

type Screenshot struct {
	Data        []byte `json:"-"`
	Width       int
	Height      int
	PeerVersion string
}

func TakeScreenshot(opts DialOpts, display int32) (*Screenshot, error) {
	if display < 0 {
		return nil, errors.New("screenshot: display must be non-negative")
	}
	opts.ConnType = pb.ConnType_DEFAULT_CONN
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()
	type connectionResult struct {
		conn *Conn
		err  error
	}
	connected := make(chan connectionResult)
	go func() {
		conn, err := Connect(opts)
		select {
		case connected <- connectionResult{conn, err}:
		case <-ctx.Done():
			if conn != nil {
				conn.Close()
			}
		}
	}()
	var conn *Conn
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("screenshot: connect: %w", ctx.Err())
	case result := <-connected:
		if result.err != nil {
			return nil, fmt.Errorf("screenshot: connect: %w", result.err)
		}
		conn = result.conn
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil, context.DeadlineExceeded
	}
	return conn.takeScreenshot(display, remaining)
}

func (c *Conn) takeScreenshot(display int32, timeout time.Duration) (*Screenshot, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	deadline := time.Now().Add(timeout)
	if err := c.sc.SetDeadline(deadline); err != nil {
		return nil, err
	}
	// Bound encrypted frame lengths before SecureConn allocates the login or image payload.
	c.sc.conn = &screenshotFrameConn{
		Conn:     c.sc.conn,
		reader:   io.MultiReader(bytes.NewReader(c.sc.readBuf), c.sc.conn),
		deadline: deadline,
	}
	c.sc.readBuf = nil
	pi, err := c.Login(LoginDefault, LoginSpec{Option: &pb.OptionMessage{
		DisableAudio:        pb.OptionMessage_Yes,
		DisableClipboard:    pb.OptionMessage_Yes,
		DisableCamera:       pb.OptionMessage_Yes,
		DisableKeyboard:     pb.OptionMessage_Yes,
		EnableFileTransfer:  pb.OptionMessage_No,
		ShowRemoteCursor:    pb.OptionMessage_No,
		ShowMyCursor:        pb.OptionMessage_No,
		FollowRemoteCursor:  pb.OptionMessage_No,
		FollowRemoteWindow:  pb.OptionMessage_No,
		LockAfterSessionEnd: pb.OptionMessage_No,
	}})
	if err != nil {
		return nil, fmt.Errorf("screenshot: login: %w", err)
	}
	if !versionAtLeast(pi.GetVersion(), "1.4.0") {
		return nil, errors.New("screenshot: peer version is unsupported; need RustDesk 1.4.0 or later")
	}
	if display < 0 || int64(display) >= int64(len(pi.GetDisplays())) {
		return nil, fmt.Errorf("screenshot: display %d unavailable; peer reports %d displays", display, len(pi.GetDisplays()))
	}
	// ScreenshotRequest only refreshes an existing capturer; secondary displays need a subscription first.
	if err := c.Send(&pb.Message{Union: &pb.Message_Misc{Misc: &pb.Misc{
		Union: &pb.Misc_CaptureDisplays{CaptureDisplays: &pb.CaptureDisplays{Set: []int32{display}}},
	}}}); err != nil {
		return nil, fmt.Errorf("screenshot: subscribe display: %w", err)
	}
	sid := newUUID()
	if err := c.Send(&pb.Message{Union: &pb.Message_ScreenshotRequest{
		ScreenshotRequest: &pb.ScreenshotRequest{Display: display, Sid: sid},
	}}); err != nil {
		return nil, err
	}
	for {
		msg, err := c.Recv()
		if err != nil {
			return nil, fmt.Errorf("screenshot: receive: %w", err)
		}
		if td := msg.GetTestDelay(); td != nil && !td.GetFromClient() {
			if err := c.Send(&pb.Message{Union: &pb.Message_TestDelay{TestDelay: td}}); err != nil {
				return nil, err
			}
		}
		if misc := msg.GetMisc(); misc != nil {
			if reason, ok := misc.GetUnion().(*pb.Misc_CloseReason); ok {
				return nil, fmt.Errorf("screenshot: peer closed: %s", c.PeerText(reason.CloseReason))
			}
		}
		response := msg.GetScreenshotResponse()
		if response == nil || response.GetSid() != sid {
			continue
		}
		if response.GetMsg() != "" {
			return nil, fmt.Errorf("screenshot: peer: %s", c.PeerText(response.GetMsg()))
		}
		result, err := validateScreenshotPNG(response.GetData())
		if err != nil {
			return nil, c.PeerError(err.Error())
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("screenshot: %w", context.DeadlineExceeded)
		}
		result.PeerVersion = c.PeerText(pi.GetVersion())
		return result, nil
	}
}

func validateScreenshotPNG(data []byte) (*Screenshot, error) {
	if len(data) == 0 || len(data) > MaxScreenshotBytes {
		return nil, fmt.Errorf("screenshot: PNG size %d is outside 1..%d bytes", len(data), MaxScreenshotBytes)
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("screenshot: invalid PNG: %w", err)
	}
	if err := checkScreenshotPNGResources(config, data); err != nil {
		return nil, err
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		return nil, fmt.Errorf("screenshot: invalid PNG: %w", err)
	}
	return &Screenshot{Data: data, Width: config.Width, Height: config.Height}, nil
}

func checkScreenshotPNGResources(config image.Config, data []byte) error {
	width, height := int64(config.Width), int64(config.Height)
	if width <= 0 || height <= 0 || width*height > maxScreenshotPixels {
		return fmt.Errorf("screenshot: PNG dimensions %dx%d exceed pixel limit", width, height)
	}
	if width > maxScreenshotDimension || height > maxScreenshotDimension {
		return fmt.Errorf("screenshot: PNG dimensions %dx%d exceed dimension limit %d", width, height, maxScreenshotDimension)
	}
	// DecodeConfig validated IHDR. Its color model omits possible tRNS expansion, so reserve RGBA storage for non-paletted images.
	depth, colorType, interlace := int64(data[24]), data[25], data[28]
	channels, pixelBytes := int64(1), int64(4)
	if depth == 16 {
		pixelBytes = 8
	}
	switch colorType {
	case 2:
		channels = 3
	case 3:
		pixelBytes = 1
	case 4:
		channels = 2
	case 6:
		channels = 4
	}
	rowBytes := 1 + (width*channels*depth+7)/8
	imageBytes := width * height * pixelBytes
	estimate := imageBytes + 2*rowBytes
	if interlace == 1 {
		// Count all Adam7 pass images and row buffers, without relying on garbage collection between passes.
		estimate = 2*imageBytes + 7*2*rowBytes
	}
	estimate += 1 << 20 // zlib workspace, palette, decoder state, and allocation rounding
	if estimate > maxScreenshotDecodeBytes {
		return fmt.Errorf("screenshot: PNG estimated decode memory %d exceeds %d bytes", estimate, maxScreenshotDecodeBytes)
	}
	return nil
}

type screenshotFrameConn struct {
	net.Conn
	reader    io.Reader
	header    []byte
	remaining int
	deadline  time.Time
}

func (c *screenshotFrameConn) SetDeadline(t time.Time) error {
	if t.IsZero() || t.After(c.deadline) {
		t = c.deadline
	}
	return c.Conn.SetDeadline(t)
}

func (c *screenshotFrameConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(c.header) == 0 && c.remaining == 0 {
		var header [4]byte
		if _, err := io.ReadFull(c.reader, header[:1]); err != nil {
			return 0, err
		}
		n := int(header[0]&3) + 1
		if _, err := io.ReadFull(c.reader, header[1:n]); err != nil {
			return 0, err
		}
		var size uint32
		for i := 0; i < n; i++ {
			size |= uint32(header[i]) << (8 * i)
		}
		size >>= 2
		if size > maxScreenshotFrame {
			return 0, fmt.Errorf("screenshot: frame size %d exceeds %d bytes", size, maxScreenshotFrame)
		}
		c.remaining = int(size)
		c.header = header[:n]
	}
	if len(c.header) > 0 {
		n := copy(p, c.header)
		c.header = c.header[n:]
		return n, nil
	}
	if len(p) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.reader.Read(p)
	c.remaining -= n
	return n, err
}

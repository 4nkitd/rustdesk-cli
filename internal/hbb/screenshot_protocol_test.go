package hbb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"net"
	"strings"
	"testing"
	"time"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func screenshotTestPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func screenshotTestInfo(version string) *pb.PeerInfo {
	return &pb.PeerInfo{Version: version, Displays: []*pb.DisplayInfo{{Width: 3, Height: 2}, {Width: 3, Height: 2}}}
}

func screenshotTestLogin(sc *SecureConn, pi *pb.PeerInfo) error {
	if err := writeProtoMessage(sc, &pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Salt: "salt", Challenge: "challenge"}}}); err != nil {
		return err
	}
	msg, err := readProtoMessage(sc)
	if err != nil {
		return err
	}
	lr := msg.GetLoginRequest()
	if lr == nil || lr.GetUnion() != nil || !bytes.Equal(lr.GetPassword(), hashPassword("secret", "salt", "challenge")) {
		return errors.New("expected authenticated LoginDefault")
	}
	options := lr.GetOption()
	if options.GetDisableAudio() != pb.OptionMessage_Yes || options.GetDisableClipboard() != pb.OptionMessage_Yes ||
		options.GetDisableCamera() != pb.OptionMessage_Yes || options.GetDisableKeyboard() != pb.OptionMessage_Yes ||
		options.GetEnableFileTransfer() != pb.OptionMessage_No {
		return errors.New("screenshot login must disable unrelated services before activation")
	}
	return writeProtoMessage(sc, &pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: &pb.LoginResponse{
		Union: &pb.LoginResponse_PeerInfo{PeerInfo: pi},
	}}})
}

func screenshotTestPipe(t *testing.T, pi *pb.PeerInfo, handler func(*SecureConn) error) (*Conn, <-chan error) {
	t.Helper()
	client, server := net.Pipe()
	clientSC, serverSC := NewSecureConn(client), NewSecureConn(server)
	key := [32]byte{1, 2, 3, 4}
	clientSC.SetKey(key)
	serverSC.SetKey(key)
	_ = server.SetDeadline(time.Now().Add(3 * time.Second))
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		if err := screenshotTestLogin(serverSC, pi); err != nil {
			done <- err
			return
		}
		done <- handler(serverSC)
	}()
	t.Cleanup(func() { client.Close(); server.Close() })
	return &Conn{sc: clientSC, peerID: "mock", opts: DialOpts{Password: "secret"}}, done
}

func screenshotTestRequest(sc *SecureConn) (*pb.ScreenshotRequest, error) {
	msg, err := readProtoMessage(sc)
	if err != nil {
		return nil, err
	}
	capture := msg.GetMisc().GetCaptureDisplays()
	if capture == nil || len(capture.GetSet()) != 1 || len(capture.GetAdd()) != 0 || len(capture.GetSub()) != 0 {
		return nil, errors.New("expected subscription to exactly one display before screenshot request")
	}
	msg, err = readProtoMessage(sc)
	if err != nil {
		return nil, err
	}
	req := msg.GetScreenshotRequest()
	if req == nil || req.GetSid() == "" {
		return nil, errors.New("missing screenshot request/session ID")
	}
	if req.GetDisplay() != capture.GetSet()[0] {
		return nil, errors.New("screenshot display does not match active subscription")
	}
	return req, nil
}

func screenshotTestResponse(sc *SecureConn, sid, message string, data []byte) error {
	return writeProtoMessage(sc, &pb.Message{Union: &pb.Message_ScreenshotResponse{
		ScreenshotResponse: &pb.ScreenshotResponse{Sid: sid, Msg: message, Data: data},
	}})
}

func TestScreenshotEncryptedSession(t *testing.T) {
	data := screenshotTestPNG(t)
	conn, done := screenshotTestPipe(t, screenshotTestInfo("1.4.0"), func(sc *SecureConn) error {
		req, err := screenshotTestRequest(sc)
		if err != nil {
			return err
		}
		if req.GetDisplay() != 1 {
			return fmt.Errorf("display = %d", req.GetDisplay())
		}
		if err := screenshotTestResponse(sc, "other-session", "wrong session error", data); err != nil {
			return err
		}
		if err := writeProtoMessage(sc, &pb.Message{Union: &pb.Message_TestDelay{TestDelay: &pb.TestDelay{Time: 123}}}); err != nil {
			return err
		}
		echo, err := readProtoMessage(sc)
		if err != nil {
			return err
		}
		if echo.GetTestDelay() == nil || echo.GetTestDelay().GetTime() != 123 {
			return errors.New("missing TestDelay echo")
		}
		if err := writeProtoMessage(sc, &pb.Message{Union: &pb.Message_TestDelay{TestDelay: &pb.TestDelay{FromClient: true}}}); err != nil {
			return err
		}
		return screenshotTestResponse(sc, req.GetSid(), "", data)
	})
	shot, err := conn.takeScreenshot(1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if shot.Width != 3 || shot.Height != 2 || shot.PeerVersion != "1.4.0" || !bytes.Equal(shot.Data, data) {
		t.Fatalf("unexpected screenshot: %+v", shot)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestScreenshotPeerVersionRedaction(t *testing.T) {
	data := screenshotTestPNG(t)
	conn, done := screenshotTestPipe(t, screenshotTestInfo("1.4.1 secret"), func(sc *SecureConn) error {
		req, err := screenshotTestRequest(sc)
		if err != nil {
			return err
		}
		return screenshotTestResponse(sc, req.GetSid(), "", data)
	})
	shot, err := conn.takeScreenshot(0, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(shot.PeerVersion, "secret") || !strings.Contains(shot.PeerVersion, "1.4.1") {
		t.Fatal("peer version metadata must preserve benign text and redact credentials")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestScreenshotVersionAndDisplayGates(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		display       int32
		want          string
	}{
		{"old", "1.3.9", 0, "need RustDesk 1.4.0"},
		{"unknown", "", 0, "need RustDesk 1.4.0"},
		{"display", "1.4.1", 2, "unavailable"},
		{"negative", "1.4.1", -1, "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, done := screenshotTestPipe(t, screenshotTestInfo(tc.version), func(sc *SecureConn) error {
				_, err := sc.ReadMessage()
				if err == nil {
					return errors.New("unexpected screenshot request")
				}
				return nil
			})
			_, err := conn.takeScreenshot(tc.display, time.Second)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v", err)
			}
			conn.Close()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestScreenshotActivatesSelectedDisplayIndex(t *testing.T) {
	data := screenshotTestPNG(t)
	for _, version := range []string{"1.4.0", "1.4.1"} {
		for _, display := range []int32{0, 1} {
			t.Run(fmt.Sprintf("%s/display=%d", version, display), func(t *testing.T) {
				pi := screenshotTestInfo(version)
				pi.CurrentDisplay = 1 - display
				pi.Displays[0].Name = "DISPLAY17"
				pi.Displays[1].Name = "DISPLAY42"
				conn, done := screenshotTestPipe(t, pi, func(sc *SecureConn) error {
					req, err := screenshotTestRequest(sc)
					if err != nil {
						return err
					}
					if req.GetDisplay() != display {
						return fmt.Errorf("capturing display %d, want index %d", req.GetDisplay(), display)
					}
					return screenshotTestResponse(sc, req.GetSid(), "", data)
				})
				shot, err := conn.takeScreenshot(display, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(shot.Data, data) {
					t.Fatal("screenshot data mismatch")
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestScreenshotResponseFailures(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		data          []byte
		want          string
	}{
		{"peer error", "capture permission denied", nil, "capture permission denied"},
		{"empty", "", nil, "PNG size"},
		{"invalid", "", []byte("not a png"), "invalid PNG"},
		{"truncated", "", screenshotTestPNG(t)[:50], "invalid PNG"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, done := screenshotTestPipe(t, screenshotTestInfo("1.4.1"), func(sc *SecureConn) error {
				req, err := screenshotTestRequest(sc)
				if err != nil {
					return err
				}
				return screenshotTestResponse(sc, req.GetSid(), tc.message, tc.data)
			})
			_, err := conn.takeScreenshot(0, time.Second)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v", err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestScreenshotDeadlineDespiteUnrelatedResponses(t *testing.T) {
	conn, done := screenshotTestPipe(t, screenshotTestInfo("1.4.1"), func(sc *SecureConn) error {
		if _, err := screenshotTestRequest(sc); err != nil {
			return err
		}
		for {
			if err := screenshotTestResponse(sc, "stale", "", nil); err != nil {
				return nil
			}
		}
	})
	_, err := conn.takeScreenshot(0, 50*time.Millisecond)
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected timeout, got %v", err)
	}
	conn.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestScreenshotRejectsOversizedFrameBeforePayload(t *testing.T) {
	for _, login := range []bool{false, true} {
		t.Run(fmt.Sprintf("login=%v", login), func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			cs, ss := NewSecureConn(client), NewSecureConn(server)
			cs.SetKey([32]byte{9})
			ss.SetKey([32]byte{9})
			done := make(chan error, 1)
			go func() {
				if login {
					if err := screenshotTestLogin(ss, screenshotTestInfo("1.4.1")); err != nil {
						done <- err
						return
					}
					if _, err := screenshotTestRequest(ss); err != nil {
						done <- err
						return
					}
				}
				var header [4]byte
				binary.LittleEndian.PutUint32(header[:], uint32(maxScreenshotFrame+1)<<2|3)
				_, err := server.Write(header[:])
				done <- err
			}()
			conn := &Conn{sc: cs, opts: DialOpts{Password: "secret"}}
			_, err := conn.takeScreenshot(0, time.Second)
			if err == nil || !strings.Contains(err.Error(), "frame size") {
				t.Fatalf("error = %v", err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestScreenshotPixelAndByteLimits(t *testing.T) {
	data := screenshotTestPNG(t)
	binary.BigEndian.PutUint32(data[16:20], 1<<20)
	binary.BigEndian.PutUint32(data[20:24], 1<<20)
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	if _, err := validateScreenshotPNG(data); err == nil || !strings.Contains(err.Error(), "pixel limit") {
		t.Fatalf("error = %v", err)
	}
	if _, err := validateScreenshotPNG(make([]byte, MaxScreenshotBytes+1)); err == nil || !strings.Contains(err.Error(), "PNG size") {
		t.Fatalf("error = %v", err)
	}
}

func screenshotTestPNGHeader(t *testing.T, width, height uint32, depth, colorType, interlace byte) []byte {
	t.Helper()
	data := screenshotTestPNG(t)[:50]
	binary.BigEndian.PutUint32(data[16:20], width)
	binary.BigEndian.PutUint32(data[20:24], height)
	data[24], data[25], data[28] = depth, colorType, interlace
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	return data
}

func TestScreenshotRejectsExtremeDimensionsBeforeDecode(t *testing.T) {
	for _, size := range [][2]uint32{{1 << 16, 1}, {1, 1 << 16}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			data := screenshotTestPNGHeader(t, size[0], size[1], 16, 6, 0)
			if _, err := png.DecodeConfig(bytes.NewReader(data)); err != nil {
				t.Fatalf("invalid test header: %v", err)
			}
			if _, err := validateScreenshotPNG(data); err == nil || !strings.Contains(err.Error(), "dimension limit") {
				t.Fatalf("expected dimension rejection before truncated IDAT decoding, got %v", err)
			}
		})
	}
}

func TestScreenshotPNGDecodeMemoryBudget(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		width, height               uint32
		depth, colorType, interlace byte
		allowed                     bool
	}{
		{"4K RGBA16", 3840, 2160, 16, 6, 0, true},
		{"4K interlaced RGBA16", 3840, 2160, 16, 6, 1, true},
		{"8K RGBA8", 7680, 4320, 8, 6, 0, true},
		{"8K RGBA16", 7680, 4320, 16, 6, 0, true},
		{"8K interlaced RGBA8", 7680, 4320, 8, 6, 1, true},
		{"8K interlaced RGBA16 over budget", 7680, 4320, 16, 6, 1, false},
		{"RGBA16 scanlines fit", 16384, 2037, 16, 6, 0, true},
		{"RGBA16 both scanlines exceed budget", 16384, 2038, 16, 6, 0, false},
		{"RGB16 narrower scanlines fit", 16384, 2038, 16, 2, 0, true},
		{"RGBA8 smaller storage fits", 16384, 2048, 8, 6, 0, true},
		{"RGB16 expands to RGBA64", 16384, 2048, 16, 2, 0, false},
		{"gray alpha16 expands to RGBA64", 16384, 2048, 16, 4, 0, false},
		{"gray16 reserves transparency expansion", 16384, 2048, 16, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := screenshotTestPNGHeader(t, tc.width, tc.height, tc.depth, tc.colorType, tc.interlace)
			config, err := png.DecodeConfig(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("invalid test header: %v", err)
			}
			err = checkScreenshotPNGResources(config, data)
			if tc.allowed {
				if err != nil {
					t.Fatalf("normal capture rejected: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "estimated decode memory") {
				t.Fatalf("expected decode memory rejection, got %v", err)
			}
		})
	}
}

func TestScreenshotPeerClose(t *testing.T) {
	conn, done := screenshotTestPipe(t, screenshotTestInfo("1.4.1"), func(sc *SecureConn) error {
		if _, err := screenshotTestRequest(sc); err != nil {
			return err
		}
		return writeProtoMessage(sc, &pb.Message{Union: &pb.Message_Misc{Misc: &pb.Misc{Union: &pb.Misc_CloseReason{}}}})
	})
	if _, err := conn.takeScreenshot(0, time.Second); err == nil || !strings.Contains(err.Error(), "peer closed") {
		t.Fatalf("error = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestScreenshotLoginDenied(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	cs, ss := NewSecureConn(client), NewSecureConn(server)
	cs.SetKey([32]byte{5})
	ss.SetKey([32]byte{5})
	done := make(chan error, 1)
	go func() {
		if err := writeProtoMessage(ss, &pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Challenge: "test"}}}); err != nil {
			done <- err
			return
		}
		if _, err := readProtoMessage(ss); err != nil {
			done <- err
			return
		}
		done <- writeProtoMessage(ss, &pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: &pb.LoginResponse{
			Union: &pb.LoginResponse_Error{Error: "desktop access denied"},
		}}})
	}()
	conn := &Conn{sc: cs}
	if _, err := conn.takeScreenshot(0, time.Second); err == nil || !strings.Contains(err.Error(), "peer rejected login") || strings.Contains(err.Error(), "desktop access denied") {
		t.Fatalf("error = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestScreenshotOverallDeadlineDuringLogin(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		sc := NewSecureConn(conn)
		if _, err := sc.ReadMessage(); err != nil {
			done <- err
			return
		}
		_, err = sc.ReadMessage()
		if err == nil {
			done <- errors.New("unexpected login without challenge")
			return
		}
		done <- nil
	}()
	started := time.Now()
	_, err = TakeScreenshot(DialOpts{PeerID: listener.Addr().String(), Yes: true, Timeout: 50 * time.Millisecond}, 0)
	if err == nil {
		t.Fatal("expected timeout")
	}
	if time.Since(started) > time.Second {
		t.Fatalf("deadline exceeded: %v", time.Since(started))
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

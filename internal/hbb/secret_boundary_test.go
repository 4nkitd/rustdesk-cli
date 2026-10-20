package hbb

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func secretBoundaryOpts() DialOpts {
	return DialOpts{
		Password: "synthetic-password-canary-7q",
		Token:    "synthetic-token-canary-8r",
		Key:      "synthetic-key-canary-9s",
		OTP:      "87264019",
		Timeout:  time.Second,
	}
}

func TestPeerTextKnownSecrets(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts DialOpts
		text string
		want string
	}{
		{"benign filename", secretBoundaryOpts(), "denied: /reports/passwords-2026.txt", "denied: /reports/passwords-2026.txt"},
		{"all credentials", secretBoundaryOpts(), secretBoundaryReflection(), "[redacted] / [redacted] / [redacted] / [redacted]"},
		{"credential in filename", secretBoundaryOpts(), "/reports/" + secretBoundaryOpts().OTP + ".txt denied", "/reports/[redacted].txt denied"},
		{"overlapping credentials", DialOpts{Password: "canary", Token: "canary-token"}, "denied: canary-token canary", "denied: [redacted] [redacted]"},
		{"control characters", DialOpts{Password: "syn\nthetic", OTP: "87264019"}, "denied\x00 synthetic 8726\x1b4019\n", "denied [redacted] [redacted]"},
		{"marker collision", DialOpts{Password: "redacted"}, "denied: /reports/redacted.txt", "denied: /reports/[].txt"},
		{"joined fragments", DialOpts{Password: "abc", Token: "[redacted]"}, "a[redacted]bc /reports/report.txt", " /reports/report.txt"},
		{"empty credentials", DialOpts{}, "benign\r\nmessage", "benignmessage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Conn{opts: tc.opts}
			if got := c.PeerText(tc.text); got != tc.want {
				t.Fatalf("PeerText = %q, want %q", got, tc.want)
			}
			if err := c.PeerError(tc.text); err == nil || err.Error() != tc.want {
				t.Fatal("PeerError did not use the same redaction")
			}
		})
	}
}

func TestPeerTextRetainsOTPAfterAuthentication(t *testing.T) {
	c := secretBoundaryPipe(t, func(sc *SecureConn) error {
		if err := secretBoundaryOTP(sc); err != nil {
			return err
		}
		return writeProtoMessage(sc, auth2FASuccess())
	})
	if _, err := c.Login(LoginDefault, LoginSpec{}); err != nil {
		t.Fatal(err)
	}
	if c.opts.OTP != "" {
		t.Fatal("login retained OTP in dial options")
	}
	secretBoundaryAssertText(t, c.PeerText(secretBoundaryReflection()))
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	secretBoundaryAssertText(t, c.PeerText(secretBoundaryReflection()))
}

func TestPeerTextConcurrentClose(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	c := &Conn{sc: NewSecureConn(client), opts: secretBoundaryOpts()}
	secretBoundaryAssertText(t, c.PeerText(secretBoundaryReflection()))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 32; j++ {
				if err := c.Close(); err != nil {
					t.Error(err)
				}
				secretBoundaryAssertText(t, c.PeerText(secretBoundaryReflection()))
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for j := 0; j < 32; j++ {
			if err := c.HandleLoginError("Wrong Password"); err == nil {
				t.Error("password rejection accepted")
			}
		}
	}()
	close(start)
	wg.Wait()
	if c.opts.OTP != "" {
		t.Error("authentication handler retained OTP")
	}
}

func secretBoundaryReflection() string {
	opts := secretBoundaryOpts()
	return strings.Join([]string{opts.Password, opts.Token, opts.Key, opts.OTP}, " / ")
}

func secretBoundaryAssertText(t *testing.T, text string) {
	t.Helper()
	opts := secretBoundaryOpts()
	for name, value := range map[string]string{"password": opts.Password, "token": opts.Token, "key": opts.Key, "OTP": opts.OTP} {
		if strings.Contains(text, value) {
			t.Errorf("feature boundary disclosed synthetic %s canary", name)
		}
	}
}

func secretBoundaryAssertError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("peer failure was accepted")
	}
	var ne net.Error
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.As(err, &ne) {
		t.Fatalf("expected peer failure, got transport failure: %v", err)
	}
	secretBoundaryAssertText(t, err.Error())
}

func secretBoundaryPipe(t *testing.T, script func(*SecureConn) error) *Conn {
	t.Helper()
	client, server := net.Pipe()
	cs, ss := NewSecureConn(client), NewSecureConn(server)
	key := [32]byte{3, 1, 4, 1, 5, 9}
	cs.SetKey(key)
	ss.SetKey(key)
	deadline := time.Now().Add(2 * time.Second)
	if err := client.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := server.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	release := make(chan struct{})
	go func() {
		defer server.Close()
		done <- script(ss)
		<-release
	}()
	c := &Conn{sc: cs, peerID: "synthetic-peer", opts: secretBoundaryOpts()}
	t.Cleanup(func() {
		c.Close()
		server.Close()
		close(release)
		if err := <-done; err != nil {
			t.Errorf("synthetic encrypted peer: %v", err)
		}
		if c.opts.OTP != "" {
			t.Error("feature boundary retained OTP in dial options")
		}
	})
	return c
}

func secretBoundaryOTP(sc *SecureConn) error {
	opts := secretBoundaryOpts()
	if err := writeProtoMessage(sc, auth2FAHash("challenge")); err != nil {
		return err
	}
	msg, err := readProtoMessage(sc)
	if err != nil {
		return err
	}
	lr := msg.GetLoginRequest()
	if lr == nil || !bytes.Equal(lr.GetPassword(), hashPassword(opts.Password, "salt", "challenge")) || len(lr.GetHwid()) != 0 {
		return errors.New("missing synthetic password login or unexpected device enrollment")
	}
	if err := writeProtoMessage(sc, auth2FAError("2FA Required")); err != nil {
		return err
	}
	msg, err = readProtoMessage(sc)
	if err != nil {
		return err
	}
	auth := msg.GetAuth_2Fa()
	if auth == nil || auth.GetCode() != opts.OTP || len(auth.GetHwid()) != 0 {
		return errors.New("missing synthetic OTP or unexpected device enrollment")
	}
	return nil
}

func secretBoundarySuccess(pi *pb.PeerInfo) *pb.Message {
	return &pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: &pb.LoginResponse{
		Union: &pb.LoginResponse_PeerInfo{PeerInfo: pi},
	}}}
}

func secretBoundaryClose(text string) *pb.Message {
	return &pb.Message{Union: &pb.Message_Misc{Misc: &pb.Misc{Union: &pb.Misc_CloseReason{CloseReason: text}}}}
}

func TestSecretBoundaryScreenshotAfterOTP(t *testing.T) {
	for _, boundary := range []string{"response message", "close reason", "unsupported version"} {
		t.Run(boundary, func(t *testing.T) {
			c := secretBoundaryPipe(t, func(sc *SecureConn) error {
				if err := secretBoundaryOTP(sc); err != nil {
					return err
				}
				pi := screenshotTestInfo("1.4.1")
				if boundary == "unsupported version" {
					pi.Version = secretBoundaryReflection()
				}
				if err := writeProtoMessage(sc, secretBoundarySuccess(pi)); err != nil {
					return err
				}
				if boundary == "unsupported version" {
					return nil
				}
				req, err := screenshotTestRequest(sc)
				if err != nil {
					return err
				}
				if boundary == "close reason" {
					return writeProtoMessage(sc, secretBoundaryClose(secretBoundaryReflection()))
				}
				return screenshotTestResponse(sc, req.GetSid(), secretBoundaryReflection(), nil)
			})
			_, err := c.takeScreenshot(0, time.Second)
			secretBoundaryAssertError(t, err)
		})
	}
}

func TestSecretBoundaryClipboardAfterOTP(t *testing.T) {
	for _, phase := range []string{"login", "read", "write"} {
		for _, boundary := range []string{"login error", "close reason"} {
			t.Run(phase+"/"+boundary, func(t *testing.T) {
				c := secretBoundaryPipe(t, func(sc *SecureConn) error {
					if err := secretBoundaryOTP(sc); err != nil {
						return err
					}
					if phase != "login" {
						if err := writeProtoMessage(sc, auth2FASuccess()); err != nil {
							return err
						}
						msg, err := readProtoMessage(sc)
						if err != nil {
							return err
						}
						if msg.GetMisc().GetCaptureDisplays() == nil {
							return errors.New("missing clipboard capture unsubscribe")
						}
						msg, err = readProtoMessage(sc)
						if err != nil {
							return err
						}
						if phase == "read" && msg.GetMisc().GetOption().GetDisableClipboard() != pb.OptionMessage_No {
							return errors.New("missing clipboard read subscription")
						}
						if phase == "write" && (msg.GetTestDelay() == nil || !msg.GetTestDelay().GetFromClient()) {
							return errors.New("missing clipboard write barrier")
						}
					}
					last := auth2FAError(secretBoundaryReflection())
					if boundary == "close reason" {
						last = secretBoundaryClose(secretBoundaryReflection())
					}
					return writeProtoMessage(sc, last)
				})
				clip := &Clipboard{conn: c, timeout: time.Second, allowed: true, keyboard: true}
				if err := clip.begin(); err != nil {
					t.Fatal(err)
				}
				err := clip.login()
				if phase != "login" {
					if err != nil {
						t.Fatal("synthetic clipboard login failed:", err)
					}
					if phase == "read" {
						_, err = clip.ReadText()
					} else {
						err = clip.WriteText("synthetic clipboard payload")
					}
				}
				secretBoundaryAssertError(t, err)
			})
		}
	}
}

func TestSecretBoundaryDiagnosticsAfterOTP(t *testing.T) {
	t.Setenv("RDC_DEBUG", "")
	for _, boundary := range []string{"metadata", "login error", "close reason"} {
		t.Run(boundary, func(t *testing.T) {
			c := secretBoundaryPipe(t, func(sc *SecureConn) error {
				if err := secretBoundaryOTP(sc); err != nil {
					return err
				}
				switch boundary {
				case "metadata":
					text := "synthetic-host " + secretBoundaryReflection()
					return writeProtoMessage(sc, secretBoundarySuccess(&pb.PeerInfo{Version: text, Platform: text, Hostname: text}))
				case "login error":
					return writeProtoMessage(sc, auth2FAError(secretBoundaryReflection()))
				default:
					return writeProtoMessage(sc, secretBoundaryClose(secretBoundaryReflection()))
				}
			})
			result := probeWithConnector(secretBoundaryOpts(), func(DialOpts) (*Conn, error) { return c, nil })
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			secretBoundaryAssertText(t, string(encoded))
			if boundary == "metadata" {
				if !result.OK || result.Peer == nil || result.Error != nil {
					t.Fatal("synthetic diagnostic metadata exchange failed")
				}
				for _, value := range []*string{result.Peer.Version, result.Peer.Platform, result.Peer.Hostname} {
					if value == nil || !strings.Contains(*value, "synthetic-host") {
						t.Error("diagnostics discarded nonsecret metadata")
					}
				}
			} else {
				want := "login_rejected"
				if boundary == "close reason" {
					want = "peer_closed"
				}
				if result.OK || result.Error == nil || result.Error.Code != want {
					t.Fatal("diagnostics did not classify the peer failure")
				}
				secretBoundaryAssertError(t, result.Error)
			}
		})
	}
}

func TestSecretBoundaryTransferAfterOTP(t *testing.T) {
	for _, operation := range []string{"read directory", "create directory", "mkdir", "initial listing", "download", "close reason"} {
		for _, reflectSecret := range []bool{false, true} {
			name := operation + "/benign filename"
			message := "permission denied: /reports/passwords-2026.txt"
			if reflectSecret {
				name = operation + "/reflected secrets"
				message += " " + secretBoundaryReflection()
			}
			t.Run(name, func(t *testing.T) {
				c := secretBoundaryPipe(t, func(sc *SecureConn) error {
					if err := secretBoundaryOTP(sc); err != nil {
						return err
					}
					if err := writeProtoMessage(sc, auth2FASuccess()); err != nil {
						return err
					}
					id := int32(0)
					if operation != "initial listing" {
						msg, err := readProtoMessage(sc)
						if err != nil {
							return err
						}
						action := msg.GetFileAction()
						if action == nil {
							return errors.New("missing file operation")
						}
						switch operation {
						case "read directory":
							if action.GetReadDir() == nil {
								return errors.New("missing directory request")
							}
						case "download":
							if action.GetSend() == nil {
								return errors.New("missing download request")
							}
							id = action.GetSend().GetId()
						default:
							if action.GetCreate() == nil {
								return errors.New("missing create request")
							}
							id = action.GetCreate().GetId()
						}
					}
					if operation == "close reason" {
						return writeProtoMessage(sc, secretBoundaryClose(message))
					}
					return writeProtoMessage(sc, &pb.Message{Union: &pb.Message_FileResponse{FileResponse: &pb.FileResponse{
						Union: &pb.FileResponse_Error{Error: &pb.FileTransferError{Id: id, Error: message}},
					}}})
				})
				if _, err := c.Login(LoginFileTransfer, LoginSpec{Dir: "/reports"}); err != nil {
					t.Fatal("synthetic transfer login failed:", err)
				}
				ft := &FileTransfer{conn: c, nextID: 1}
				if err := ft.beginFileOperation(); err != nil {
					t.Fatal(err)
				}
				var err error
				switch operation {
				case "read directory":
					_, err = ft.ReadDir("/reports")
				case "create directory":
					err = ft.CreateDir("/reports/new")
				case "initial listing":
					_, err = ft.waitFileDirectory()
				case "download":
					err = ft.receiveFiles("/reports/passwords-2026.txt", filepath.Join(t.TempDir(), "synthetic-download"), nil)
				default:
					err = ft.Mkdir("/reports/new")
				}
				secretBoundaryAssertError(t, err)
				if !reflectSecret && !strings.Contains(err.Error(), "/reports/passwords-2026.txt") {
					t.Error("transfer error discarded legitimate nonsecret filename")
				}
			})
		}
	}
}

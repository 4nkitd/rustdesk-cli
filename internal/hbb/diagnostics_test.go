package hbb

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func diagnosticTestSend(sc *SecureConn, msg *pb.Message) error {
	b, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = sc.Write(b)
	return err
}

func diagnosticTestPermission(permission pb.PermissionInfo_Permission, enabled bool) *pb.Message {
	return &pb.Message{Union: &pb.Message_Misc{Misc: &pb.Misc{
		Union: &pb.Misc_PermissionInfo{PermissionInfo: &pb.PermissionInfo{Permission: permission, Enabled: enabled}},
	}}}
}

func TestDiagnosticProbeObservedState(t *testing.T) {
	t.Setenv("RDC_DEBUG", "")
	for _, direct := range []bool{false, true} {
		for _, secured := range []bool{false, true} {
			name := "relay"
			if direct {
				name = "direct"
			}
			if secured {
				name += "-secured"
			}
			t.Run(name, func(t *testing.T) {
				client, server := net.Pipe()
				defer server.Close()
				c := &Conn{sc: NewSecureConn(client), direct: direct, peerID: "123456789"}
				sc := NewSecureConn(server)
				if secured {
					key := [32]byte{1, 2, 3}
					c.sc.SetKey(key)
					sc.SetKey(key)
				}
				done := make(chan error, 1)
				go func() {
					defer server.Close()
					if err := diagnosticTestSend(sc, diagnosticTestPermission(pb.PermissionInfo_File, false)); err != nil {
						done <- err
						return
					}
					if err := diagnosticTestSend(sc, &pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Salt: "salt", Challenge: "challenge"}}}); err != nil {
						done <- err
						return
					}
					msg, err := diagnosticRecv(&Conn{sc: sc})
					if err != nil {
						done <- err
						return
					}
					lr := msg.GetLoginRequest()
					if lr == nil || lr.GetUnion() != nil || !bytes.Equal(lr.GetPassword(), hashPassword("test-password", "salt", "challenge")) {
						done <- errors.New("not a default authenticated login")
						return
					}
					o := lr.GetOption()
					if o.GetDisableKeyboard() != pb.OptionMessage_Yes || o.GetDisableClipboard() != pb.OptionMessage_Yes || o.GetDisableAudio() != pb.OptionMessage_Yes || o.GetEnableFileTransfer() != pb.OptionMessage_No || o.GetLockAfterSessionEnd() != pb.OptionMessage_No {
						done <- errors.New("login did not disable session input and transfer")
						return
					}
					if err := diagnosticTestSend(sc, &pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: &pb.LoginResponse{
						Union: &pb.LoginResponse_PeerInfo{PeerInfo: &pb.PeerInfo{
							Version: "1.4.1", Platform: "Linux", Hostname: "mock-host",
							Features: &pb.Features{Terminal: true},
						}},
					}}}); err != nil {
						done <- err
						return
					}
					if err := diagnosticTestSend(sc, diagnosticTestPermission(pb.PermissionInfo_Clipboard, true)); err != nil {
						done <- err
						return
					}
					_, err = sc.ReadMessage()
					if err == nil {
						done <- errors.New("unexpected message after read-only login")
						return
					}
					done <- nil
				}()
				result := probeWithConnector(DialOpts{Password: "test-password", ForceRelay: direct, Timeout: time.Second, ConnType: pb.ConnType_TERMINAL}, func(opts DialOpts) (*Conn, error) {
					if opts.ConnType != pb.ConnType_DEFAULT_CONN {
						t.Fatal("probe requested a non-default connection")
					}
					return c, nil
				})
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if !result.OK || result.Stage != "complete" || result.Error != nil {
					t.Fatalf("probe failed: %+v", result)
				}
				wantTransport := "relay"
				if direct {
					wantTransport = "direct"
				}
				if result.Transport != wantTransport || result.Secured == nil || *result.Secured != secured {
					t.Fatalf("wrong observed connection state: %+v", result)
				}
				if result.Peer == nil || *result.Peer.Version != "1.4.1" || *result.Peer.Platform != "Linux" || *result.Peer.Hostname != "mock-host" {
					t.Fatalf("wrong peer information: %+v", result.Peer)
				}
				if result.Capabilities["terminal"] != "advertised" || result.Capabilities["privacy_mode"] != "not_advertised" {
					t.Fatalf("wrong capability observations: %+v", result.Capabilities)
				}
				if result.Permissions["file"] == nil || *result.Permissions["file"] || result.Permissions["clipboard"] == nil || !*result.Permissions["clipboard"] || result.Permissions["terminal"] != nil {
					t.Fatalf("wrong permission observations: %+v", result.Permissions)
				}
			})
		}
	}
}

func TestDiagnosticProbeLoginDeadline(t *testing.T) {
	t.Setenv("RDC_DEBUG", "")
	client, server := net.Pipe()
	defer server.Close()
	start := time.Now()
	result := probeWithConnector(DialOpts{Timeout: 20 * time.Millisecond}, func(DialOpts) (*Conn, error) {
		return &Conn{sc: NewSecureConn(client), direct: true}, nil
	})
	if result.OK || result.Error == nil || result.Error.Code != "timeout" || result.Error.Stage != "authenticate" {
		t.Fatalf("expected bounded authentication failure, got %+v", result)
	}
	if time.Since(start) > time.Second {
		t.Fatal("probe exceeded its login deadline")
	}
	if _, err := server.Write([]byte("x")); err == nil {
		t.Fatal("probe left its connection open")
	}
}

func TestDiagnosticProbeDoesNotExposeConnectionErrors(t *testing.T) {
	t.Setenv("RDC_DEBUG", "")
	result := probeWithConnector(DialOpts{}, func(DialOpts) (*Conn, error) {
		return nil, errors.New("password=test-password token=test-token uuid=12345678-1234-1234-1234-123456789abc")
	})
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "test-password") || strings.Contains(string(b), "test-token") || strings.Contains(string(b), "12345678") {
		t.Fatalf("diagnostics exposed raw error: %s", b)
	}
	if result.Transport != "unknown" || result.Secured != nil || result.Peer != nil || result.Error == nil || result.Error.Stage != "connect" {
		t.Fatalf("failed connection fabricated observations: %+v", result)
	}
}

func TestDiagnosticProbeRejectsDebugBeforeConnecting(t *testing.T) {
	t.Setenv("RDC_DEBUG", "1")
	result := probeWithConnector(DialOpts{}, func(DialOpts) (*Conn, error) {
		t.Fatal("debug-enabled probe must not invoke connection logs")
		return nil, nil
	})
	if result.Error == nil || result.Error.Code != "debug_enabled" {
		t.Fatalf("got %+v", result)
	}
}

func TestDiagnosticLoginRejectsUnsolicitedPeerInfo(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		_ = diagnosticTestSend(NewSecureConn(server), &pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: &pb.LoginResponse{
			Union: &pb.LoginResponse_PeerInfo{PeerInfo: &pb.PeerInfo{Version: "unverified"}},
		}}})
	}()
	result := unknownDiagnostics()
	err := (&Conn{sc: NewSecureConn(client)}).diagnosticLogin(&result, DialOpts{})
	var de *DiagnosticError
	if !errors.As(err, &de) || de.Code != "unexpected_response" || result.Peer != nil {
		t.Fatalf("unsolicited peer info accepted: %+v, %v", result, err)
	}
}

func TestDiagnosticTextRedaction(t *testing.T) {
	value := "test-password test-token test-key 654321 12345678-1234-1234-1234-123456789abc 12345678123412341234123456789abc\x1b\n"
	got := diagnosticText(value, DialOpts{Password: "test-password", Token: "test-token", Key: "test-key", OTP: "654321"})
	if got == nil || *got != "[redacted] [redacted] [redacted] [redacted] [redacted] [redacted]" {
		t.Fatalf("unexpected sanitized text: %v", got)
	}
	if diagnosticText("", DialOpts{}) != nil {
		t.Fatal("missing text must remain unknown")
	}
}

func TestDiagnosticTextRedactsNormalizedSecrets(t *testing.T) {
	opts := DialOpts{Password: "test-password", Token: "test-token", OTP: "654321"}
	for _, value := range []string{"test-\npassword", "test-\rtoken", "654\x1b321", "12345678-1234-1234-1234-123456\n789abc"} {
		got := diagnosticText(value, opts)
		if got == nil || *got != "[redacted]" {
			t.Fatalf("control characters bypassed redaction: %v", got)
		}
	}
	if got := diagnosticText("redacted", DialOpts{Password: "redacted"}); got != nil {
		t.Fatalf("redaction marker exposed the credential: %q", *got)
	}
}

func TestDiagnosticProbeOTP(t *testing.T) {
	t.Setenv("RDC_DEBUG", "")
	for _, tc := range []struct {
		name, otp, response, wantCode string
		secured, sendsOTP, stalls     bool
	}{
		{name: "success", otp: "654321", secured: true, sendsOTP: true},
		{name: "missing", secured: true, wantCode: "otp_required"},
		{name: "insecure", otp: "654321", wantCode: "otp_failed"},
		{name: "invalid", otp: "invalid-code", secured: true, wantCode: "otp_failed"},
		{name: "rejected", otp: "654321", secured: true, sendsOTP: true, response: "Wrong 2FA Code", wantCode: "otp_rejected"},
		{name: "repeated", otp: "654321", secured: true, sendsOTP: true, response: "2FA Required", wantCode: "otp_failed"},
		{name: "timeout-after-otp", otp: "654321", secured: true, sendsOTP: true, stalls: true, wantCode: "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer server.Close()
			_ = server.SetDeadline(time.Now().Add(2 * time.Second))
			opts := DialOpts{OTP: tc.otp, Timeout: time.Second, Yes: true}
			if tc.stalls {
				opts.Timeout = 30 * time.Millisecond
			}
			c := &Conn{sc: NewSecureConn(client), peerID: "123456789", opts: opts}
			sc := NewSecureConn(server)
			if tc.secured {
				key := [32]byte{4, 5, 6}
				c.sc.SetKey(key)
				sc.SetKey(key)
			}
			done := make(chan error, 1)
			go func() {
				defer server.Close()
				if err := diagnosticTestSend(sc, &pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Challenge: "challenge", Salt: "salt"}}}); err != nil {
					done <- err
					return
				}
				if _, err := sc.ReadMessage(); err != nil {
					done <- err
					return
				}
				if err := diagnosticTestSend(sc, &pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: &pb.LoginResponse{
					Union: &pb.LoginResponse_Error{Error: "2FA Required"},
				}}}); err != nil {
					done <- err
					return
				}
				if tc.sendsOTP {
					msg, err := diagnosticRecv(&Conn{sc: sc})
					if err != nil {
						done <- err
						return
					}
					if msg.GetAuth_2Fa() == nil || msg.GetAuth_2Fa().GetCode() != tc.otp || len(msg.GetAuth_2Fa().GetHwid()) != 0 {
						done <- errors.New("missing shared-helper OTP response or unexpected device enrollment")
						return
					}
					if !tc.stalls {
						response := &pb.LoginResponse{Union: &pb.LoginResponse_PeerInfo{PeerInfo: &pb.PeerInfo{Version: "1.4.1", Hostname: "host-654321"}}}
						if tc.response != "" {
							response.Union = &pb.LoginResponse_Error{Error: tc.response}
						}
						if err := diagnosticTestSend(sc, &pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: response}}); err != nil {
							done <- err
							return
						}
					}
				}
				if _, err := sc.ReadMessage(); err == nil {
					done <- errors.New("diagnostics submitted an unexpected or repeated OTP")
					return
				}
				done <- nil
			}()
			result := probeWithConnector(opts, func(DialOpts) (*Conn, error) { return c, nil })
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if c.opts.OTP != "" {
				t.Fatal("connection retained the OTP")
			}
			if tc.wantCode == "" {
				if !result.OK || result.Error != nil || result.Peer == nil || result.Peer.Hostname == nil || *result.Peer.Hostname != "host-[redacted]" {
					t.Fatalf("OTP login did not await sanitized peer info: %+v", result)
				}
			} else if result.OK || result.Error == nil || result.Error.Stage != "authenticate" || result.Error.Code != tc.wantCode || result.Peer != nil {
				t.Fatalf("incorrect OTP failure: %+v", result)
			}
			data, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if tc.otp != "" && strings.Contains(string(data), tc.otp) {
				t.Fatal("diagnostics exposed the OTP")
			}
		})
	}
}

func TestDiagnosticProbePeerMetadataRedactsCredentials(t *testing.T) {
	t.Setenv("RDC_DEBUG", "")
	client, server := net.Pipe()
	defer server.Close()
	opts := DialOpts{Password: "test-password", Token: "test-token", Key: "test-key", OTP: "654321", Timeout: time.Second}
	go func() {
		defer server.Close()
		sc := NewSecureConn(server)
		if err := diagnosticTestSend(sc, &pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{}}}); err != nil {
			return
		}
		if _, err := sc.ReadMessage(); err != nil {
			return
		}
		value := "test-password|test-token|test-key|654321|12345678-1234-1234-1234-123456789abc"
		_ = diagnosticTestSend(sc, &pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: &pb.LoginResponse{
			Union: &pb.LoginResponse_PeerInfo{PeerInfo: &pb.PeerInfo{Version: value, Platform: value, Hostname: value}},
		}}})
	}()
	result := probeWithConnector(opts, func(DialOpts) (*Conn, error) {
		return &Conn{sc: NewSecureConn(client), peerID: "123456789", opts: opts}, nil
	})
	if !result.OK || result.Peer == nil {
		t.Fatalf("probe failed: %+v", result)
	}
	data, err := json.Marshal(result.Peer)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{opts.Password, opts.Token, opts.Key, opts.OTP, "12345678-1234-1234-1234-123456789abc"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("returned peer metadata contained a credential or UUID")
		}
	}
}

func TestDiagnosticProbeLoginResponse(t *testing.T) {
	t.Setenv("RDC_DEBUG", "")
	for _, rejected := range []bool{false, true} {
		name := "minimal-peer-info"
		if rejected {
			name = "rejected"
		}
		t.Run(name, func(t *testing.T) {
			client, server := net.Pipe()
			defer server.Close()
			go func() {
				defer server.Close()
				sc := NewSecureConn(server)
				if err := diagnosticTestSend(sc, &pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{}}}); err != nil {
					return
				}
				if _, err := sc.ReadMessage(); err != nil {
					return
				}
				response := &pb.LoginResponse{Union: &pb.LoginResponse_PeerInfo{PeerInfo: &pb.PeerInfo{Version: "1.4.1"}}}
				if rejected {
					response.Union = &pb.LoginResponse_Error{Error: "test-password test-token 12345678-1234-1234-1234-123456789abc"}
				}
				_ = diagnosticTestSend(sc, &pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: response}})
			}()
			result := probeWithConnector(DialOpts{Timeout: time.Second}, func(DialOpts) (*Conn, error) {
				return &Conn{sc: NewSecureConn(client)}, nil
			})
			if rejected {
				if result.OK || result.Error == nil || result.Error.Code != "login_rejected" || result.Peer != nil {
					t.Fatalf("unexpected rejected login: %+v", result)
				}
				b, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				for _, secret := range []string{"test-password", "test-token", "12345678"} {
					if strings.Contains(string(b), secret) {
						t.Fatalf("exposed login rejection text: %s", b)
					}
				}
			} else if !result.OK || result.Peer == nil || result.Peer.Hostname != nil || result.Peer.Platform != nil || result.Capabilities["terminal"] != "unknown" || result.Permissions["keyboard"] != nil {
				t.Fatalf("missing fields did not remain unknown: %+v", result)
			}
		})
	}
}

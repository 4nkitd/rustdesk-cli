package hbb

import (
	"errors"
	"net"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

type DiagnosticError struct {
	Stage   string `json:"stage"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *DiagnosticError) Error() string { return e.Stage + ": " + e.Message }

type DiagnosticPeer struct {
	Version  *string `json:"version"`
	Platform *string `json:"platform"`
	Hostname *string `json:"hostname"`
}

type Diagnostics struct {
	Version      int               `json:"version"`
	OK           bool              `json:"ok"`
	Stage        string            `json:"stage"`
	Transport    string            `json:"transport"`
	Secured      *bool             `json:"secured"`
	Peer         *DiagnosticPeer   `json:"peer"`
	Capabilities map[string]string `json:"capabilities"`
	Permissions  map[string]*bool  `json:"permissions"`
	ElapsedMS    int64             `json:"elapsed_ms"`
	Error        *DiagnosticError  `json:"error,omitempty"`
}

func unknownDiagnostics() Diagnostics {
	return Diagnostics{
		Version: 1, Stage: "connect", Transport: "unknown",
		Capabilities: map[string]string{"privacy_mode": "unknown", "terminal": "unknown"},
		Permissions: map[string]*bool{
			"keyboard": nil, "clipboard": nil, "audio": nil, "file": nil,
			"restart": nil, "recording": nil, "block_input": nil,
			"privacy_mode": nil, "terminal": nil, "tunnel": nil,
		},
	}
}

// Probe uses Connect's bounded per-phase timeout, then a separate bounded login.
// It never opens a terminal, tunnel, file transfer, or sends control events.
func Probe(opts DialOpts) Diagnostics {
	return probeWithConnector(opts, Connect)
}

func probeWithConnector(opts DialOpts, connect func(DialOpts) (*Conn, error)) (result Diagnostics) {
	result = unknownDiagnostics()
	start := time.Now()
	defer func() { result.ElapsedMS = time.Since(start).Milliseconds() }()
	fail := func(stage, code, message string) {
		result.Stage = stage
		result.Error = &DiagnosticError{Stage: stage, Code: code, Message: message}
	}
	// Connect's debug output includes relay UUIDs. Do not invoke it in that mode.
	if os.Getenv("RDC_DEBUG") != "" {
		fail("connect", "debug_enabled", "diagnostics require RDC_DEBUG to be unset")
		return
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	opts.ConnType = pb.ConnType_DEFAULT_CONN
	c, err := connect(opts)
	if err != nil {
		fail("connect", diagnosticErrorCode(err), "connection or secure handshake failed")
		return
	}
	defer c.Close()
	result.Transport = "relay"
	if c.Direct() {
		result.Transport = "direct"
	}
	secured := c.sc.IsSecured()
	result.Secured = &secured
	result.Stage = "authenticate"
	deadline := time.Now().Add(opts.Timeout)
	if err := c.sc.SetDeadline(deadline); err != nil {
		fail("authenticate", "deadline_failed", "could not bound authentication")
		return
	}
	if err := c.diagnosticLogin(&result, opts); err != nil {
		var de *DiagnosticError
		if errors.As(err, &de) {
			result.Error = de
		} else {
			fail("authenticate", diagnosticErrorCode(err), "authentication exchange failed")
		}
		return
	}
	// Permissions may follow PeerInfo. Missing announcements remain unknown.
	observationDeadline := time.Now().Add(100 * time.Millisecond)
	if deadline.Before(observationDeadline) {
		observationDeadline = deadline
	}
	if err := c.sc.SetDeadline(observationDeadline); err == nil {
		for {
			msg, err := diagnosticRecv(c)
			if err != nil {
				break
			}
			observeDiagnosticPermission(&result, msg)
		}
	}
	result.OK = true
	result.Stage = "complete"
	return
}

func diagnosticErrorCode(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	return "failed"
}

func diagnosticRecv(c *Conn) (*pb.Message, error) {
	raw, err := c.sc.ReadMessage()
	if err != nil {
		return nil, err
	}
	msg := &pb.Message{}
	if err := decodePeerMessage(raw, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

func (c *Conn) diagnosticLogin(result *Diagnostics, opts DialOpts) error {
	defer func() { c.opts.OTP = "" }()
	sentLogin := false
	for {
		msg, err := diagnosticRecv(c)
		if err != nil {
			return err
		}
		observeDiagnosticPermission(result, msg)
		switch u := msg.GetUnion().(type) {
		case *pb.Message_Hash:
			if sentLogin {
				return &DiagnosticError{"authenticate", "repeated_challenge", "peer repeated the authentication challenge"}
			}
			lr := &pb.LoginRequest{
				Username: c.peerID, MyId: randomPeerID(), MyName: "rdcli",
				MyPlatform: runtime.GOOS, Version: Version,
				Option: &pb.OptionMessage{
					DisableAudio: pb.OptionMessage_Yes, DisableClipboard: pb.OptionMessage_Yes,
					DisableKeyboard: pb.OptionMessage_Yes, DisableCamera: pb.OptionMessage_Yes,
					EnableFileTransfer: pb.OptionMessage_No, LockAfterSessionEnd: pb.OptionMessage_No,
				},
			}
			if opts.Password != "" {
				lr.Password = hashPassword(opts.Password, u.Hash.GetSalt(), u.Hash.GetChallenge())
			}
			if err := c.Send(&pb.Message{Union: &pb.Message_LoginRequest{LoginRequest: lr}}); err != nil {
				return err
			}
			sentLogin = true
		case *pb.Message_LoginResponse:
			if !sentLogin {
				return &DiagnosticError{"authenticate", "unexpected_response", "peer replied before a login request"}
			}
			if message := u.LoginResponse.GetError(); message != "" {
				if err := c.HandleLoginError(message); err != nil {
					if diagnosticErrorCode(err) == "timeout" {
						return &DiagnosticError{"authenticate", "timeout", "authentication exchange timed out"}
					}
					switch message {
					case "2FA Required":
						if opts.OTP == "" {
							return &DiagnosticError{"authenticate", "otp_required", "peer requires an OTP; supply --otp or RDC_OTP"}
						}
						return &DiagnosticError{"authenticate", "otp_failed", "two-factor authentication failed"}
					case "Wrong 2FA Code":
						return &DiagnosticError{"authenticate", "otp_rejected", "peer rejected the two-factor authentication code"}
					default:
						return &DiagnosticError{"authenticate", "login_rejected", "peer rejected the login request"}
					}
				}
				continue
			}
			pi := u.LoginResponse.GetPeerInfo()
			if pi == nil {
				return &DiagnosticError{"authenticate", "missing_peer_info", "login response contained no peer information"}
			}
			c.peerInfo = pi
			result.Peer = &DiagnosticPeer{
				Version: diagnosticText(pi.GetVersion(), opts), Platform: diagnosticText(pi.GetPlatform(), opts),
				Hostname: diagnosticText(pi.GetHostname(), opts),
			}
			if f := pi.GetFeatures(); f != nil {
				for name, advertised := range map[string]bool{"privacy_mode": f.GetPrivacyMode(), "terminal": f.GetTerminal()} {
					result.Capabilities[name] = "not_advertised"
					if advertised {
						result.Capabilities[name] = "advertised"
					}
				}
			}
			return nil
		case *pb.Message_TestDelay:
			if err := c.Send(&pb.Message{Union: &pb.Message_TestDelay{TestDelay: u.TestDelay}}); err != nil {
				return err
			}
		case *pb.Message_Auth_2Fa:
			return &DiagnosticError{"authenticate", "unexpected_response", "peer sent an unexpected two-factor authentication message"}
		case *pb.Message_Misc:
			if u.Misc.GetCloseReason() != "" {
				return &DiagnosticError{"authenticate", "peer_closed", "peer closed the authentication exchange"}
			}
		}
	}
}

func observeDiagnosticPermission(result *Diagnostics, msg *pb.Message) {
	p := msg.GetMisc().GetPermissionInfo()
	if p == nil {
		return
	}
	names := map[pb.PermissionInfo_Permission]string{
		pb.PermissionInfo_Keyboard: "keyboard", pb.PermissionInfo_Clipboard: "clipboard",
		pb.PermissionInfo_Audio: "audio", pb.PermissionInfo_File: "file",
		pb.PermissionInfo_Restart: "restart", pb.PermissionInfo_Recording: "recording",
		pb.PermissionInfo_BlockInput: "block_input", pb.PermissionInfo_PrivacyMode: "privacy_mode",
	}
	if name, ok := names[p.GetPermission()]; ok {
		enabled := p.GetEnabled()
		result.Permissions[name] = &enabled
	}
}

var diagnosticUUID = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|\b[0-9a-f]{32}\b`)

func diagnosticText(value string, opts DialOpts) *string {
	if value == "" {
		return nil
	}
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, s)
	}
	value = clean(value)
	secrets := []string{opts.Password, opts.Token, opts.Key, opts.OTP}
	for _, secret := range secrets {
		secret = clean(secret)
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	value = diagnosticUUID.ReplaceAllString(value, "[redacted]")
	if runes := []rune(value); len(runes) > 256 {
		value = string(runes[:256])
	}
	for _, secret := range secrets {
		if secret = clean(secret); secret != "" && strings.Contains(value, secret) {
			return nil
		}
	}
	return &value
}

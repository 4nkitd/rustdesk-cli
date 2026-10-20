package integration

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/4nkitd/rustdesk-cli/internal/hbb"
	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func TestCLIEncryptedLoopback(t *testing.T) {
	h := buildCLI(t)

	t.Run("info JSON", func(t *testing.T) {
		fixture := newPeerFixture(t, pb.ConnType_DEFAULT_CONN, "", func(sc *hbb.SecureConn) error {
			message := &pb.Message{}
			if err := readProto(sc, message); errors.Is(err, io.EOF) {
				return nil
			} else if err != nil {
				return err
			}
			return unexpectedMessage(message)
		})
		result := fixture.run(t, h, "info", fixtureID, "--json")
		result.requireCode(t, 0)
		if result.stderr != "" {
			t.Fatalf("unexpected stderr: %s", result.stderr)
		}
		var info struct {
			Version      int                                          `json:"version"`
			OK           bool                                         `json:"ok"`
			Stage        string                                       `json:"stage"`
			Transport    string                                       `json:"transport"`
			Secured      bool                                         `json:"secured"`
			Peer         struct{ Hostname, Platform, Version string } `json:"peer"`
			Capabilities map[string]string                            `json:"capabilities"`
			Error        any                                          `json:"error"`
		}
		requireJSON(t, result.stdout, &info)
		if info.Version != 1 || !info.OK || info.Stage != "complete" || info.Transport != "relay" || !info.Secured || info.Error != nil {
			t.Fatalf("unexpected diagnostics: %s", result.stdout)
		}
		if info.Peer.Hostname != "integration-fixture" || info.Peer.Platform != "Linux" || info.Peer.Version != "1.4.1" || info.Capabilities["terminal"] != "advertised" {
			t.Fatalf("unexpected peer information: %s", result.stdout)
		}
	})

	t.Run("info authentication failure", func(t *testing.T) {
		fixture := newPeerFixture(t, pb.ConnType_DEFAULT_CONN, "fixture-private-denial", nil)
		result := fixture.run(t, h, "info", fixtureID, "--json")
		result.requireCode(t, 1)
		var info struct {
			OK    bool                                  `json:"ok"`
			Stage string                                `json:"stage"`
			Error struct{ Stage, Code, Message string } `json:"error"`
		}
		requireJSON(t, result.stdout, &info)
		if info.OK || info.Stage != "authenticate" || info.Error.Stage != "authenticate" || info.Error.Code == "" || info.Error.Message == "" {
			t.Fatalf("unexpected failure diagnostics: %s", result.stdout)
		}
		if result.stderr == "" || strings.Contains(result.stdout+result.stderr, "fixture-private-denial") {
			t.Fatalf("unexpected failure output: stdout=%q stderr=%q", result.stdout, result.stderr)
		}
	})

	t.Run("screenshot PNG and JSON", func(t *testing.T) {
		pixels := image.NewNRGBA(image.Rect(0, 0, 3, 2))
		pixels.SetNRGBA(1, 1, color.NRGBA{R: 31, G: 127, B: 223, A: 255})
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, pixels); err != nil {
			t.Fatal(err)
		}
		fixture := newPeerFixture(t, pb.ConnType_DEFAULT_CONN, "", screenshotResponse(encoded.Bytes()))
		output := filepath.Join(t.TempDir(), "synthetic.png")
		result := fixture.run(t, h, "screenshot", fixtureID, "--output", output, "--json")
		result.requireCode(t, 0)
		if result.stderr != "" {
			t.Fatalf("unexpected stderr: %s", result.stderr)
		}
		var metadata map[string]any
		requireJSON(t, result.stdout, &metadata)
		if metadata["format"] != "png" || metadata["width"] != float64(3) || metadata["height"] != float64(2) || metadata["output"] != output {
			t.Fatalf("unexpected screenshot metadata: %s", result.stdout)
		}
		if _, exists := metadata["data"]; exists {
			t.Fatal("image data included in metadata")
		}
		actual, err := os.ReadFile(output)
		if err != nil || !bytes.Equal(actual, encoded.Bytes()) {
			t.Fatalf("synthetic PNG mismatch: %v", err)
		}
		decoded, err := png.Decode(bytes.NewReader(actual))
		if err != nil || decoded.Bounds() != pixels.Bounds() {
			t.Fatalf("invalid saved PNG: %v", err)
		}
		if got := color.NRGBAModel.Convert(decoded.At(1, 1)); got != pixels.NRGBAAt(1, 1) {
			t.Fatalf("saved pixel = %v", got)
		}
	})

	t.Run("screenshot rejects invalid PNG", func(t *testing.T) {
		fixture := newPeerFixture(t, pb.ConnType_DEFAULT_CONN, "", screenshotResponse([]byte("synthetic but invalid PNG")))
		output := filepath.Join(t.TempDir(), "invalid.png")
		result := fixture.run(t, h, "screenshot", fixtureID, "--output", output, "--json")
		result.requireCode(t, 1)
		if result.stdout != "" || !strings.Contains(result.stderr, "invalid PNG") {
			t.Fatalf("unexpected failure output: stdout=%q stderr=%q", result.stdout, result.stderr)
		}
		if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid screenshot created a file: %v", err)
		}
	})

	t.Run("screenshot usage exit", func(t *testing.T) {
		unused := serveOnce(t, func(*hbb.SecureConn) error { return errors.New("usage error contacted rendezvous") })
		result := h.run(t, "screenshot", fixtureID, "--json", "--token", fixtureToken, "--password", fixturePassword,
			"--server", unused.addr, "--key", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", "--timeout", "100ms")
		result.requireCode(t, 2)
		if result.stdout != "" || !strings.Contains(result.stderr, "requires --output") {
			t.Fatalf("unexpected usage output: stdout=%q stderr=%q", result.stdout, result.stderr)
		}
	})

	for _, status := range []int{0, 27} {
		t.Run(fmt.Sprintf("shell exit %d", status), func(t *testing.T) {
			fixture := newPeerFixture(t, pb.ConnType_TERMINAL, "", shellResponse(status))
			result := fixture.run(t, h, "sh", fixtureID, "--command-timeout", "2s", "--", fmt.Sprintf("printf integration-shell-output; exit %d", status))
			result.requireCode(t, status)
			if result.stdout != "integration-shell-output\n" {
				t.Fatalf("shell output or completion marker leaked: %q", result.stdout)
			}
			if status == 0 && result.stderr != "" {
				t.Fatalf("unexpected stderr: %s", result.stderr)
			}
			if status != 0 && !strings.Contains(result.stderr, "status 27") {
				t.Fatalf("missing shell failure diagnostic: %s", result.stderr)
			}
		})
	}
	runTransferCases(t, h)
}

func screenshotResponse(data []byte) func(*hbb.SecureConn) error {
	return func(sc *hbb.SecureConn) error {
		message := &pb.Message{}
		if err := readProto(sc, message); err != nil {
			return err
		}
		capture := message.GetMisc().GetCaptureDisplays()
		if capture == nil || len(capture.GetSet()) != 1 || capture.GetSet()[0] != 0 || len(capture.GetAdd()) != 0 || len(capture.GetSub()) != 0 {
			return errors.New("expected display 0 subscription")
		}
		if err := readProto(sc, message); err != nil {
			return err
		}
		request := message.GetScreenshotRequest()
		if request == nil || request.GetSid() == "" || request.GetDisplay() != 0 {
			return errors.New("expected screenshot request for display 0")
		}
		return writeProto(sc, &pb.Message{Union: &pb.Message_ScreenshotResponse{ScreenshotResponse: &pb.ScreenshotResponse{
			Sid: request.GetSid(), Data: data,
		}}})
	}
}

func shellResponse(status int) func(*hbb.SecureConn) error {
	return func(sc *hbb.SecureConn) error {
		message := &pb.Message{}
		if err := readProto(sc, message); err != nil {
			return err
		}
		if message.GetTerminalAction().GetOpen() == nil {
			return unexpectedMessage(message)
		}
		if err := writeProto(sc, &pb.Message{Union: &pb.Message_TerminalResponse{TerminalResponse: &pb.TerminalResponse{
			Union: &pb.TerminalResponse_Opened{Opened: &pb.TerminalOpened{TerminalId: 42, Success: true}},
		}}}); err != nil {
			return err
		}
		if err := readProto(sc, message); err != nil {
			return err
		}
		input := message.GetTerminalAction().GetData()
		if input == nil || input.GetTerminalId() != 42 || !bytes.Contains(input.GetData(), []byte(fmt.Sprintf("printf integration-shell-output; exit %d", status))) {
			return errors.New("missing one-shot command")
		}
		token := regexp.MustCompile(`rdcli-[0-9a-f]{48}`).Find(input.GetData())
		if token == nil {
			return errors.New("missing one-shot completion token")
		}
		payload := fmt.Sprintf("\x1e%s:start\x1fintegration-shell-output\n\x1e%s:exit:%d\x1f", token, token, status)
		for _, chunk := range []string{string(input.GetData()), payload[:11], payload[11 : len(payload)-2], payload[len(payload)-2:]} {
			if err := writeProto(sc, &pb.Message{Union: &pb.Message_TerminalResponse{TerminalResponse: &pb.TerminalResponse{
				Union: &pb.TerminalResponse_Data{Data: &pb.TerminalData{TerminalId: 42, Data: []byte(chunk)}},
			}}}); err != nil {
				return err
			}
		}
		if err := readProto(sc, message); err != nil {
			return err
		}
		if message.GetTerminalAction().GetClose().GetTerminalId() != 42 {
			return errors.New("missing terminal close")
		}
		return nil
	}
}

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/4nkitd/rustdesk-cli/internal/hbb"
	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func TestScreenshotOutputNoClobberAndOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "capture.png")
	root, name, err := screenshotOutputRoot(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := writeScreenshotFile(root, name, []byte("first"), false); err != nil {
		t.Fatal(err)
	}
	if err := writeScreenshotFile(root, name, []byte("second"), false); err == nil {
		t.Fatal("overwrote without permission")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "first" {
		t.Fatalf("data = %q, err = %v", data, err)
	}
	if err := writeScreenshotFile(root, name, []byte("replacement"), true); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "replacement" {
		t.Fatalf("data = %q, err = %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("insecure permissions: %v", info.Mode())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary file leak: %v, %v", entries, err)
	}
}

func TestScreenshotConcurrentNoClobber(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.png")
	root, name, err := screenshotOutputRoot(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, contents := range []string{"first", "second"} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- writeScreenshotFile(root, name, []byte(contents), false)
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful publishers = %d, want 1", succeeded)
	}
	data, err := os.ReadFile(path)
	if err != nil || (string(data) != "first" && string(data) != "second") {
		t.Fatalf("output = %q, error = %v", data, err)
	}
}

func TestScreenshotRejectsSymlinkAndDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, dir + string(filepath.Separator), link} {
		for _, overwrite := range []bool{false, true} {
			root, _, err := screenshotOutputRoot(path, overwrite)
			if root != nil {
				root.Close()
			}
			if err == nil {
				t.Fatalf("accepted %q with overwrite=%v", path, overwrite)
			}
		}
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "keep" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
}

func TestScreenshotValidationBeforeConnect(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RDC_CONFIG", filepath.Join(dir, "config.toml"))
	existing := filepath.Join(dir, "exists.png")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"123456789"}, "requires --output"},
		{[]string{"123456789", "--output", "-"}, "stdout is not supported"},
		{[]string{"123456789", "--output", existing, "--display", "-1"}, "non-negative"},
		{[]string{"123456789", "--output", existing, "--yes"}, "--overwrite"},
	} {
		cmd := newScreenshotCmd()
		registerCommonFlags(cmd)
		cmd.SetArgs(tc.args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("args %v: error = %v", tc.args, err)
		}
	}
}

func TestScreenshotCommandMockJSONAndPNG(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RDC_CONFIG", filepath.Join(dir, "config.toml"))
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	data := pngBuf.Bytes()
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
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		sc := hbb.NewSecureConn(conn)
		read := func() (*pb.Message, error) {
			data, err := sc.ReadMessage()
			if err != nil {
				return nil, err
			}
			msg := &pb.Message{}
			return msg, proto.Unmarshal(data, msg)
		}
		write := func(msg *pb.Message) error {
			data, err := proto.Marshal(msg)
			if err != nil {
				return err
			}
			_, err = sc.Write(data)
			return err
		}
		if _, err := read(); err != nil {
			done <- err
			return
		}
		if err := write(&pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Salt: "mock", Challenge: "test"}}}); err != nil {
			done <- err
			return
		}
		login, err := read()
		if err != nil {
			done <- err
			return
		}
		if login.GetLoginRequest() == nil || login.GetLoginRequest().GetUnion() != nil {
			done <- errors.New("expected default login")
			return
		}
		option := login.GetLoginRequest().GetOption()
		if option.GetDisableClipboard() != pb.OptionMessage_Yes || option.GetDisableAudio() != pb.OptionMessage_Yes ||
			option.GetDisableCamera() != pb.OptionMessage_Yes || option.GetEnableFileTransfer() != pb.OptionMessage_No {
			done <- errors.New("unrelated services must be disabled in login options")
			return
		}
		if err := write(&pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: &pb.LoginResponse{Union: &pb.LoginResponse_PeerInfo{
			PeerInfo: &pb.PeerInfo{Version: "1.4.1", Displays: []*pb.DisplayInfo{{Width: 2, Height: 3}}},
		}}}}); err != nil {
			done <- err
			return
		}
		request, err := read()
		if err != nil {
			done <- err
			return
		}
		capture := request.GetMisc().GetCaptureDisplays()
		if capture == nil || len(capture.GetSet()) != 1 || capture.GetSet()[0] != 0 || len(capture.GetAdd()) != 0 || len(capture.GetSub()) != 0 {
			done <- errors.New("expected display 0 subscription before screenshot request")
			return
		}
		request, err = read()
		if err != nil {
			done <- err
			return
		}
		if request.GetScreenshotRequest() == nil || request.GetScreenshotRequest().GetSid() == "" {
			done <- errors.New("missing screenshot request")
			return
		}
		done <- write(&pb.Message{Union: &pb.Message_ScreenshotResponse{ScreenshotResponse: &pb.ScreenshotResponse{
			Sid: request.GetScreenshotRequest().GetSid(), Data: data,
		}}})
	}()
	output := filepath.Join(dir, "screen.png")
	cmd := newScreenshotCmd()
	registerCommonFlags(cmd)
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{listener.Addr().String(), "--yes", "--output", output, "--json", "--timeout", "2s"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(written, data) {
		t.Fatalf("file mismatch: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &metadata); err != nil {
		t.Fatalf("invalid JSON output %q: %v", stdout.String(), err)
	}
	if metadata["format"] != "png" || metadata["width"] != float64(2) || metadata["height"] != float64(3) || metadata["output"] != output {
		t.Fatalf("metadata = %v", metadata)
	}
	if _, ok := metadata["data"]; ok {
		t.Fatal("image data leaked into JSON")
	}
}

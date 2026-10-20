package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

type shSplitReader struct{ chunks [][]byte }

func (r *shSplitReader) Read(p []byte) (int, error) {
	for len(r.chunks) > 0 && len(r.chunks[0]) == 0 {
		r.chunks = r.chunks[1:]
	}
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	r.chunks[0] = r.chunks[0][n:]
	return n, nil
}

func TestShCompletionEveryBoundary(t *testing.T) {
	for _, code := range []string{"0", "7", "127", "255"} {
		wire := []byte("prompt and echoed input\r\n\x1etoken:start\x1fhello\r\nworld\x1etoken:exit:" + code + "\x1ftrailing prompt")
		for i := 0; i <= len(wire); i++ {
			reader := &shSplitReader{chunks: [][]byte{wire[:i], wire[i:]}}
			var output bytes.Buffer
			err := readShCompletion(reader, &output, "token")
			if output.String() != "hello\r\nworld" {
				t.Fatalf("split %d: output %q", i, output.String())
			}
			if code == "0" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var status interface{ ExitCode() int }
				if !errors.As(err, &status) || err.Error() != "remote command exited with status "+code {
					t.Fatalf("split %d: %v", i, err)
				}
			}
		}
	}
}

func TestShCompletionRequiresExplicitStatus(t *testing.T) {
	for _, wire := range []string{
		"prompt\r\n",
		"\x1etoken:start\x1foutput without completion",
		"\x1etoken:start\x1foutput\x1eother:exit:0\x1f",
		"\x1etoken:start\x1foutput\x1etoken:exit:",
	} {
		var output bytes.Buffer
		err := readShCompletion(strings.NewReader(wire), &output, "token")
		if err == nil || !errors.Is(err, io.EOF) {
			t.Fatalf("wire %q: %v", wire, err)
		}
	}
}

func TestShCompletionRejectsInvalidStatus(t *testing.T) {
	for _, status := range []string{"", "-1", "+1", "256", "1000", "x", "0\n"} {
		err := readShCompletion(strings.NewReader("\x1etoken:start\x1f\x1etoken:exit:"+status+"\x1f"), io.Discard, "token")
		if err == nil || !strings.Contains(err.Error(), "invalid") {
			t.Fatalf("status %q: %v", status, err)
		}
	}
}

type shBrokenWriter struct{ err error }

func (w shBrokenWriter) Write(p []byte) (int, error) { return 0, w.err }

func TestShCompletionOutputFailure(t *testing.T) {
	broken := errors.New("output failed")
	wire := "\x1etoken:start\x1fdata\x1etoken:exit:0\x1f"
	if err := readShCompletion(strings.NewReader(wire), shBrokenWriter{broken}, "token"); !errors.Is(err, broken) {
		t.Fatal(err)
	}
	if err := readShCompletion(strings.NewReader(wire), shBrokenWriter{}, "token"); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
}

func TestShCommandScriptActualShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell required")
	}
	for _, tc := range []struct {
		command, output string
		code            int
	}{
		{"printf 'hello'", "hello", 0},
		{"printf '%s' \"single'quote\"; exit 23", "single'quote", 23},
		{"printf 'first\\n'\nprintf 'second'; false", "first\nsecond", 1},
		{"exec sh -c 'exit 19'", "", 19},
		{"sleep 1.1; printf 'after silence'", "after silence", 0},
	} {
		t.Run(tc.command, func(t *testing.T) {
			script := strings.TrimSuffix(shCommandScript(tc.command, "testtoken"), "\r\n")
			wire, err := exec.Command("sh", "-c", script).Output()
			if err != nil {
				t.Fatalf("wrapper: %v", err)
			}
			var output bytes.Buffer
			err = readShCompletion(bytes.NewReader(wire), &output, "testtoken")
			if output.String() != tc.output {
				t.Fatalf("output %q, want %q", output.String(), tc.output)
			}
			if tc.code == 0 {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var status interface{ ExitCode() int }
				if !errors.As(err, &status) || status.ExitCode() != tc.code {
					t.Fatalf("status: %v", err)
				}
			}
		})
	}
}

func TestShFlagsCompletionTimeout(t *testing.T) {
	cmd := newShCmd()
	d, err := cmd.Flags().GetDuration("command-timeout")
	if err != nil || d <= 0 {
		t.Fatalf("timeout %v: %v", d, err)
	}
	if cmd.Flags().Lookup("idle").Deprecated == "" {
		t.Fatal("idle must be deprecated")
	}
}

func TestShCommandDisconnectAndDeadline(t *testing.T) {
	for _, disconnect := range []bool{true, false} {
		t.Run(fmt.Sprint(disconnect), func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			client.SetDeadline(time.Now().Add(100 * time.Millisecond))
			go func() {
				buf := make([]byte, 4096)
				n, err := server.Read(buf)
				if err != nil {
					return
				}
				token := regexp.MustCompile(`rdcli-[0-9a-f]{48}`).Find(buf[:n])
				io.WriteString(server, "echoed wrapper\r\n\x1e"+string(token)+":start\x1fpartial output")
				if disconnect {
					server.Close()
				}
			}()
			var output bytes.Buffer
			err := executeShCommand(client, &output, "sleep 99")
			if err == nil || output.String() != "partial output" {
				t.Fatalf("output %q, err %v", output.String(), err)
			}
			if disconnect {
				if !errors.Is(err, io.EOF) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatal(err)
			}
		})
	}
}

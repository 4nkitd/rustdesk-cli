//go:build unix

package cli

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"

	"github.com/4nkitd/rustdesk-cli/internal/hbb"
	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func tunnelTestPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	return r, w
}

func tunnelTestStdio(t *testing.T) (io.ReadWriteCloser, *os.File, *os.File) {
	t.Helper()
	in, sender := tunnelTestPipe(t)
	receiver, out := tunnelTestPipe(t)
	s, err := newTunnelStdio(in, out)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, sender, receiver
}

func TestTunnelStdioIdleReadCancellation(t *testing.T) {
	s, _, _ := tunnelTestStdio(t)
	done := make(chan error, 1)
	go func() {
		var b [1]byte
		_, err := s.Read(b[:])
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	s.Close()
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("cancelled stdin = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle stdin goroutine did not stop")
	}
}

func TestTunnelStdioRejectsUncancellableFiles(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "regular"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	in, _ := tunnelTestPipe(t)
	_, out := tunnelTestPipe(t)
	for _, files := range [][2]*os.File{{file, out}, {in, file}} {
		beforeIn, beforeOut := tunnelTestFlags(t, files[0]), tunnelTestFlags(t, files[1])
		stdio, err := newTunnelStdio(files[0], files[1])
		if err == nil {
			stdio.Close()
			t.Fatal("accepted uncancellable regular-file stdio")
		}
		if !strings.Contains(err.Error(), "cannot be cancelled safely") {
			t.Fatalf("unclear stdio error: %v", err)
		}
		if tunnelTestFlags(t, files[0]) != beforeIn || tunnelTestFlags(t, files[1]) != beforeOut {
			t.Fatal("failed adapter changed borrowed descriptor flags")
		}
	}
}

func TestTunnelStdioBlockedWriteCancellation(t *testing.T) {
	s, _, _ := tunnelTestStdio(t)
	done := make(chan error, 1)
	go func() {
		_, err := s.Write(bytes.Repeat([]byte("x"), 4<<20))
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	s.Close()
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("cancelled stdout = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked stdout goroutine did not stop")
	}
}

func TestTunnelStdioDataAndEOF(t *testing.T) {
	s, sender, receiver := tunnelTestStdio(t)
	if _, err := sender.Write([]byte("input")); err != nil {
		t.Fatal(err)
	}
	sender.Close()
	got, err := io.ReadAll(s)
	if err != nil || string(got) != "input" {
		t.Fatalf("stdin = %q, %v", got, err)
	}
	if _, err := s.Write([]byte("output")); err != nil {
		t.Fatal(err)
	}
	receiver.SetReadDeadline(time.Now().Add(time.Second))
	got = make([]byte, len("output"))
	if _, err := io.ReadFull(receiver, got); err != nil || string(got) != "output" {
		t.Fatalf("stdout = %q, %v", got, err)
	}
}

func tunnelTestFlags(t *testing.T, file *os.File) int {
	t.Helper()
	raw, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags int
	var flagErr error
	if err := raw.Control(func(fd uintptr) {
		flags, flagErr = unix.FcntlInt(fd, unix.F_GETFL, 0)
	}); err != nil {
		t.Fatal(err)
	}
	if flagErr != nil {
		t.Fatal(flagErr)
	}
	return flags
}

func TestTunnelStdioPreservesBorrowedFiles(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "distinct", true: "shared"}[shared], func(t *testing.T) {
			in, sender := tunnelTestPipe(t)
			receiver, out := tunnelTestPipe(t)
			if shared {
				out = in
			}
			for _, file := range []*os.File{in, out} {
				raw, err := file.SyscallConn()
				if err != nil {
					t.Fatal(err)
				}
				var flagErr error
				if err := raw.Control(func(fd uintptr) {
					flagErr = unix.SetNonblock(int(fd), false)
				}); err != nil || flagErr != nil {
					t.Fatalf("set blocking flags: %v, %v", err, flagErr)
				}
			}
			beforeIn, beforeOut := tunnelTestFlags(t, in), tunnelTestFlags(t, out)
			s, err := newTunnelStdio(in, out)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if tunnelTestFlags(t, in) != beforeIn || tunnelTestFlags(t, out) != beforeOut {
				t.Fatal("borrowed descriptor flags changed")
			}
			if _, err := sender.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			var b [1]byte
			if _, err := io.ReadFull(in, b[:]); err != nil || b[0] != 'x' {
				t.Fatalf("original stdin no longer usable: %q, %v", b, err)
			}
			if !shared {
				if _, err := out.Write([]byte("y")); err != nil {
					t.Fatal(err)
				}
				if _, err := io.ReadFull(receiver, b[:]); err != nil || b[0] != 'y' {
					t.Fatalf("original stdout no longer usable: %q, %v", b, err)
				}
			}
		})
	}
}

func tunnelTestRemote(t *testing.T) (*hbb.Tunnel, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	peer := make(chan net.Conn, 1)
	fail := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			fail <- err
			return
		}
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		sc := hbb.NewSecureConn(conn)
		if _, err := sc.ReadMessage(); err != nil {
			conn.Close()
			fail <- err
			return
		}
		hash, _ := proto.Marshal(&pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Salt: "salt", Challenge: "challenge"}}})
		if _, err := sc.Write(hash); err != nil {
			conn.Close()
			fail <- err
			return
		}
		if _, err := sc.ReadMessage(); err != nil {
			conn.Close()
			fail <- err
			return
		}
		login, _ := proto.Marshal(&pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: &pb.LoginResponse{
			Union: &pb.LoginResponse_PeerInfo{PeerInfo: &pb.PeerInfo{Version: "1.4.1"}},
		}}})
		if _, err := sc.Write(login); err != nil {
			conn.Close()
			fail <- err
			return
		}
		peer <- conn
	}()
	tunnel, err := hbb.NewTunnel(hbb.DialOpts{PeerID: ln.Addr().String(), Yes: true, Timeout: 2 * time.Second}, "localhost", 80)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tunnel.Close() })
	select {
	case conn := <-peer:
		t.Cleanup(func() { conn.Close() })
		return tunnel, conn
	case err := <-fail:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("mock peer did not finish login")
	}
	return nil, nil
}

func TestTunnelStdioRemoteAndLocalFirstCompletion(t *testing.T) {
	for _, side := range []string{"remote", "local"} {
		t.Run(side, func(t *testing.T) {
			tunnel, peer := tunnelTestRemote(t)
			s, sender, _ := tunnelTestStdio(t)
			done := make(chan error, 1)
			go func() { done <- tunnel.Pipe(s) }()
			if side == "remote" {
				peer.Close()
			} else {
				sender.Close()
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("orderly close returned %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("tunnel left a stdio copy blocked")
			}
		})
	}
}

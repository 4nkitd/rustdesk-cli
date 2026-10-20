package hbb

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func terminalCompletionPipe(t *testing.T) (*TerminalSession, *Conn) {
	t.Helper()
	client, server := net.Pipe()
	ts := &TerminalSession{conn: &Conn{sc: NewSecureConn(client)}, termID: 42}
	peer := &Conn{sc: NewSecureConn(server)}
	t.Cleanup(func() { client.Close(); server.Close() })
	return ts, peer
}

func terminalCompletionMessage(response *pb.TerminalResponse) *pb.Message {
	return &pb.Message{Union: &pb.Message_TerminalResponse{TerminalResponse: response}}
}

func TestTerminalCompletionRequiresAdvertisedFeature(t *testing.T) {
	for _, tc := range []struct {
		name string
		info *pb.PeerInfo
		want bool
	}{
		{"missing peer info", nil, false},
		{"version alone", &pb.PeerInfo{Version: "1.5.0"}, false},
		{"explicitly disabled", &pb.PeerInfo{Version: "1.5.0", Features: &pb.Features{}}, false},
		{"advertised", &pb.PeerInfo{Version: "1.4.1", Features: &pb.Features{Terminal: true}}, true},
		{"feature without version", &pb.PeerInfo{Features: &pb.Features{Terminal: true}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := terminalSupported(tc.info); got != tc.want {
				t.Fatalf("support = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTerminalCompletionFiltersIDsAndKeepsExitCode(t *testing.T) {
	ts, peer := terminalCompletionPipe(t)
	go func() {
		defer peer.Close()
		for _, response := range []*pb.TerminalResponse{
			{Union: &pb.TerminalResponse_Data{Data: &pb.TerminalData{TerminalId: 99, Data: []byte("wrong")}}},
			{Union: &pb.TerminalResponse_Error{Error: &pb.TerminalError{TerminalId: 99, Message: "wrong error"}}},
			{Union: &pb.TerminalResponse_Closed{Closed: &pb.TerminalClosed{TerminalId: 99, ExitCode: 0}}},
			{Union: &pb.TerminalResponse_Data{Data: &pb.TerminalData{TerminalId: 42, Data: []byte("correct")}}},
			{Union: &pb.TerminalResponse_Closed{Closed: &pb.TerminalClosed{TerminalId: 42, ExitCode: 37}}},
		} {
			if err := peer.Send(terminalCompletionMessage(response)); err != nil {
				return
			}
		}
	}()
	output, err := io.ReadAll(ts)
	var status interface{ ExitCode() int }
	if string(output) != "correct" || !errors.As(err, &status) || status.ExitCode() != 37 {
		t.Fatalf("output %q, err %v", output, err)
	}
}

func TestTerminalCompletionDisconnectIsNotCleanEOF(t *testing.T) {
	ts, peer := terminalCompletionPipe(t)
	peer.Close()
	_, err := ts.Read(make([]byte, 1))
	if err == nil || err == io.EOF || !strings.Contains(err.Error(), "disconnected") {
		t.Fatal(err)
	}
}

func TestTerminalCompletionCloseAfterRemoteExitClosesTransport(t *testing.T) {
	ts, peer := terminalCompletionPipe(t)
	go peer.Send(terminalCompletionMessage(&pb.TerminalResponse{Union: &pb.TerminalResponse_Closed{Closed: &pb.TerminalClosed{TerminalId: 42}}}))
	if _, err := ts.Read(make([]byte, 1)); err != io.EOF {
		t.Fatal(err)
	}
	if err := ts.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.Recv(); err != io.EOF {
		t.Fatalf("connection still open: %v", err)
	}
	if err := ts.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalCompletionDeadlineIgnoresUnrelatedMessages(t *testing.T) {
	ts, peer := terminalCompletionPipe(t)
	ts.SetDeadline(time.Now().Add(30 * time.Millisecond))
	go func() {
		for {
			if err := peer.Send(terminalCompletionMessage(&pb.TerminalResponse{Union: &pb.TerminalResponse_Data{Data: &pb.TerminalData{TerminalId: 99, Data: []byte("unrelated")}}})); err != nil {
				return
			}
		}
	}()
	_, err := ts.Read(make([]byte, 1))
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("deadline error: %v", err)
	}
}

func TestTerminalCompletionRejectsCorruptAndOversizedData(t *testing.T) {
	for _, data := range []*pb.TerminalData{
		{Compressed: true, Data: []byte("not zstd")},
		{Data: bytes.Repeat([]byte("x"), maxTerminalBufferedOutput+1)},
		{Compressed: true, Data: zstdEnc.EncodeAll(bytes.Repeat([]byte("x"), maxTerminalBufferedOutput+1), nil)},
	} {
		if _, err := decodeTerminalData(data); err == nil {
			t.Fatal("accepted invalid terminal output")
		}
	}
}

func TestTerminalCompletionBoundsPreOpenOutput(t *testing.T) {
	ts, peer := terminalCompletionPipe(t)
	go func() {
		defer peer.Close()
		if _, err := peer.Recv(); err != nil {
			return
		}
		for i := 0; i < 3; i++ {
			if err := peer.Send(terminalCompletionMessage(&pb.TerminalResponse{Union: &pb.TerminalResponse_Data{Data: &pb.TerminalData{Data: bytes.Repeat([]byte("x"), maxTerminalBufferedOutput/2)}}})); err != nil {
				return
			}
		}
	}()
	_, err := openTerminalOnConn(ts.conn, 24, 80)
	if err == nil || !strings.Contains(err.Error(), "buffer limit") {
		t.Fatal(err)
	}
}

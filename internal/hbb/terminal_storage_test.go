package hbb

import (
	"bytes"
	"encoding/binary"
	"testing"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func terminalStorageSkippableFrame(output []byte) []byte {
	frame := []byte{0x28, 0xb5, 0x2f, 0xfd, 0x20, 0, 1, 0, 0}
	if len(output) > 0 {
		frame = zstdEnc.EncodeAll(output, nil)
	}
	const padding = 512 << 10
	data := make([]byte, len(frame)+8+padding)
	copy(data, frame)
	binary.LittleEndian.PutUint32(data[len(frame):], 0x184d2a50)
	binary.LittleEndian.PutUint32(data[len(frame)+4:], padding)
	return data
}

func TestTerminalStorageSkippableDecodeRetention(t *testing.T) {
	for _, output := range []string{"", "x"} {
		t.Run(output, func(t *testing.T) {
			frame := terminalStorageSkippableFrame([]byte(output))
			var retained [4][]byte
			for i := range retained {
				data, err := decodeTerminalData(&pb.TerminalData{Compressed: true, Data: frame})
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != output {
					t.Fatalf("output = %q, want %q", data, output)
				}
				retained[i] = data
			}
			capacity := 0
			for _, data := range retained {
				capacity += cap(data)
				if output == "" && data != nil {
					t.Fatal("empty output retained decoder storage")
				}
			}
			if capacity != len(retained)*len(output) {
				t.Fatalf("four payloads retained capacity %d, want %d", capacity, len(retained)*len(output))
			}
		})
	}
}

func TestTerminalStoragePreOpenSkippableOutput(t *testing.T) {
	ts, peer := terminalCompletionPipe(t)
	empty := terminalStorageSkippableFrame(nil)
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			if _, err := peer.Recv(); err != nil {
				return err
			}
			for i := 0; i < 4; i++ {
				if err := peer.Send(terminalCompletionMessage(&pb.TerminalResponse{Union: &pb.TerminalResponse_Data{Data: &pb.TerminalData{TerminalId: 42, Compressed: true, Data: empty}}})); err != nil {
					return err
				}
			}
			if err := peer.Send(terminalCompletionMessage(&pb.TerminalResponse{Union: &pb.TerminalResponse_Data{Data: &pb.TerminalData{TerminalId: 42, Data: []byte("ok")}}})); err != nil {
				return err
			}
			return peer.Send(terminalCompletionMessage(&pb.TerminalResponse{Union: &pb.TerminalResponse_Opened{Opened: &pb.TerminalOpened{TerminalId: 42, Success: true}}}))
		}()
	}()
	opened, err := openTerminalOnConn(ts.conn, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if string(opened.buf) != "ok" || cap(opened.buf) != 2 {
		t.Fatalf("pre-open buffer = %q, capacity %d", opened.buf, cap(opened.buf))
	}
}

func TestTerminalStorageReadReleasesBuffer(t *testing.T) {
	ts, peer := terminalCompletionPipe(t)
	empty := terminalStorageSkippableFrame(nil)
	tiny := terminalStorageSkippableFrame([]byte("ok"))
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			data := &pb.TerminalData{TerminalId: 42, Data: bytes.Repeat([]byte("x"), 64<<10)}
			if err := peer.Send(terminalCompletionMessage(&pb.TerminalResponse{Union: &pb.TerminalResponse_Data{Data: data}})); err != nil {
				return err
			}
			for i := 0; i < 4; i++ {
				if err := peer.Send(terminalCompletionMessage(&pb.TerminalResponse{Union: &pb.TerminalResponse_Data{Data: &pb.TerminalData{TerminalId: 42, Compressed: true, Data: empty}}})); err != nil {
					return err
				}
			}
			return peer.Send(terminalCompletionMessage(&pb.TerminalResponse{Union: &pb.TerminalResponse_Data{Data: &pb.TerminalData{TerminalId: 42, Compressed: true, Data: tiny}}}))
		}()
	}()
	if n, err := ts.Read(make([]byte, 64<<10)); err != nil || n != 64<<10 {
		t.Fatalf("initial output: n=%d, error=%v", n, err)
	}
	if ts.buf != nil {
		t.Fatal("drained buffer still retains its backing allocation")
	}
	var first [1]byte
	if n, err := ts.Read(first[:]); err != nil || n != 1 || first[0] != 'o' {
		t.Fatalf("tiny output: n=%d, byte=%q, error=%v", n, first, err)
	}
	if string(ts.buf) != "k" || cap(ts.buf) != 1 {
		t.Fatalf("remaining output = %q, capacity %d", ts.buf, cap(ts.buf))
	}
	if n, err := ts.Read(first[:]); err != nil || n != 1 || first[0] != 'k' {
		t.Fatalf("last output: n=%d, byte=%q, error=%v", n, first, err)
	}
	if ts.buf != nil {
		t.Fatal("drained tiny buffer still retains its backing allocation")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

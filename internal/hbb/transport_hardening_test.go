package hbb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

type hardeningConn struct {
	net.Conn
	reader io.Reader
	writer io.Writer
	reads  int
}

func (c *hardeningConn) Read(p []byte) (int, error) {
	c.reads++
	return c.reader.Read(p)
}

func (c *hardeningConn) Write(p []byte) (int, error) {
	return c.writer.Write(p)
}

type hardeningWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	chunk  int
	failAt int
	err    error
	calls  int
}

func (w *hardeningWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.calls++
	n := min(len(p), w.chunk)
	var err error
	if w.err != nil && w.buf.Len()+n >= w.failAt {
		n = max(0, w.failAt-w.buf.Len())
		err = w.err
	}
	w.buf.Write(p[:n])
	w.mu.Unlock()
	runtime.Gosched()
	return n, err
}

func TestTransportConcurrentWrites(t *testing.T) {
	for _, mode := range []string{"framed", "encrypted", "raw"} {
		t.Run(mode, func(t *testing.T) {
			writer := &hardeningWriter{chunk: 3}
			sender := NewSecureConn(&hardeningConn{writer: writer})
			key := [32]byte{7, 8, 9}
			if mode == "encrypted" {
				sender.SetKey(key)
			} else if mode == "raw" {
				sender.SetRaw()
			}
			const count = 96
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := 0; i < count; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					msg := []byte(fmt.Sprintf("message-%03d", i))
					if n, err := sender.Write(msg); err != nil || n != len(msg) {
						t.Errorf("Write = %d, %v", n, err)
					}
				}(i)
			}
			close(start)
			wg.Wait()
			receiver := NewSecureConn(&hardeningConn{reader: bytes.NewReader(writer.buf.Bytes())})
			if mode == "encrypted" {
				receiver.SetKey(key)
			} else if mode == "raw" {
				receiver.SetRaw()
			}
			seen := make(map[string]bool)
			for i := 0; i < count; i++ {
				var msg []byte
				var err error
				if mode == "raw" {
					msg = make([]byte, len("message-000"))
					_, err = io.ReadFull(receiver, msg)
				} else {
					msg, err = receiver.ReadMessage()
				}
				if err != nil {
					t.Fatalf("read message %d: %v", i, err)
				}
				seen[string(msg)] = true
			}
			for i := 0; i < count; i++ {
				if !seen[fmt.Sprintf("message-%03d", i)] {
					t.Fatalf("missing or interleaved message %d", i)
				}
			}
			if _, err := receiver.Read(make([]byte, 1)); err != io.EOF {
				t.Fatalf("trailing data: %v", err)
			}
		})
	}
}

func TestTransportWriteFailures(t *testing.T) {
	failure := errors.New("write failed")
	for _, encrypted := range []bool{false, true} {
		for _, zero := range []bool{false, true} {
			t.Run(fmt.Sprintf("encrypted=%t/zero=%t", encrypted, zero), func(t *testing.T) {
				writer := &hardeningWriter{chunk: 3, failAt: 5, err: failure}
				wantErr := failure
				if zero {
					writer.chunk = 0
					writer.err = nil
					wantErr = io.ErrShortWrite
				}
				sender := NewSecureConn(&hardeningConn{writer: writer})
				if encrypted {
					sender.SetKey([32]byte{1})
				}
				if n, err := sender.Write([]byte("a complete message")); n != 0 || !errors.Is(err, wantErr) {
					t.Fatalf("Write = %d, %v", n, err)
				}
				calls := writer.calls
				if n, err := sender.Write([]byte("next message")); n != 0 || !errors.Is(err, wantErr) {
					t.Fatalf("second Write = %d, %v", n, err)
				}
				if writer.calls != calls {
					t.Fatal("wrote to a broken framed stream")
				}
				if encrypted && sender.encryptor.sendCount != 1 {
					t.Fatal("advanced nonce after failed frame")
				}
			})
		}
	}
	writer := &hardeningWriter{chunk: 3, failAt: 5, err: failure}
	sender := NewSecureConn(&hardeningConn{writer: writer})
	sender.SetRaw()
	if n, err := sender.Write([]byte("raw bytes")); n != 5 || !errors.Is(err, failure) {
		t.Fatalf("raw Write = %d, %v", n, err)
	}
}

func hardeningHeader(size int) []byte {
	header := make([]byte, 4)
	binary.LittleEndian.PutUint32(header, uint32(size)<<2|3)
	return header
}

type hardeningReader struct {
	data  []byte
	chunk int
}

func (r *hardeningReader) Read(p []byte) (int, error) {
	n := copy(p, r.data[:min(len(r.data), r.chunk)])
	r.data = r.data[n:]
	if len(r.data) == 0 {
		return n, io.EOF
	}
	return n, nil
}

func TestTransportRejectsOversizedHeaders(t *testing.T) {
	for _, size := range []int{maxFramePayloadLength + 1, 0x3fffffff} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("size=%d/stream=%t", size, stream), func(t *testing.T) {
				conn := &hardeningConn{reader: &hardeningReader{data: hardeningHeader(size), chunk: 1}}
				receiver := NewSecureConn(conn)
				for i := 0; i < 2; i++ {
					var err error
					if stream {
						_, err = receiver.Read(make([]byte, 1))
					} else {
						_, err = receiver.ReadMessage()
					}
					if err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
						t.Fatalf("expected frame limit error, got %v", err)
					}
				}
				if conn.reads != 4 || len(receiver.readBuf) != 4 {
					t.Fatalf("continued accumulating oversized frame: reads=%d buffer=%d", conn.reads, len(receiver.readBuf))
				}
			})
		}
	}
}

func TestTransportCodecLimitCompatibility(t *testing.T) {
	codec := BytesCodec{maxPacketLength: maxFramePayloadLength}
	buf := hardeningHeader(maxFramePayloadLength)
	if _, ok, err := codec.decodeFrame(&buf); ok || err != nil {
		t.Fatalf("maximum-sized incomplete frame = %t, %v", ok, err)
	}
	buf = hardeningHeader(maxFramePayloadLength + 1)
	original := bytes.Clone(buf)
	if _, ok := codec.DecodeFrame(&buf); ok || !bytes.Equal(buf, original) {
		t.Fatal("public decoder must leave oversized frames unconsumed")
	}
	codec.SetMaxPacketLength(0)
	if _, ok, err := codec.decodeFrame(&buf); ok || err != nil {
		t.Fatalf("unlimited codec = %t, %v", ok, err)
	}
}

func TestTransportReadsDataWithEOF(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			wire := append(Encode([]byte("first")), Encode([]byte("second"))...)
			receiver := NewSecureConn(&hardeningConn{reader: &hardeningReader{data: wire, chunk: len(wire)}})
			for _, want := range []string{"first", "second"} {
				var msg []byte
				var err error
				if stream {
					msg = make([]byte, len(want))
					_, err = io.ReadFull(receiver, msg)
				} else {
					msg, err = receiver.ReadMessage()
				}
				if err != nil || string(msg) != want {
					t.Fatalf("read = %q, %v; want %q", msg, err, want)
				}
			}
			if _, err := receiver.ReadMessage(); err != io.EOF {
				t.Fatalf("expected deferred EOF, got %v", err)
			}
		})
	}
}

func TestTransportLargeEncryptedMessage(t *testing.T) {
	key := [32]byte{1, 2, 3}
	payload := bytes.Repeat([]byte{42}, 8<<20)
	var wire bytes.Buffer
	sender := NewSecureConn(&hardeningConn{writer: &wire})
	sender.SetKey(key)
	if n, err := sender.Write(payload); n != len(payload) || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	receiver := NewSecureConn(&hardeningConn{reader: &wire})
	receiver.SetKey(key)
	got, err := receiver.ReadMessage()
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("large encrypted roundtrip: bytes=%d, err=%v", len(got), err)
	}
}

func TestTransportRejectsOversizedWritesBeforeEncryption(t *testing.T) {
	payload := make([]byte, maxFramePayloadLength+1)
	for _, encrypted := range []bool{false, true} {
		writer := &hardeningWriter{chunk: 64}
		sender := NewSecureConn(&hardeningConn{writer: writer})
		msg := payload
		if encrypted {
			sender.SetKey([32]byte{1})
			msg = payload[:maxFramePayloadLength]
		}
		if n, err := sender.Write(msg); n != 0 || err == nil {
			t.Fatalf("oversized Write = %d, %v", n, err)
		}
		if writer.calls != 0 || encrypted && sender.encryptor.sendCount != 0 {
			t.Fatal("oversized write touched transport or nonce")
		}
		if n, err := sender.Write([]byte("valid")); n != 5 || err != nil {
			t.Fatalf("valid write after rejected payload = %d, %v", n, err)
		}
	}
}

func TestTransportHandshakeShortWrites(t *testing.T) {
	local, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	remote, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	writer := &hardeningWriter{chunk: 3}
	conn := &hardeningConn{reader: bytes.NewReader(remote.Pub), writer: writer}
	got, err := SecureHandshake(conn, local, nil)
	if err != nil {
		t.Fatal(err)
	}
	want, err := DeriveSharedSecret(remote.Priv, local.Pub)
	if err != nil || got != want || !bytes.Equal(writer.buf.Bytes(), local.Pub) {
		t.Fatalf("short-write handshake mismatch: %v", err)
	}
}

func TestTransportRawTransitionPreservesCoalescedBanner(t *testing.T) {
	login := &pb.Message{Union: &pb.Message_LoginResponse{
		LoginResponse: &pb.LoginResponse{Union: &pb.LoginResponse_PeerInfo{
			PeerInfo: &pb.PeerInfo{Version: "1.4.1", Platform: "Linux"},
		}},
	}}
	loginBytes, err := proto.Marshal(login)
	if err != nil {
		t.Fatal(err)
	}
	banner := []byte("SSH-2.0-OpenSSH_9.9\r\n")
	for _, encrypted := range []bool{false, true} {
		t.Run(fmt.Sprintf("encrypted=%t", encrypted), func(t *testing.T) {
			key := [32]byte{1, 2, 3}
			payload := loginBytes
			if encrypted {
				payload = NewEncryptor(key).Encrypt(payload)
			}
			wire := append(Encode(payload), banner...)
			conn := &hardeningConn{reader: &hardeningReader{data: wire, chunk: len(wire)}}
			receiver := NewSecureConn(conn)
			if encrypted {
				receiver.SetKey(key)
			}
			got, err := receiver.ReadMessage()
			if err != nil || !bytes.Equal(got, loginBytes) {
				t.Fatalf("login response = %x, %v", got, err)
			}
			receiver.SetRaw()
			var gotBanner bytes.Buffer
			buf := make([]byte, 3)
			for {
				n, err := receiver.Read(buf)
				gotBanner.Write(buf[:n])
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Equal(gotBanner.Bytes(), banner) || conn.reads != 1 {
				t.Fatalf("banner = %q, transport reads = %d", gotBanner.Bytes(), conn.reads)
			}
		})
	}
}

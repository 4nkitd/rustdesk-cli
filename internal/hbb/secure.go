package hbb

import (
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/nacl/secretbox"
)

type streamMode int

const (
	modeFramed streamMode = iota
	modeEncrypted
	modeRaw
)

// Limit wire payloads, including encryption overhead, while allowing large
// screenshots and file-transfer messages. The wire format itself allows 1 GiB.
const maxFramePayloadLength = 64 << 20

// SecureConn is a framed stream that can be plaintext, secretbox-encrypted,
// or raw (unframed). It mirrors hbb_common tcp::FramedStream.
// Writes may run concurrently with each other and with a single reader.
// Callers must serialize reads and keep SetKey/SetRaw exclusive of all I/O
// and mode inspection. Underlying must not be used for concurrent direct I/O.
type SecureConn struct {
	conn      net.Conn
	mode      streamMode
	encryptor *Encryptor
	readBuf   []byte
	decBuf    []byte
	readErr   error
	writeMu   sync.Mutex
	writeErr  error
}

func NewSecureConn(conn net.Conn) *SecureConn {
	return &SecureConn{
		conn: conn,
		mode: modeFramed,
	}
}

// SetKey switches from plaintext framing to encrypted framing. The given key
// is used for both directions; counters start at 1 as in tcp.rs.
func (s *SecureConn) SetKey(key [32]byte) {
	s.encryptor = NewEncryptor(key)
	s.mode = modeEncrypted
}

func (s *SecureConn) SetRaw() {
	s.mode = modeRaw
	s.encryptor = nil
	s.decBuf = nil
}

func (s *SecureConn) IsSecured() bool {
	return s.mode == modeEncrypted
}

func (s *SecureConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	for {
		if len(s.decBuf) > 0 {
			n := copy(p, s.decBuf)
			s.decBuf = s.decBuf[n:]
			return n, nil
		}

		if s.mode == modeRaw {
			if len(s.readBuf) > 0 {
				n := copy(p, s.readBuf)
				s.readBuf = s.readBuf[n:]
				return n, nil
			}
			if s.readErr != nil {
				err := s.readErr
				s.readErr = nil
				return 0, err
			}
			return s.conn.Read(p)
		}

		frame, err := s.readFrame()
		if err != nil {
			return 0, err
		}
		if err := s.decryptFrame(frame); err != nil {
			return 0, err
		}
	}
}

func (s *SecureConn) decryptFrame(frame []byte) error {
	if s.mode == modeEncrypted {
		if len(frame) <= 1 {
			// hbb_common tcp.rs skips empty/1-byte frames without
			// decrypting them or advancing the nonce counter.
			s.decBuf = frame
			return nil
		}
		plaintext, err := s.encryptor.Decrypt(frame)
		if err != nil {
			return err
		}
		s.decBuf = plaintext
		return nil
	}
	s.decBuf = frame
	return nil
}

func (s *SecureConn) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if s.writeErr != nil {
		return 0, s.writeErr
	}
	if s.mode == modeRaw {
		return writeFull(s.conn, p)
	}
	maxLen := maxFramePayloadLength
	if s.mode == modeEncrypted {
		maxLen -= secretbox.Overhead
	}
	if len(p) > maxLen {
		return 0, fmt.Errorf("hbb: message length %d exceeds maximum %d", len(p), maxLen)
	}
	payload := p
	if s.mode == modeEncrypted {
		payload = s.encryptor.Encrypt(p)
	}
	if _, err := writeFull(s.conn, Encode(payload)); err != nil {
		// A partial frame or advanced nonce makes further framed writes unsafe.
		s.writeErr = err
		return 0, err
	}
	return len(p), nil
}

func writeFull(w io.Writer, p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n, err := w.Write(p)
		total += n
		p = p[n:]
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}

// ReadMessage reads one full frame and returns it decrypted/plain as raw bytes.
func (s *SecureConn) ReadMessage() ([]byte, error) {
	frame, err := s.readFrame()
	if err != nil {
		return nil, err
	}
	if s.mode == modeEncrypted && len(frame) > 1 {
		return s.encryptor.Decrypt(frame)
	}
	return frame, nil
}

func (s *SecureConn) readFrame() ([]byte, error) {
	codec := BytesCodec{maxPacketLength: maxFramePayloadLength}
	tmp := make([]byte, 4096)
	for {
		frame, ok, err := codec.decodeFrame(&s.readBuf)
		if err != nil {
			return nil, err
		}
		if ok {
			return frame, nil
		}
		if s.readErr != nil {
			err := s.readErr
			s.readErr = nil
			return nil, err
		}
		var n int
		n, s.readErr = s.conn.Read(tmp)
		if n > 0 {
			s.readBuf = append(s.readBuf, tmp[:n]...)
		}
	}
}

func (s *SecureConn) Close() error {
	return s.conn.Close()
}

func (s *SecureConn) LocalAddr() net.Addr {
	return s.conn.LocalAddr()
}

func (s *SecureConn) RemoteAddr() net.Addr {
	return s.conn.RemoteAddr()
}

func (s *SecureConn) SetDeadline(t time.Time) error {
	return s.conn.SetDeadline(t)
}

func (s *SecureConn) SetReadDeadline(t time.Time) error {
	return s.conn.SetReadDeadline(t)
}

func (s *SecureConn) SetWriteDeadline(t time.Time) error {
	return s.conn.SetWriteDeadline(t)
}

func (s *SecureConn) Underlying() net.Conn {
	return s.conn
}

// SecureHandshake is retained as the raw X25519 key-exchange helper used for
// the legacy rendezvous Token stream. It exchanges 32-byte public keys and
// derives the shared secret via NaCl box precompute.
func SecureHandshake(conn net.Conn, keypair KeyPair, theirPub []byte) ([32]byte, error) {
	if len(keypair.Pub) != 32 || len(keypair.Priv) != 32 {
		return [32]byte{}, fmt.Errorf("hbb: invalid local keypair length")
	}
	if len(theirPub) > 0 && len(theirPub) != 32 {
		return [32]byte{}, fmt.Errorf("hbb: invalid remote public key length: %d", len(theirPub))
	}

	if _, err := writeFull(conn, keypair.Pub); err != nil {
		return [32]byte{}, fmt.Errorf("hbb: failed to send public key: %w", err)
	}

	remotePub := make([]byte, 32)
	if _, err := io.ReadFull(conn, remotePub); err != nil {
		return [32]byte{}, fmt.Errorf("hbb: failed to read remote public key: %w", err)
	}

	targetPub := remotePub
	if len(theirPub) == 32 {
		targetPub = theirPub
	}

	return DeriveSharedSecret(keypair.Priv, targetPub)
}

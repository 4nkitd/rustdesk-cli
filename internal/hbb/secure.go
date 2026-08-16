package hbb

import (
	"fmt"
	"io"
	"net"
	"time"
)

type streamMode int

const (
	modeFramed streamMode = iota
	modeEncrypted
	modeRaw
)

// SecureConn is a framed stream that can be plaintext, secretbox-encrypted,
// or raw (unframed). It mirrors hbb_common tcp::FramedStream.
type SecureConn struct {
	conn      net.Conn
	mode      streamMode
	encryptor *Encryptor
	readBuf   []byte
	decBuf    []byte
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
	s.readBuf = nil
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
			return s.conn.Read(p)
		}

		if len(s.readBuf) > 0 {
			frame, ok := DecodeFrame(&s.readBuf)
			if ok {
				if err := s.decryptFrame(frame); err != nil {
					return 0, err
				}
				continue
			}
		}

		buf := make([]byte, 4096)
		n, err := s.conn.Read(buf)
		if n > 0 {
			s.readBuf = append(s.readBuf, buf[:n]...)
		}
		if err != nil {
			if len(s.readBuf) > 0 {
				frame, ok := DecodeFrame(&s.readBuf)
				if ok {
					if decErr := s.decryptFrame(frame); decErr != nil {
						return 0, decErr
					}
					continue
				}
			}
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
	switch s.mode {
	case modeRaw:
		return s.conn.Write(p)
	case modeEncrypted:
		encrypted := s.encryptor.Encrypt(p)
		framed := Encode(encrypted)
		if _, err := s.conn.Write(framed); err != nil {
			return 0, err
		}
		return len(p), nil
	default:
		framed := Encode(p)
		if _, err := s.conn.Write(framed); err != nil {
			return 0, err
		}
		return len(p), nil
	}
}

// ReadMessage reads one full frame and returns it decrypted/plain as raw bytes.
func (s *SecureConn) ReadMessage() ([]byte, error) {
	tmp := make([]byte, 4096)
	for {
		frame, ok := DecodeFrame(&s.readBuf)
		if ok {
			if s.mode == modeEncrypted && len(frame) > 1 {
				plain, err := s.encryptor.Decrypt(frame)
				if err != nil {
					return nil, err
				}
				return plain, nil
			}
			return frame, nil
		}
		n, err := s.conn.Read(tmp)
		if n > 0 {
			s.readBuf = append(s.readBuf, tmp[:n]...)
		}
		if err != nil {
			return nil, err
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

	if _, err := conn.Write(keypair.Pub); err != nil {
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

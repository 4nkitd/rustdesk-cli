package hbb

import (
	"bytes"
	"net"
	"testing"
)

func TestSecureConnRaw(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	var key [32]byte
	s1 := NewSecureConn(c1)
	s2 := NewSecureConn(c2)
	s1.SetKey(key)
	s2.SetKey(key)
	s1.SetRaw()
	s2.SetRaw()

	msg := []byte("hello raw world")

	go func() {
		_, err := s1.Write(msg)
		if err != nil {
			t.Errorf("s1.Write failed: %v", err)
		}
	}()

	buf := make([]byte, 100)
	n, err := s2.Read(buf)
	if err != nil {
		t.Fatalf("s2.Read failed: %v", err)
	}

	if !bytes.Equal(buf[:n], msg) {
		t.Fatalf("got %q, want %q", buf[:n], msg)
	}
}

func TestSecureConnEncryptedRoundtrip(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	key := [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}

	s1 := NewSecureConn(c1)
	s2 := NewSecureConn(c2)
	s1.SetKey(key)
	s2.SetKey(key)

	msg1 := []byte("first encrypted message")
	msg2 := []byte("second encrypted message")

	go func() {
		if _, err := s1.Write(msg1); err != nil {
			t.Errorf("s1.Write msg1 failed: %v", err)
		}
		if _, err := s2.Write(msg2); err != nil {
			t.Errorf("s2.Write msg2 failed: %v", err)
		}
	}()

	buf := make([]byte, 100)
	n1, err := s2.Read(buf)
	if err != nil {
		t.Fatalf("s2.Read msg1 failed: %v", err)
	}
	if !bytes.Equal(buf[:n1], msg1) {
		t.Fatalf("got %q, want %q", buf[:n1], msg1)
	}

	n2, err := s1.Read(buf)
	if err != nil {
		t.Fatalf("s1.Read msg2 failed: %v", err)
	}
	if !bytes.Equal(buf[:n2], msg2) {
		t.Fatalf("got %q, want %q", buf[:n2], msg2)
	}
}

func TestSecureConnPlainFramed(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	s1 := NewSecureConn(c1)
	s2 := NewSecureConn(c2)

	msg := []byte("plain framed message")
	go func() {
		s1.Write(msg)
	}()

	buf := make([]byte, 100)
	n, err := s2.Read(buf)
	if err != nil {
		t.Fatalf("s2.Read failed: %v", err)
	}
	if !bytes.Equal(buf[:n], msg) {
		t.Fatalf("got %q, want %q", buf[:n], msg)
	}
}

func TestSecureHandshake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer ln.Close()

	kp1, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair kp1 failed: %v", err)
	}

	kp2, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair kp2 failed: %v", err)
	}

	var secret1, secret2 [32]byte
	var err1, err2 error

	done := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			err1 = err
			close(done)
			return
		}
		defer conn.Close()
		secret1, err1 = SecureHandshake(conn, kp1, nil)
		close(done)
	}()

	c2, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer c2.Close()
	secret2, err2 = SecureHandshake(c2, kp2, nil)
	<-done

	if err1 != nil {
		t.Fatalf("handshake 1 failed: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("handshake 2 failed: %v", err2)
	}

	if secret1 != secret2 {
		t.Fatalf("handshake secrets mismatch: %x vs %x", secret1, secret2)
	}
}

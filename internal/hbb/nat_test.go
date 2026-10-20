package hbb

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func runNatTestServer(t *testing.T, probe1, probe2 int32) string {
	t.Helper()
	var listeners [2]net.Listener
	var err error
	for attempt := 0; attempt < 100; attempt++ {
		listeners[0], err = net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listeners[0].Addr().(*net.TCPAddr).Port
		listeners[1], err = net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port-1)))
		if err == nil {
			break
		}
		listeners[0].Close()
	}
	if err != nil {
		t.Fatalf("reserve adjacent NAT listeners: %v", err)
	}
	done := make(chan error, 1)
	t.Cleanup(func() {
		for _, ln := range listeners {
			ln.Close()
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("NAT server: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("NAT server did not stop")
		}
	})
	go func() {
		done <- serveNatTest(listeners, [2]int32{probe1, probe2})
	}()
	return listeners[0].Addr().String()
}

func serveNatTest(listeners [2]net.Listener, ports [2]int32) error {
	var first net.Conn
	var conns []net.Conn
	for i, ln := range listeners {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		defer conn.Close()
		conns = append(conns, conn)
		if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			return err
		}
		req, err := readRendezvousMessage(conn)
		if err != nil {
			return err
		}
		if req.GetTestNatRequest() == nil {
			return fmt.Errorf("unexpected NAT request: %T", req.GetUnion())
		}
		if i == 0 {
			first = conn
		} else {
			if conn.RemoteAddr().String() != first.RemoteAddr().String() {
				return fmt.Errorf("probe source changed: %s to %s", first.RemoteAddr(), conn.RemoteAddr())
			}
			if err := first.SetReadDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
				return err
			}
			var b [1]byte
			n, err := first.Read(b[:])
			var timeout net.Error
			if n != 0 || !errors.As(err, &timeout) || !timeout.Timeout() {
				return fmt.Errorf("first probe did not stay open through second request: read %d, %v", n, err)
			}
			if err := first.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
				return err
			}
		}
		if err := writeRendezvousMessage(conn, &pb.RendezvousMessage{
			Union: &pb.RendezvousMessage_TestNatResponse{
				TestNatResponse: &pb.TestNatResponse{Port: ports[i]},
			},
		}); err != nil {
			return err
		}
	}
	for i, conn := range conns {
		var b [1]byte
		if n, err := conn.Read(b[:]); n != 0 || !errors.Is(err, io.EOF) {
			return fmt.Errorf("probe %d not closed after NAT test: read %d, %v", i+1, n, err)
		}
	}
	return nil
}

func TestNATTypeAsymmetric(t *testing.T) {
	server := runNatTestServer(t, 12345, 12345)
	nt, err := TestNATType(server, 3*time.Second)
	if err != nil {
		t.Fatalf("TestNATType failed: %v", err)
	}
	if nt != pb.NatType_ASYMMETRIC {
		t.Fatalf("got %v, want ASYMMETRIC", nt)
	}
}

func TestNATTypeSymmetric(t *testing.T) {
	server := runNatTestServer(t, 12345, 23456)
	nt, err := TestNATType(server, 3*time.Second)
	if err != nil {
		t.Fatalf("TestNATType failed: %v", err)
	}
	if nt != pb.NatType_SYMMETRIC {
		t.Fatalf("got %v, want SYMMETRIC", nt)
	}
}

func TestNATTypeInvalidPortsClosesProbes(t *testing.T) {
	server := runNatTestServer(t, 12345, 0)
	nt, err := TestNATType(server, 3*time.Second)
	if err == nil || nt != pb.NatType_UNKNOWN_NAT {
		t.Fatalf("TestNATType = %v, %v; want UNKNOWN_NAT and an error", nt, err)
	}
}

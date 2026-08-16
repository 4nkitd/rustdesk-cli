package hbb

import (
	"fmt"
	"net"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "rdcli/internal/hbb/proto"
)

// runNatTestServer binds listeners on port and port-1. probe1 is the port the
// first probe's response reports, probe2 the second probe's response port.
func runNatTestServer(t *testing.T, port, probe1, probe2 int) string {
	t.Helper()
	ln1, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("listen %d: %v", port, err)
	}
	ln2, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port-1))
	if err != nil {
		t.Fatalf("listen %d: %v", port-1, err)
	}
	serve := func(ln net.Listener, port int) {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn, port int) {
				defer c.Close()
				req, err := readRendezvousMessage(c)
				if err != nil {
					return
				}
				if req.GetTestNatRequest() == nil {
					return
				}
				resp := &pb.RendezvousMessage{
					Union: &pb.RendezvousMessage_TestNatResponse{
						TestNatResponse: &pb.TestNatResponse{Port: int32(port)},
					},
				}
				data, _ := proto.Marshal(resp)
				_, _ = c.Write(Encode(data))
			}(conn, port)
		}
	}
	go serve(ln1, probe1)
	go serve(ln2, probe2)
	return fmt.Sprintf("127.0.0.1:%d", port)
}

func TestNATTypeAsymmetric(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	basePort := base.Addr().(*net.TCPAddr).Port
	base.Close()

	server := runNatTestServer(t, basePort, basePort, basePort)

	nt, err := TestNATType(server, 3*time.Second)
	if err != nil {
		t.Fatalf("TestNATType failed: %v", err)
	}
	if nt != pb.NatType_ASYMMETRIC {
		t.Fatalf("got %v, want ASYMMETRIC", nt)
	}
}

func TestNATTypeSymmetric(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	basePort := base.Addr().(*net.TCPAddr).Port
	base.Close()

	server := runNatTestServer(t, basePort, basePort, basePort+5000)

	nt, err := TestNATType(server, 3*time.Second)
	if err != nil {
		t.Fatalf("TestNATType failed: %v", err)
	}
	if nt != pb.NatType_SYMMETRIC {
		t.Fatalf("got %v, want SYMMETRIC", nt)
	}
}

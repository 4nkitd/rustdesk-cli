package hbb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func networkListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

func networkPunchPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln := networkListener(t)
	c, err := dialPunch(netip.AddrPort{}, netip.MustParseAddrPort(ln.Addr().String()), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	p, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return c, p
}

func TestNetworkPunchRefused(t *testing.T) {
	ln := networkListener(t)
	addr := netip.MustParseAddrPort(ln.Addr().String())
	ln.Close()
	c, err := dialPunch(netip.AddrPort{}, addr, time.Second)
	if c != nil {
		c.Close()
	}
	if err == nil {
		t.Fatal("refused connection reported success")
	}
}

func TestNetworkPunchEOF(t *testing.T) {
	c, p := networkPunchPair(t)
	p.Close()
	c.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if n, err := c.Read(b[:]); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("Read = %d, %v; want EOF", n, err)
	}
}

func TestNetworkPunchDeadlineIsolation(t *testing.T) {
	c, p := networkPunchPair(t)
	c.SetReadDeadline(time.Now().Add(-time.Second))
	var b [1]byte
	if _, err := c.Read(b[:]); err == nil {
		t.Fatal("expired read deadline did not fail")
	} else if e, ok := err.(net.Error); !ok || !e.Timeout() {
		t.Fatalf("expected timeout, got %v", err)
	}
	c.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := c.Write([]byte("x")); err != nil {
		t.Fatalf("read deadline affected write: %v", err)
	}
	p.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := io.ReadFull(p, b[:]); err != nil || b[0] != 'x' {
		t.Fatalf("peer read = %q, %v", b, err)
	}
	c.SetReadDeadline(time.Time{})
	c.SetWriteDeadline(time.Now().Add(-time.Second))
	if _, err := p.Write([]byte("y")); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := io.ReadFull(c, b[:]); err != nil || b[0] != 'y' {
		t.Fatalf("write deadline affected read: %q, %v", b, err)
	}
}

func TestNetworkPunchCloseInterruptsRead(t *testing.T) {
	c, _ := networkPunchPair(t)
	done := make(chan error, 1)
	go func() {
		var b [1]byte
		_, err := c.Read(b[:])
		done <- err
	}()
	c.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Read after Close = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not interrupt Read")
	}
}

func TestNetworkPunchReusesLocalPort(t *testing.T) {
	first, _ := networkPunchPair(t)
	local := first.LocalAddr().(*net.TCPAddr).AddrPort()
	ln := networkListener(t)
	c, err := dialPunch(local, netip.MustParseAddrPort(ln.Addr().String()), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	p, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if got := p.RemoteAddr().(*net.TCPAddr).AddrPort(); got != local {
		t.Fatalf("source = %v, want %v", got, local)
	}
}

func TestNetworkNATZeroTimeout(t *testing.T) {
	var first, second net.Listener
	for i := 0; i < 20; i++ {
		first = networkListener(t)
		port := first.Addr().(*net.TCPAddr).Port
		var err error
		second, err = net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port-1)))
		if err == nil {
			break
		}
		first.Close()
	}
	if second == nil {
		t.Fatal("could not reserve adjacent loopback ports")
	}
	t.Cleanup(func() { second.Close() })
	observed := make(chan string, 2)
	for _, ln := range []net.Listener{first, second} {
		go func(ln net.Listener) {
			c, err := ln.Accept()
			if err != nil {
				observed <- "accept failed"
				return
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(time.Second))
			if _, err := readRendezvousMessage(c); err != nil {
				observed <- "read failed"
				return
			}
			observed <- c.RemoteAddr().String()
			writeRendezvousMessage(c, &pb.RendezvousMessage{Union: &pb.RendezvousMessage_TestNatResponse{
				TestNatResponse: &pb.TestNatResponse{Port: 12345},
			}})
		}(ln)
	}
	nt, err := TestNATType(first.Addr().String(), 0)
	if err != nil || nt != pb.NatType_ASYMMETRIC {
		t.Fatalf("zero-timeout NAT probe = %v, %v", nt, err)
	}
	a, b := <-observed, <-observed
	if a != b || a == "read failed" || a == "accept failed" {
		t.Fatalf("probe source ports differ: %q, %q", a, b)
	}
}

type networkReadConn struct {
	net.Conn
	r        *bytes.Reader
	finalEOF bool
	chunk    int
}

func (c *networkReadConn) Read(p []byte) (int, error) {
	if c.chunk > 0 && len(p) > c.chunk {
		p = p[:c.chunk]
	}
	n, err := c.r.Read(p)
	if c.finalEOF && c.r.Len() == 0 {
		err = io.EOF
	}
	return n, err
}

func TestNetworkRendezvousExactFrames(t *testing.T) {
	for _, size := range []int{1, 100, 20000} {
		for _, chunk := range []int{0, 1} {
			t.Run(strconv.Itoa(size)+"/chunk="+strconv.Itoa(chunk), func(t *testing.T) {
				first := &pb.RendezvousMessage{Union: &pb.RendezvousMessage_KeyExchange{
					KeyExchange: &pb.KeyExchange{Keys: [][]byte{bytes.Repeat([]byte("a"), size)}},
				}}
				second := &pb.RendezvousMessage{Union: &pb.RendezvousMessage_RelayResponse{
					RelayResponse: &pb.RelayResponse{Uuid: "preserved"},
				}}
				a, _ := proto.Marshal(first)
				b, _ := proto.Marshal(second)
				c := &networkReadConn{r: bytes.NewReader(append(Encode(a), Encode(b)...)), finalEOF: true, chunk: chunk}
				for _, want := range []*pb.RendezvousMessage{first, second} {
					got, err := readRendezvousMessage(c)
					if err != nil || !proto.Equal(got, want) {
						t.Fatalf("frame mismatch: %v", err)
					}
				}
				if _, err := readRendezvousMessage(c); !errors.Is(err, io.EOF) {
					t.Fatalf("final read = %v, want EOF", err)
				}
			})
		}
	}
}

func TestNetworkRendezvousRejectsOversizedAndTruncated(t *testing.T) {
	var oversized [4]byte
	binary.LittleEndian.PutUint32(oversized[:], uint32(maxFramePayloadLength+1)<<2|3)
	for _, wire := range [][]byte{oversized[:], {1}, {8, 0}} {
		c := &networkReadConn{r: bytes.NewReader(wire)}
		if _, err := readRendezvousMessage(c); err == nil {
			t.Fatalf("accepted invalid frame %x", wire)
		}
	}
}

func TestNetworkTunnelFirstCompletion(t *testing.T) {
	for _, side := range []string{"local", "peer"} {
		t.Run(side, func(t *testing.T) {
			remote, peer := net.Pipe()
			local, app := net.Pipe()
			defer peer.Close()
			defer app.Close()
			sc := NewSecureConn(remote)
			sc.SetRaw()
			tunnel := &Tunnel{conn: &Conn{sc: sc}}
			done := make(chan error, 1)
			go func() { done <- tunnel.Pipe(local) }()
			remaining := app
			if side == "local" {
				app.Close()
				remaining = peer
			} else {
				peer.Close()
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("orderly close = %v", err)
				}
			case <-time.After(time.Second):
				local.Close()
				remote.Close()
				t.Fatal("Pipe did not stop both copies")
			}
			remaining.SetReadDeadline(time.Now().Add(time.Second))
			var b [1]byte
			if _, err := remaining.Read(b[:]); !errors.Is(err, io.EOF) {
				t.Fatalf("remaining stream not closed: %v", err)
			}
		})
	}
}

type networkFailWriter struct {
	net.Conn
	err error
}

func (c *networkFailWriter) Write(p []byte) (int, error) { return 0, c.err }

func TestNetworkTunnelWriteFailure(t *testing.T) {
	remote, peer := net.Pipe()
	local, app := net.Pipe()
	defer peer.Close()
	defer app.Close()
	sc := NewSecureConn(remote)
	sc.SetRaw()
	want := errors.New("write failed")
	tunnel := &Tunnel{conn: &Conn{sc: sc}}
	done := make(chan error, 1)
	go func() { done <- tunnel.Pipe(&networkFailWriter{Conn: local, err: want}) }()
	peer.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := peer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, want) {
			t.Fatalf("Pipe = %v, want write failure", err)
		}
	case <-time.After(time.Second):
		local.Close()
		remote.Close()
		t.Fatal("write failure left Pipe blocked")
	}
}

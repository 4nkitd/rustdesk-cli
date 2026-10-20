package hbb

import (
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestNATPunchDifferentAddressFamilies(t *testing.T) {
	for _, test := range []struct {
		name    string
		network string
		listen  string
		local   string
	}{
		{"IPv6 rendezvous to IPv4 peer", "tcp4", "127.0.0.1:0", "[::1]:12345"},
		{"IPv4 rendezvous to IPv6 peer", "tcp6", "[::1]:0", "127.0.0.1:12345"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ln, err := net.Listen(test.network, test.listen)
			if err != nil {
				if test.network == "tcp6" {
					t.Skipf("IPv6 loopback unavailable: %v", err)
				}
				t.Fatal(err)
			}
			defer ln.Close()
			conn, err := dialPunch(netip.MustParseAddrPort(test.local), netip.MustParseAddrPort(ln.Addr().String()), time.Second)
			if err != nil {
				t.Fatalf("cross-family direct attempt failed: %v", err)
			}
			defer conn.Close()
			peer, err := ln.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
		})
	}
}

package hbb

import (
	"net/netip"
	"testing"
)

func TestAddrMangle(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"192.168.16.32:21116", "192.168.16.32:21116"},
		{"[2001:db8::1]:8080", "[2001:db8::1]:8080"},
		{"[2001:db8:ff::1111]:80", "[2001:db8:ff::1111]:80"},
		{"[::ffff:127.0.0.1]:8080", "127.0.0.1:8080"},
		{"[::1]:8080", "[::1]:8080"},
	}

	for _, tt := range tests {
		inAddr := netip.MustParseAddrPort(tt.input)
		expectedAddr := netip.MustParseAddrPort(tt.expected)

		encoded := EncodeAddr(inAddr)
		decoded := DecodeAddr(encoded)

		if decoded != expectedAddr {
			t.Errorf("roundtrip for %s failed: got %s, want %s", tt.input, decoded, expectedAddr)
		}
	}
}

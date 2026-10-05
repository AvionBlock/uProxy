package core

import (
	"bytes"
	"encoding/hex"
	"net"
	"testing"
)

func TestEncodeProxyProtocolV2UDP4(t *testing.T) {
	header, err := EncodeProxyProtocolV2(0x12, net.ParseIP("192.0.2.10"), 19132, net.ParseIP("198.51.100.20"), 19133)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := hex.DecodeString("0d0a0d0a000d0a515549540a2112000cc000020ac63364144abc4abd")
	if !bytes.Equal(header, want) {
		t.Fatalf("UDP4 header = %x, want %x", header, want)
	}
}

func TestEncodeProxyProtocolV2UDP6(t *testing.T) {
	header, err := EncodeProxyProtocolV2(0x22, net.ParseIP("2001:db8::1"), 1234, net.ParseIP("2001:db8::2"), 19132)
	if err != nil {
		t.Fatal(err)
	}
	if len(header) != 52 || header[13] != 0x22 || header[14] != 0 || header[15] != 36 {
		t.Fatalf("invalid UDP6 header: %x", header)
	}
	if !bytes.Equal(header[16:32], net.ParseIP("2001:db8::1").To16()) ||
		!bytes.Equal(header[32:48], net.ParseIP("2001:db8::2").To16()) {
		t.Fatalf("invalid UDP6 addresses: %x", header)
	}
}

func TestEncodeProxyProtocolV2RejectsInvalidAddressesAndPorts(t *testing.T) {
	tests := []struct {
		name  string
		proto byte
		src   net.IP
		dst   net.IP
		port  int
	}{
		{"mixed family", 0x12, net.ParseIP("127.0.0.1"), net.ParseIP("::1"), 1234},
		{"missing address", 0x11, nil, net.ParseIP("127.0.0.1"), 1234},
		{"invalid protocol", 0x42, net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"), 1234},
		{"negative port", 0x12, net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"), -1},
		{"oversized port", 0x12, net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"), 65536},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := EncodeProxyProtocolV2(tt.proto, tt.src, tt.port, tt.dst, 19132); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

package core

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
)

func TestEncodeProxyProtocolV2IPv4(t *testing.T) {
	h := EncodeProxyProtocolV2(TransportUDP, net.ParseIP("203.0.113.7"), 40123, net.ParseIP("10.0.0.1"), 19132)
	if len(h) != ProxyProtocolV2LenIPv4 {
		t.Fatalf("len = %d, want %d", len(h), ProxyProtocolV2LenIPv4)
	}
	if !bytes.Equal(h[:12], ProxyProtocolV2Signature) {
		t.Fatal("bad signature")
	}
	if h[12] != 0x21 || h[13] != 0x12 || binary.BigEndian.Uint16(h[14:16]) != 12 {
		t.Fatalf("bad header bytes %x", h[12:16])
	}
	d, ok := DecodeProxyProtocolV2(append(h, 0xAA))
	if !ok || d.Len != ProxyProtocolV2LenIPv4 || d.Transport != TransportUDP {
		t.Fatalf("decode failed: %+v %v", d, ok)
	}
	if !d.SrcIP.Equal(net.ParseIP("203.0.113.7")) || d.SrcPort != 40123 || !d.DstIP.Equal(net.ParseIP("10.0.0.1")) || d.DstPort != 19132 {
		t.Fatalf("decoded %+v", d)
	}
}

func TestEncodeProxyProtocolV2IPv6AndMixed(t *testing.T) {
	h := EncodeProxyProtocolV2(TransportTCP, net.ParseIP("2001:db8::1"), 1234, net.ParseIP("192.0.2.1"), 443)
	if len(h) != ProxyProtocolV2LenIPv6 || h[13] != 0x21 {
		t.Fatalf("len=%d fam/proto=%x", len(h), h[13])
	}
	d, ok := DecodeProxyProtocolV2(h)
	if !ok || !d.SrcIP.Equal(net.ParseIP("2001:db8::1")) || !d.DstIP.Equal(net.ParseIP("192.0.2.1")) || d.DstPort != 443 {
		t.Fatalf("decoded %+v ok=%v", d, ok)
	}

	// Unspecified destination takes the family of the source.
	h = EncodeProxyProtocolV2(TransportUDP, net.ParseIP("198.51.100.9"), 5, net.IPv6unspecified, 7)
	if len(h) != ProxyProtocolV2LenIPv4 {
		t.Fatalf("unspecified dst should stay IPv4, got len %d", len(h))
	}
	h = EncodeProxyProtocolV2(TransportUDP, net.ParseIP("198.51.100.9"), 5, nil, 7)
	if len(h) != ProxyProtocolV2LenIPv4 {
		t.Fatalf("nil dst should stay IPv4, got len %d", len(h))
	}
}

func rakPing() []byte {
	b := []byte{RakNetIDUnconnectedPing}
	b = append(b, make([]byte, 8)...)
	b = append(b, RakNetMagic...)
	b = append(b, make([]byte, 8)...)
	return b
}

func stunBindingRequest() []byte {
	b := []byte{0x00, 0x01, 0x00, 0x00, 0x21, 0x12, 0xa4, 0x42}
	b = append(b, make([]byte, 12)...) // transaction id
	return b
}

func TestClassifyDatagram(t *testing.T) {
	cases := []struct {
		name string
		b    []byte
		want DatagramKind
	}{
		{"empty", nil, KindUnknown},
		{"raknet ping", rakPing(), KindRakNet},
		{"raknet ping open", append([]byte{RakNetIDUnconnectedPingOpenConnections}, rakPing()[1:]...), KindRakNet},
		{"raknet ocr1", append(append([]byte{RakNetIDOpenConnectionRequest1}, RakNetMagic...), 11, 0, 0), KindRakNet},
		{"raknet ocr2", append(append([]byte{RakNetIDOpenConnectionRequest2}, RakNetMagic...), make([]byte, 20)...), KindRakNet},
		{"raknet datagram", []byte{0x84, 0x00, 0x00, 0x00, 0x60}, KindRakNet},
		{"raknet ack", []byte{0xc0, 0x00, 0x01, 0x01, 0x00, 0x00, 0x00}, KindRakNet},
		{"raknet nak", []byte{0xa0, 0x00, 0x01, 0x01, 0x00, 0x00, 0x00}, KindRakNet},
		{"stun request", stunBindingRequest(), KindNetherNet},
		{"stun success response (0x01 like raknet ping)", append([]byte{0x01, 0x01, 0x00, 0x00, 0x21, 0x12, 0xa4, 0x42}, make([]byte, 12)...), KindNetherNet},
		{"dtls 1.2 handshake", append([]byte{22, 0xfe, 0xfd}, make([]byte, 20)...), KindNetherNet},
		{"dtls 1.0 app data", append([]byte{23, 0xfe, 0xff}, make([]byte, 20)...), KindNetherNet},
		{"0x01 without magic", []byte{0x01, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25}, KindUnknown},
		{"garbage", []byte{0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42}, KindUnknown},
	}
	for _, c := range cases {
		if got := ClassifyDatagram(c.b); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func pong(motd string) []byte {
	b := []byte{RakNetIDUnconnectedPong}
	b = append(b, make([]byte, 16)...) // time + guid
	b = append(b, RakNetMagic...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(motd)))
	b = append(b, motd...)
	return b
}

func TestRewriteUnconnectedPongPorts(t *testing.T) {
	in := pong("MCPE;Geyser;819;1.21.0;1;20;123456789;Bedrock level;Survival;1;19133;19133;")
	out := RewriteUnconnectedPongPorts(in, 19132)
	if !IsRakNetUnconnectedPong(out) {
		t.Fatal("result is not a pong")
	}
	n := int(binary.BigEndian.Uint16(out[33:35]))
	got := string(out[35 : 35+n])
	want := "MCPE;Geyser;819;1.21.0;1;20;123456789;Bedrock level;Survival;1;19132;19132;"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if len(out) != 35+n {
		t.Fatalf("trailing bytes: len %d", len(out))
	}

	// Already right: unchanged slice.
	same := RewriteUnconnectedPongPorts(out, 19132)
	if !bytes.Equal(same, out) {
		t.Fatal("rewrite should be idempotent")
	}
	// Too few fields: untouched.
	short := pong("MCPE;motd;819;1.21.0;1;20")
	if !bytes.Equal(RewriteUnconnectedPongPorts(short, 1), short) {
		t.Fatal("short MOTD must not be rewritten")
	}
	// Not a pong: untouched.
	if !bytes.Equal(RewriteUnconnectedPongPorts(rakPing(), 1), rakPing()) {
		t.Fatal("ping must not be rewritten")
	}
}

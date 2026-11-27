package core

import (
	"encoding/binary"
	"net"
)

// Transport selects the transport-protocol nibble of a PROXY protocol v2 header.
type Transport byte

const (
	// TransportTCP marks the forwarded connection as TCP (0x11 / 0x21).
	TransportTCP Transport = 0x1
	// TransportUDP marks the forwarded datagram as UDP (0x12 / 0x22).
	TransportUDP Transport = 0x2
)

// ProxyProtocolV2Signature is the 12-byte magic every binary PROXY header starts with.
var ProxyProtocolV2Signature = []byte{
	0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A,
}

const (
	// ProxyProtocolV2LenIPv4 is the size of a v2 header carrying IPv4 addresses.
	ProxyProtocolV2LenIPv4 = 16 + 12
	// ProxyProtocolV2LenIPv6 is the size of a v2 header carrying IPv6 addresses.
	ProxyProtocolV2LenIPv6 = 16 + 36
)

// EncodeProxyProtocolV2 builds a PROXY protocol v2 "PROXY" command header.
//
// The address family is chosen from the source address: an IPv4 client yields an
// AF_INET header, anything else AF_INET6. When the families of source and
// destination differ, the IPv4 side is written as an IPv4-mapped IPv6 address,
// which Java (and therefore Geyser / CloudburstMC Network) turns back into an
// Inet4Address. A nil or unspecified destination is written as the unspecified
// address of the chosen family.
func EncodeProxyProtocolV2(t Transport, srcIP net.IP, srcPort int, dstIP net.IP, dstPort int) []byte {
	src4 := srcIP.To4()
	if dstIP == nil || dstIP.IsUnspecified() {
		if src4 != nil {
			dstIP = net.IPv4zero
		} else {
			dstIP = net.IPv6unspecified
		}
	}
	dst4 := dstIP.To4()

	if src4 != nil && dst4 != nil {
		buf := make([]byte, 0, ProxyProtocolV2LenIPv4)
		buf = append(buf, ProxyProtocolV2Signature...)
		buf = append(buf, 0x21, 0x10|byte(t), 0x00, 12)
		buf = append(buf, src4...)
		buf = append(buf, dst4...)
		buf = binary.BigEndian.AppendUint16(buf, uint16(srcPort))
		buf = binary.BigEndian.AppendUint16(buf, uint16(dstPort))
		return buf
	}

	src16 := srcIP.To16()
	dst16 := dstIP.To16()
	if src16 == nil {
		src16 = net.IPv6unspecified
	}
	if dst16 == nil {
		dst16 = net.IPv6unspecified
	}
	buf := make([]byte, 0, ProxyProtocolV2LenIPv6)
	buf = append(buf, ProxyProtocolV2Signature...)
	buf = append(buf, 0x21, 0x20|byte(t), 0x00, 36)
	buf = append(buf, src16...)
	buf = append(buf, dst16...)
	buf = binary.BigEndian.AppendUint16(buf, uint16(srcPort))
	buf = binary.BigEndian.AppendUint16(buf, uint16(dstPort))
	return buf
}

// ProxyProtocolV2Header holds the fields of a decoded PROXY v2 header.
type ProxyProtocolV2Header struct {
	Transport Transport
	SrcIP     net.IP
	SrcPort   int
	DstIP     net.IP
	DstPort   int
	// Len is the total header length, i.e. the offset of the payload.
	Len int
}

// DecodeProxyProtocolV2 parses a PROXY v2 "PROXY" header at the start of b.
// It returns false when b does not start with a v2 header or the header is
// truncated. Only the IPv4 and IPv6 families are decoded. It is used by tests
// and by anyone who wants to inspect what uProxy emits; the proxies themselves
// never need to decode headers.
func DecodeProxyProtocolV2(b []byte) (ProxyProtocolV2Header, bool) {
	var h ProxyProtocolV2Header
	if len(b) < 16 {
		return h, false
	}
	for i, c := range ProxyProtocolV2Signature {
		if b[i] != c {
			return h, false
		}
	}
	if b[12]&0xF0 != 0x20 || b[12]&0x0F != 0x01 {
		return h, false
	}
	family := b[13] & 0xF0
	h.Transport = Transport(b[13] & 0x0F)
	addrLen := int(binary.BigEndian.Uint16(b[14:16]))
	h.Len = 16 + addrLen
	if len(b) < h.Len {
		return h, false
	}
	switch family {
	case 0x10:
		if addrLen < 12 {
			return h, false
		}
		h.SrcIP = net.IP(append([]byte(nil), b[16:20]...))
		h.DstIP = net.IP(append([]byte(nil), b[20:24]...))
		h.SrcPort = int(binary.BigEndian.Uint16(b[24:26]))
		h.DstPort = int(binary.BigEndian.Uint16(b[26:28]))
	case 0x20:
		if addrLen < 36 {
			return h, false
		}
		h.SrcIP = net.IP(append([]byte(nil), b[16:32]...))
		h.DstIP = net.IP(append([]byte(nil), b[32:48]...))
		h.SrcPort = int(binary.BigEndian.Uint16(b[48:50]))
		h.DstPort = int(binary.BigEndian.Uint16(b[50:52]))
	default:
		return h, false
	}
	return h, true
}

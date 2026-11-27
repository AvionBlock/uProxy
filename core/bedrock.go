package core

import (
	"bytes"
	"encoding/binary"
	"strconv"
	"strings"
)

// DatagramKind tells which Bedrock transport a UDP datagram belongs to.
type DatagramKind int

const (
	// KindUnknown is returned for datagrams that match neither transport.
	KindUnknown DatagramKind = iota
	// KindRakNet covers RakNet offline packets and connected datagrams (classic Bedrock transport).
	KindRakNet
	// KindNetherNet covers the WebRTC media path used by NetherNet: STUN (ICE) and DTLS (SCTP inside).
	KindNetherNet
)

func (k DatagramKind) String() string {
	switch k {
	case KindRakNet:
		return "raknet"
	case KindNetherNet:
		return "nethernet"
	default:
		return "unknown"
	}
}

// RakNetMagic is the 16-byte "offline message" magic shared by all RakNet unconnected packets.
var RakNetMagic = []byte{
	0x00, 0xff, 0xff, 0x00, 0xfe, 0xfe, 0xfe, 0xfe,
	0xfd, 0xfd, 0xfd, 0xfd, 0x12, 0x34, 0x56, 0x78,
}

// RakNet packet identifiers that matter to the proxy.
const (
	RakNetIDUnconnectedPing                = 0x01
	RakNetIDUnconnectedPingOpenConnections = 0x02
	RakNetIDOpenConnectionRequest1         = 0x05
	RakNetIDOpenConnectionRequest2         = 0x07
	RakNetIDUnconnectedPong                = 0x1c
)

// stunMagicCookie is the fixed cookie at offset 4 of every STUN message (RFC 5389).
var stunMagicCookie = []byte{0x21, 0x12, 0xa4, 0x42}

// ClassifyDatagram inspects the first bytes of a client-to-server datagram and
// decides which transport it belongs to.
//
// NetherNet rides on WebRTC, so the client-to-server traffic on the media port is
// STUN (ICE connectivity checks, first byte 0..3 plus the magic cookie) and DTLS
// (first byte 20..63 with a DTLS record version). RakNet traffic is either an
// offline packet carrying the RakNet magic or a connected datagram with the high
// bit set (0x80..0x8f data, 0xa0 NAK, 0xc0 ACK). The demultiplexing rules follow
// RFC 7983; the two families never overlap once the magic values are checked.
func ClassifyDatagram(b []byte) DatagramKind {
	if len(b) == 0 {
		return KindUnknown
	}
	first := b[0]
	switch {
	case first < 4:
		if len(b) >= 20 && bytes.Equal(b[4:8], stunMagicCookie) {
			return KindNetherNet
		}
		// Unconnected ping: id, int64 time, magic, int64 client guid.
		if (first == RakNetIDUnconnectedPing || first == RakNetIDUnconnectedPingOpenConnections) &&
			len(b) >= 25 && bytes.Equal(b[9:25], RakNetMagic) {
			return KindRakNet
		}
		return KindUnknown
	case first == RakNetIDOpenConnectionRequest1 || first == RakNetIDOpenConnectionRequest2:
		if len(b) >= 17 && bytes.Equal(b[1:17], RakNetMagic) {
			return KindRakNet
		}
		return KindUnknown
	case first >= 20 && first <= 63:
		// DTLS record: content type, version (0xfeff = DTLS 1.0, 0xfefd = DTLS 1.2), epoch, seq, length.
		if len(b) >= 13 && b[1] == 0xfe && (b[2] == 0xfd || b[2] == 0xff) {
			return KindNetherNet
		}
		return KindUnknown
	case first&0x80 != 0:
		return KindRakNet
	}
	return KindUnknown
}

// IsRakNetUnconnectedPong reports whether b is a RakNet 0x1c pong with a valid magic.
func IsRakNetUnconnectedPong(b []byte) bool {
	return len(b) >= 35 && b[0] == RakNetIDUnconnectedPong && bytes.Equal(b[17:33], RakNetMagic)
}

// RewriteUnconnectedPongPorts rewrites the IPv4 and IPv6 port fields of a
// RakNet unconnected pong (0x1c) so that the server list shows the proxy's
// public port instead of the backend's. The layout is: id (1), ping time (8),
// server GUID (8), magic (16), uint16 length, then the MOTD string
// "MCPE;motd;protocol;version;players;max;guid;submotd;gametype;nintendo;ipv4Port;ipv6Port;".
// The datagram is returned unchanged when it is not a pong, when the MOTD has
// fewer than 12 fields, or when the port fields are not numeric.
func RewriteUnconnectedPongPorts(b []byte, port int) []byte {
	if !IsRakNetUnconnectedPong(b) {
		return b
	}
	n := int(binary.BigEndian.Uint16(b[33:35]))
	if 35+n > len(b) {
		return b
	}
	motd := string(b[35 : 35+n])
	parts := strings.Split(motd, ";")
	if len(parts) < 12 {
		return b
	}
	if _, err := strconv.Atoi(parts[10]); err != nil {
		return b
	}
	if _, err := strconv.Atoi(parts[11]); err != nil {
		return b
	}
	p := strconv.Itoa(port)
	if parts[10] == p && parts[11] == p {
		return b
	}
	parts[10] = p
	parts[11] = p
	out := strings.Join(parts, ";")
	if len(out) > 0xFFFF {
		return b
	}

	res := make([]byte, 0, 35+len(out)+len(b)-(35+n))
	res = append(res, b[:33]...)
	res = binary.BigEndian.AppendUint16(res, uint16(len(out)))
	res = append(res, out...)
	res = append(res, b[35+n:]...)
	return res
}

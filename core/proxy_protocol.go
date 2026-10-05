package core

import (
	"encoding/binary"
	"fmt"
	"net"
)

var proxyV2Signature = []byte{0x0d, 0x0a, 0x0d, 0x0a, 0x00, 0x0d, 0x0a, 0x51, 0x55, 0x49, 0x54, 0x0a}

// EncodeProxyProtocolV2 builds a PROXY v2 header. proto is TCP4 (0x11), UDP4
// (0x12), TCP6 (0x21), or UDP6 (0x22).
func EncodeProxyProtocolV2(proto byte, clientIP net.IP, clientPort int, serverIP net.IP, serverPort int) ([]byte, error) {
	if clientPort < 0 || clientPort > 65535 || serverPort < 0 || serverPort > 65535 {
		return nil, fmt.Errorf("PROXY protocol port out of range")
	}

	var source, destination net.IP
	switch proto {
	case 0x11, 0x12:
		source, destination = clientIP.To4(), serverIP.To4()
	case 0x21, 0x22:
		if clientIP.To4() == nil && serverIP.To4() == nil {
			source, destination = clientIP.To16(), serverIP.To16()
		}
	default:
		return nil, fmt.Errorf("unsupported PROXY protocol family/transport 0x%02x", proto)
	}
	if source == nil || destination == nil {
		return nil, fmt.Errorf("client and backend IP families do not match PROXY protocol 0x%02x", proto)
	}

	length := len(source)*2 + 4
	header := make([]byte, 16+length)
	copy(header, proxyV2Signature)
	header[12] = 0x21 // version 2, PROXY command
	header[13] = proto
	binary.BigEndian.PutUint16(header[14:16], uint16(length))
	copy(header[16:], source)
	copy(header[16+len(source):], destination)
	binary.BigEndian.PutUint16(header[16+len(source)*2:], uint16(clientPort))
	binary.BigEndian.PutUint16(header[18+len(source)*2:], uint16(serverPort))
	return header, nil
}

package core

import (
	"bytes"
	"encoding/binary"
	"net"
)

func EncodeProxyProtocolV2(proto byte, clientIP net.IP, clientPort int, serverIP net.IP, serverPort int) []byte {
	sig := []byte{
		0x0D, 0x0A, 0x0D, 0x0A,
		0x00, 0x0D, 0x0A, 0x51,
		0x55, 0x49, 0x54, 0x0A,
		0x21,       // version and command: PROXY V2
		proto,      // protocol, e.g. 0x11 = TCP/IPv4, 0x12 = UDP/IPv4
		0x00, 0x0C, // address length
	}

	buf := bytes.NewBuffer(sig)
	buf.Write(clientIP.To4())
	buf.Write(serverIP.To4())

	binary.Write(buf, binary.BigEndian, uint16(clientPort))
	binary.Write(buf, binary.BigEndian, uint16(serverPort))

	return buf.Bytes()
}

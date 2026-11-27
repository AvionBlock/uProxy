package proxies

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"uproxy/config"
	"uproxy/core"
)

func init() {
	logger.SetOutput(io.Discard)
}

// fakeUDPServer records what it receives and answers with reply(payload) when non-nil.
type fakeUDPServer struct {
	conn *net.UDPConn
	recv chan udpRecord
}

type udpRecord struct {
	from *net.UDPAddr
	data []byte
}

func newFakeUDPServer(t *testing.T, reply func(data []byte) []byte) *fakeUDPServer {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeUDPServer{conn: conn, recv: make(chan udpRecord, 64)}
	go func() {
		buf := make([]byte, 65535)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			data := append([]byte(nil), buf[:n]...)
			s.recv <- udpRecord{from: from, data: data}
			if reply != nil {
				if r := reply(data); r != nil {
					_, _ = conn.WriteToUDP(r, from)
				}
			}
		}
	}()
	t.Cleanup(func() { conn.Close() })
	return s
}

func (s *fakeUDPServer) port() int { return s.conn.LocalAddr().(*net.UDPAddr).Port }

func (s *fakeUDPServer) next(t *testing.T) udpRecord {
	t.Helper()
	select {
	case r := <-s.recv:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for datagram at fake server")
		return udpRecord{}
	}
}

func startUDP(t *testing.T, cfg config.ProxyConfig) *UDPProxy {
	t.Helper()
	cfg.ListenIP = "127.0.0.1"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	p := NewUDPProxy(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = p.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for p.LocalAddr() == nil {
		if time.Now().After(deadline) {
			t.Fatal("proxy did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return p
}

func udpClient(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func rakPing() []byte {
	b := []byte{core.RakNetIDUnconnectedPing}
	b = append(b, make([]byte, 8)...)
	b = append(b, core.RakNetMagic...)
	b = append(b, 1, 2, 3, 4, 5, 6, 7, 8)
	return b
}

func rakPong(motd string) []byte {
	b := []byte{core.RakNetIDUnconnectedPong}
	b = append(b, make([]byte, 16)...)
	b = append(b, core.RakNetMagic...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(motd)))
	b = append(b, motd...)
	return b
}

func stun() []byte {
	b := []byte{0x00, 0x01, 0x00, 0x00, 0x21, 0x12, 0xa4, 0x42}
	return append(b, make([]byte, 12)...)
}

func expectHeader(t *testing.T, r udpRecord, client *net.UDPAddr, payload []byte) {
	t.Helper()
	h, ok := core.DecodeProxyProtocolV2(r.data)
	if !ok {
		t.Fatalf("expected PROXY header, got %x", r.data[:min(len(r.data), 16)])
	}
	if h.Transport != core.TransportUDP {
		t.Fatalf("transport %x, want UDP", h.Transport)
	}
	if !h.SrcIP.Equal(client.IP) || h.SrcPort != client.Port {
		t.Fatalf("header src %s:%d, want %s", h.SrcIP, h.SrcPort, client)
	}
	if !bytes.Equal(r.data[h.Len:], payload) {
		t.Fatalf("payload mismatch after header")
	}
}

func expectNoHeader(t *testing.T, r udpRecord, payload []byte) {
	t.Helper()
	if _, ok := core.DecodeProxyProtocolV2(r.data); ok {
		t.Fatal("unexpected PROXY header")
	}
	if !bytes.Equal(r.data, payload) {
		t.Fatalf("payload mismatch: %x", r.data)
	}
}

func TestUDPRakNetHeaderOnFirstDatagramAndResendAfterIdle(t *testing.T) {
	srv := newFakeUDPServer(t, func(d []byte) []byte {
		if h, ok := core.DecodeProxyProtocolV2(d); ok {
			d = d[h.Len:]
		}
		if len(d) > 0 && d[0] == core.RakNetIDUnconnectedPing {
			port := int(srvPortHolder.Load())
			return rakPong("MCPE;motd;819;1.21.0;0;10;1;sub;Survival;1;" + itoa(port) + ";" + itoa(port) + ";")
		}
		return nil
	})
	srvPortHolder.Store(int32(srv.port()))

	p := startUDP(t, config.ProxyConfig{
		ListenPort:             freePort(t),
		ServerHost:             "127.0.0.1",
		ServerPort:             srv.port(),
		Protocol:               config.ProtocolUDP,
		RakNetHeaderResendIdle: config.Duration(150 * time.Millisecond),
	})

	c := udpClient(t)
	clientAddr := c.LocalAddr().(*net.UDPAddr)
	target := p.LocalAddr()

	ping := rakPing()
	if _, err := c.WriteToUDP(ping, target); err != nil {
		t.Fatal(err)
	}
	first := srv.next(t)
	expectHeader(t, first, clientAddr, ping)

	// The pong must come back through the proxy with the port fields rewritten to the listen port.
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 2048)
	n, from, err := c.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	if from.Port != target.Port {
		t.Fatalf("pong came from %s, want proxy %s", from, target)
	}
	if !core.IsRakNetUnconnectedPong(buf[:n]) {
		t.Fatal("not a pong")
	}
	motd := string(buf[35:n])
	want := "MCPE;motd;819;1.21.0;0;10;1;sub;Survival;1;" + itoa(target.Port) + ";" + itoa(target.Port) + ";"
	if motd != want {
		t.Fatalf("motd %q want %q", motd, want)
	}

	// Second datagram right away: no header, same relay source port.
	if _, err := c.WriteToUDP(ping, target); err != nil {
		t.Fatal(err)
	}
	second := srv.next(t)
	expectNoHeader(t, second, ping)
	if second.from.Port != first.from.Port {
		t.Fatalf("relay port changed within a flow: %d -> %d", first.from.Port, second.from.Port)
	}
	// Drain the pong reply.
	_, _, _ = c.ReadFromUDP(buf)

	// After the upstream direction was idle for longer than the resend threshold the header comes back.
	time.Sleep(250 * time.Millisecond)
	if _, err := c.WriteToUDP(ping, target); err != nil {
		t.Fatal(err)
	}
	third := srv.next(t)
	expectHeader(t, third, clientAddr, ping)
	if third.from.Port != first.from.Port {
		t.Fatal("relay port must stay stable across a header re-send")
	}
}

var srvPortHolder atomic.Int32

func TestUDPNetherNetHeaderOnEveryDatagram(t *testing.T) {
	srv := newFakeUDPServer(t, func(d []byte) []byte { return []byte{0x01, 0x01, 0x00, 0x00, 0x21, 0x12, 0xa4, 0x42} })
	p := startUDP(t, config.ProxyConfig{
		ListenPort: freePort(t),
		ServerHost: "127.0.0.1",
		ServerPort: srv.port(),
		Protocol:   config.ProtocolUDP,
	})

	c := udpClient(t)
	clientAddr := c.LocalAddr().(*net.UDPAddr)
	for i := 0; i < 3; i++ {
		if _, err := c.WriteToUDP(stun(), p.LocalAddr()); err != nil {
			t.Fatal(err)
		}
		expectHeader(t, srv.next(t), clientAddr, stun())
	}
	// Replies reach the client unchanged.
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 64)
	n, _, err := c.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[:n], []byte{0x01, 0x01, 0x00, 0x00, 0x21, 0x12, 0xa4, 0x42}) {
		t.Fatalf("reply altered: %x", buf[:n])
	}

	// A DTLS record on the same flow keeps the header as well.
	dtls := append([]byte{22, 0xfe, 0xfd}, make([]byte, 20)...)
	if _, err := c.WriteToUDP(dtls, p.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	expectHeader(t, srv.next(t), clientAddr, dtls)
}

func TestUDPPlainModeAndDistinctRelayPorts(t *testing.T) {
	srv := newFakeUDPServer(t, nil)
	off := false
	p := startUDP(t, config.ProxyConfig{
		ListenPort:    freePort(t),
		ServerHost:    "127.0.0.1",
		ServerPort:    srv.port(),
		Protocol:      config.ProtocolUDP,
		ProxyProtocol: &off,
	})

	c1, c2 := udpClient(t), udpClient(t)
	if _, err := c1.WriteToUDP(rakPing(), p.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	r1 := srv.next(t)
	expectNoHeader(t, r1, rakPing())
	if _, err := c2.WriteToUDP(stun(), p.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	r2 := srv.next(t)
	expectNoHeader(t, r2, stun())
	if r1.from.Port == r2.from.Port {
		t.Fatal("two clients must be forwarded from two relay ports")
	}
	if p.FlowCount() != 2 {
		t.Fatalf("flows = %d, want 2", p.FlowCount())
	}
}

func TestUDPIdleTimeoutClosesFlow(t *testing.T) {
	srv := newFakeUDPServer(t, nil)
	p := startUDP(t, config.ProxyConfig{
		ListenPort:  freePort(t),
		ServerHost:  "127.0.0.1",
		ServerPort:  srv.port(),
		Protocol:    config.ProtocolUDP,
		IdleTimeout: config.Duration(200 * time.Millisecond),
	})
	c := udpClient(t)
	if _, err := c.WriteToUDP(rakPing(), p.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	srv.next(t)
	deadline := time.Now().Add(3 * time.Second)
	for p.FlowCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("flow was not reaped")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// A new datagram opens a new flow with a fresh header.
	if _, err := c.WriteToUDP(rakPing(), p.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	expectHeader(t, srv.next(t), c.LocalAddr().(*net.UDPAddr), rakPing())
}

func TestTCPProxyHeaderAndEcho(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	gotHeader := make(chan core.ProxyProtocolV2Header, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		hdr := make([]byte, core.ProxyProtocolV2LenIPv4)
		if _, err := io.ReadFull(conn, hdr); err != nil {
			return
		}
		h, ok := core.DecodeProxyProtocolV2(hdr)
		if !ok {
			close(gotHeader)
			return
		}
		gotHeader <- h
		_, _ = io.Copy(conn, conn)
	}()

	cfg := config.ProxyConfig{
		ListenIP:   "127.0.0.1",
		ListenPort: freePort(t),
		ServerHost: "127.0.0.1",
		ServerPort: ln.Addr().(*net.TCPAddr).Port,
		Protocol:   config.ProtocolTCP,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	p := NewTCPProxy(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = p.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for p.LocalAddr() == nil {
		if time.Now().After(deadline) {
			t.Fatal("tcp proxy did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}

	c, err := net.Dial("tcp", p.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	msg := []byte("GET /v1/join HTTP/1.1\r\nHost: x\r\n\r\n")
	if _, err := c.Write(msg); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, len(msg))
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(c, echo); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(echo, msg) {
		t.Fatalf("echo mismatch: %q", echo)
	}
	select {
	case h, ok := <-gotHeader:
		if !ok {
			t.Fatal("backend did not get a valid PROXY header")
		}
		ca := c.LocalAddr().(*net.TCPAddr)
		if h.Transport != core.TransportTCP || !h.SrcIP.Equal(ca.IP) || h.SrcPort != ca.Port {
			t.Fatalf("header %+v, client %s", h, ca)
		}
		if h.DstPort != cfg.ListenPort {
			t.Fatalf("dst port %d, want listen port %d", h.DstPort, cfg.ListenPort)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no header")
	}
}

func TestLegacyConfigCompatibility(t *testing.T) {
	tr := true
	legacy := config.ProxyConfig{ListenIP: "0.0.0.0", ListenPort: 25565, ServerHost: "1.2.3.4", ServerPort: 25565, ProtoTCP: &tr}
	if legacy.EffectiveProtocol() != config.ProtocolTCP {
		t.Fatal("protoTCP=true must mean tcp")
	}
	legacy.ProtoTCP = nil
	if legacy.EffectiveProtocol() != config.ProtocolUDP {
		t.Fatal("missing protocol must mean udp")
	}
	if !legacy.UseProxyProtocol() || legacy.EffectiveUDPMode() != config.UDPModeAuto || !legacy.PongRewriteEnabled() {
		t.Fatal("defaults changed")
	}
	if legacy.IdleTimeoutOrDefault() != config.DefaultIdleTimeout || legacy.HeaderResendIdleOrDefault() != config.DefaultRakNetHeaderResendIdle {
		t.Fatal("duration defaults changed")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	port := l.LocalAddr().(*net.UDPAddr).Port
	l.Close()
	return port
}

func itoa(i int) string { return strconv.Itoa(i) }

package proxies

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"uproxy/config"
)

func startUDPTestProxy(t *testing.T, idle time.Duration) (*udpProxy, *net.UDPConn) {
	t.Helper()
	backend, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	p, err := newUDPProxy(config.ProxyConfig{
		ListenIP: "127.0.0.1", ListenPort: 0,
		ServerHost: "127.0.0.1", ServerPort: backend.LocalAddr().(*net.UDPAddr).Port,
	}, false, idle)
	if err != nil {
		backend.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- p.serve() }()
	t.Cleanup(func() {
		_ = p.Close()
		_ = backend.Close()
		if err := <-done; err != nil {
			t.Errorf("UDP proxy stopped: %v", err)
		}
	})
	return p, backend
}

func readBackend(t *testing.T, backend *net.UDPConn) ([]byte, *net.UDPAddr) {
	t.Helper()
	buf := make([]byte, 65535)
	_ = backend.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, from, err := backend.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	return buf[:n], from
}

func TestUDPProxyGeyserHandshakeAndReplies(t *testing.T) {
	p, backend := startUDPTestProxy(t, time.Second)
	client, err := net.DialUDP("udp4", nil, p.listener.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	clientAddr := client.LocalAddr().(*net.UDPAddr)

	// RakNet's first datagram may be a ping (0x01) or something else. The
	// PROXY header must be present on whichever datagram arrives first.
	first := []byte{0x80, 0x01, 0x05}
	if _, err := client.Write(first); err != nil {
		t.Fatal(err)
	}
	got, backendClient := readBackend(t, backend)
	if len(got) != 28+len(first) || !bytes.Equal(got[:12], []byte{13, 10, 13, 10, 0, 13, 10, 81, 85, 73, 84, 10}) ||
		got[12] != 0x21 || got[13] != 0x12 || binary.BigEndian.Uint16(got[14:16]) != 12 ||
		!bytes.Equal(got[16:20], clientAddr.IP.To4()) ||
		!bytes.Equal(got[20:24], backend.LocalAddr().(*net.UDPAddr).IP.To4()) ||
		int(binary.BigEndian.Uint16(got[24:26])) != clientAddr.Port ||
		int(binary.BigEndian.Uint16(got[26:28])) != backend.LocalAddr().(*net.UDPAddr).Port ||
		!bytes.Equal(got[28:], first) {
		t.Fatalf("invalid first datagram: %x", got)
	}

	second := []byte{0x05, 0x99, 0x00}
	if _, err := client.Write(second); err != nil {
		t.Fatal(err)
	}
	got, from := readBackend(t, backend)
	if !bytes.Equal(got, second) || !from.IP.Equal(backendClient.IP) || from.Port != backendClient.Port {
		t.Fatalf("subsequent datagram changed socket or contents: %x from %s", got, from)
	}

	reply := []byte("19132 binary response 19132")
	if _, err := backend.WriteToUDP(reply, backendClient); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 100)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := client.Read(buf)
	if err != nil || !bytes.Equal(buf[:n], reply) {
		t.Fatalf("client reply = %q, %v", buf[:n], err)
	}

	other, err := net.DialUDP("udp4", nil, p.listener.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Write([]byte{0x01}); err != nil {
		t.Fatal(err)
	}
	got, otherBackendClient := readBackend(t, backend)
	if len(got) != 29 || otherBackendClient.Port == backendClient.Port {
		t.Fatalf("second client did not get its own PROXY session: %x from %s", got, otherBackendClient)
	}
}

func TestUDPProxyIdleSessionStartsWithNewHeader(t *testing.T) {
	p, backend := startUDPTestProxy(t, 80*time.Millisecond)
	client, err := net.DialUDP("udp4", nil, p.listener.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Write([]byte{0x05}); err != nil {
		t.Fatal(err)
	}
	first, oldSocket := readBackend(t, backend)
	if len(first) != 29 {
		t.Fatalf("first packet missing header: %x", first)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		p.mu.Lock()
		count := len(p.sessions)
		p.mu.Unlock()
		if count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle session was not removed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := client.Write([]byte{0x05}); err != nil {
		t.Fatal(err)
	}
	second, newSocket := readBackend(t, backend)
	if len(second) != 29 {
		t.Fatalf("new session did not start with a header: %x from %s (previous socket %s)", second, newSocket, oldSocket)
	}
}

func TestUDPProxySeparatesSamePortOnDifferentClientIPs(t *testing.T) {
	p, backend := startUDPTestProxy(t, time.Second)
	first := &net.UDPAddr{IP: net.ParseIP("192.0.2.1"), Port: 40000}
	second := &net.UDPAddr{IP: net.ParseIP("192.0.2.2"), Port: 40000}
	p.forward(first.AddrPort(), []byte{0x01})
	p.forward(second.AddrPort(), []byte{0x01})
	firstData, firstBackend := readBackend(t, backend)
	secondData, secondBackend := readBackend(t, backend)
	correctAddresses := (bytes.Equal(firstData[16:20], first.IP.To4()) && bytes.Equal(secondData[16:20], second.IP.To4())) ||
		(bytes.Equal(firstData[16:20], second.IP.To4()) && bytes.Equal(secondData[16:20], first.IP.To4()))
	if len(firstData) != 29 || len(secondData) != 29 || !correctAddresses ||
		firstBackend.Port == secondBackend.Port {
		t.Fatalf("clients with the same port were mixed: %x from %s, %x from %s", firstData, firstBackend, secondData, secondBackend)
	}
}

func TestUDPProxySessionLimitKeepsExistingClient(t *testing.T) {
	p, backend := startUDPTestProxy(t, time.Second)
	p.maxSessions = 1
	first := &net.UDPAddr{IP: net.ParseIP("192.0.2.1"), Port: 40000}
	second := &net.UDPAddr{IP: net.ParseIP("192.0.2.2"), Port: 40000}
	p.forward(first.AddrPort(), []byte{0x01})
	_, _ = readBackend(t, backend)
	p.forward(second.AddrPort(), []byte{0x01})
	p.mu.Lock()
	count := len(p.sessions)
	p.mu.Unlock()
	if count != 1 {
		t.Fatalf("session limit allowed %d sessions", count)
	}
	p.forward(first.AddrPort(), []byte{0x05})
	packet, _ := readBackend(t, backend)
	if !bytes.Equal(packet, []byte{0x05}) {
		t.Fatalf("existing session stopped forwarding: %x", packet)
	}
}

func TestUDPProxyIPv6(t *testing.T) {
	backend, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback})
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	defer backend.Close()
	p, err := newUDPProxy(config.ProxyConfig{
		ListenIP: "::1", ServerHost: "::1", ServerPort: backend.LocalAddr().(*net.UDPAddr).Port,
	}, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- p.serve() }()
	defer func() {
		_ = p.Close()
		if err := <-done; err != nil {
			t.Errorf("UDP proxy stopped: %v", err)
		}
	}()
	client, err := net.DialUDP("udp6", nil, p.listener.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Write([]byte{0x01}); err != nil {
		t.Fatal(err)
	}
	packet, _ := readBackend(t, backend)
	if len(packet) != 53 || packet[13] != 0x22 || binary.BigEndian.Uint16(packet[14:16]) != 36 ||
		!bytes.Equal(packet[16:32], net.IPv6loopback.To16()) || packet[52] != 0x01 {
		t.Fatalf("invalid IPv6 PROXY datagram: %x", packet)
	}
}

func TestUDPProxyActiveSessionSurvivesReadDeadline(t *testing.T) {
	p, backend := startUDPTestProxy(t, 250*time.Millisecond)
	client, err := net.DialUDP("udp4", nil, p.listener.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Write([]byte{0x01}); err != nil {
		t.Fatal(err)
	}
	_, originalSocket := readBackend(t, backend)
	for i := 0; i < 6; i++ {
		time.Sleep(75 * time.Millisecond)
		if _, err := client.Write([]byte{0x80}); err != nil {
			t.Fatal(err)
		}
		packet, socket := readBackend(t, backend)
		if !bytes.Equal(packet, []byte{0x80}) || socket.Port != originalSocket.Port {
			t.Fatalf("active session was recreated: %x from %s (original %s)", packet, socket, originalSocket)
		}
	}
}

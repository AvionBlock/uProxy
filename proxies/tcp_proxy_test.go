package proxies

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"uproxy/config"
)

func TestTCPProxyHeaderWithHostnameAndHalfClose(t *testing.T) {
	backend, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	frontend, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer frontend.Close()

	backendResult := make(chan error, 1)
	go func() {
		conn, err := backend.Accept()
		if err != nil {
			backendResult <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		header := make([]byte, 28)
		if _, err := io.ReadFull(conn, header); err != nil {
			backendResult <- err
			return
		}
		payload, err := io.ReadAll(conn)
		if err == nil && (!bytes.Equal(payload, []byte("hello")) || header[13] != 0x11 ||
			!bytes.Equal(header[20:24], net.IPv4(127, 0, 0, 1).To4()) ||
			int(binary.BigEndian.Uint16(header[26:28])) != backend.Addr().(*net.TCPAddr).Port) {
			err = io.ErrUnexpectedEOF
		}
		if err == nil {
			_, err = conn.Write([]byte("world"))
		}
		backendResult <- err
	}()

	forwardResult := make(chan struct{})
	go func() {
		conn, err := frontend.Accept()
		if err == nil {
			handleTCPConnection(conn, &config.Config{}, config.ProxyConfig{
				ServerHost: "localhost", ServerPort: backend.Addr().(*net.TCPAddr).Port,
			})
		}
		close(forwardResult)
	}()

	client, err := net.DialTimeout("tcp4", frontend.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := client.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := client.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	reply, err := io.ReadAll(client)
	if err != nil || !bytes.Equal(reply, []byte("world")) {
		t.Fatalf("reply = %q, %v", reply, err)
	}
	if err := <-backendResult; err != nil {
		t.Fatalf("backend: %v", err)
	}
	<-forwardResult
}

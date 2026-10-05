package proxies

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
	"uproxy/config"
	"uproxy/core"
)

// StartTCPProxy starts a TCP proxy server listening on the port defined in cfg.ListenPort.
// It accepts incoming TCP connections and forwards them to the configured backend server.
// Uses Proxy Protocol v2 header to forward client info to backend.
func StartTCPProxy(config *config.Config, cfg config.ProxyConfig) error {
	listenAddr := net.JoinHostPort(cfg.ListenIP, strconv.Itoa(cfg.ListenPort))
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("bind TCP listener on %s: %w", listenAddr, err)
	}
	defer listener.Close()

	logger.Info("TCP proxy listening on %s", listenAddr)

	for {
		clientConn, err := listener.Accept()
		if err != nil {
			return fmt.Errorf("accept TCP connection: %w", err)
		}
		// Handle each connection concurrently
		go handleTCPConnection(clientConn, config, cfg)
	}
}

// handleTCPConnection handles a single TCP client connection.
// It establishes a connection to the backend server, sends Proxy Protocol v2 header,
// then pipes data between client and server bidirectionally.
func handleTCPConnection(clientConn net.Conn, config *config.Config, cfg config.ProxyConfig) {
	defer clientConn.Close()

	clientAddr, ok := clientConn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		logger.Warn("Could not parse client address")
		return
	}

	serverAddr := net.JoinHostPort(cfg.ServerHost, strconv.Itoa(cfg.ServerPort))
	network := "tcp4"
	if clientAddr.IP.To4() == nil {
		network = "tcp6"
	}
	serverConn, err := net.DialTimeout(network, serverAddr, 10*time.Second)
	if err != nil {
		logger.Error("Failed to connect to TCP server %s: %v", serverAddr, err)
		return
	}
	defer serverConn.Close()

	if config.DebugMode {
		logger.Info("New TCP connection from %s to %s", clientAddr.String(), serverAddr)
	}

	// Prepare and send Proxy Protocol v2 header to backend server
	serverTCPAddr := serverConn.RemoteAddr().(*net.TCPAddr)
	proto := byte(0x11) // TCP4
	if clientAddr.IP.To4() == nil {
		proto = 0x21 // TCP6
	}
	ppHeader, err := core.EncodeProxyProtocolV2(
		proto,
		clientAddr.IP,
		clientAddr.Port,
		serverTCPAddr.IP,
		serverTCPAddr.Port,
	)
	if err != nil {
		logger.Error("Cannot encode Proxy Protocol header: %v", err)
		return
	}
	_, err = io.Copy(serverConn, bytes.NewReader(ppHeader))
	if err != nil {
		logger.Error("Failed to send Proxy Protocol header: %v", err)
		return
	}

	// Start piping client → server in a separate goroutine
	go func() {
		_, err := io.Copy(serverConn, clientConn)
		if err != nil {
			logger.Warn("Client → Server error: %v", err)
		}
		if tcp, ok := serverConn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()

	// Pipe server → client (blocking)
	_, err = io.Copy(clientConn, serverConn)
	if err != nil {
		logger.Warn("Server → Client error: %v", err)
	}
	if tcp, ok := clientConn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
}

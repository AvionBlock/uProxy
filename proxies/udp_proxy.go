package proxies

import (
	"bytes"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
	"uproxy/config"
	"uproxy/core"
)

// ClientInfo holds info about a UDP client connection.
type ClientInfo struct {
	Addr       *net.UDPAddr // Client UDP address
	HeaderSent bool         // Whether Proxy Protocol header was sent
	LastActive time.Time    // Last activity timestamp
	Conn       *net.UDPConn // UDP connection dedicated to client
}

var (
	clients   = make(map[int]*ClientInfo) // Map client port -> ClientInfo
	clientsMu sync.Mutex                  // Mutex to protect clients map
)

// StartUDPProxy starts the UDP proxy server.
// It listens on cfg.ListenPort and forwards UDP packets between clients and backend server.
// Maintains per-client UDP sockets and handles Proxy Protocol v2 header for new clients.
func StartUDPProxy(config *config.Config, cfg config.ProxyConfig) {
	serverIP := net.ParseIP(cfg.ServerHost)
	serverPort := cfg.ServerPort
	localPort := cfg.ListenPort
	debugMode := config.DebugMode

	bindIP := net.ParseIP(cfg.ListenIP)
	if bindIP == nil {
		logger.Error("Invalid ListenIP: %s", cfg.ListenIP)
		return
	}
	
	addr := &net.UDPAddr{IP: bindIP, Port: localPort}
	mainConn, err := net.ListenUDP("udp", addr)
	if err != nil {
		logger.Error("Failed to bind: %v", err)
		return
	}
	defer mainConn.Close()

	logger.Info("UDP proxy listening on %s", mainConn.LocalAddr())

	buf := make([]byte, 2048)
	for {
		n, raddr, err := mainConn.ReadFromUDP(buf)
		if err != nil {
			logger.Warn("read error: %v", err)
			continue
		}
		data := make([]byte, n)
		copy(data, buf[:n])

		// Handle each UDP packet concurrently
		go handlePacket(mainConn, data, raddr, serverIP, serverPort, localPort, debugMode)
	}
}

// handlePacket processes a single UDP packet from client or server.
// Differentiates packets by source IP/port, manages client sessions,
// sends Proxy Protocol header when needed, and forwards packets.
func handlePacket(mainConn *net.UDPConn, data []byte, raddr *net.UDPAddr, serverIP net.IP, serverPort, localPort int, debugMode bool) {
	if len(data) == 0 {
		return
	}
	typeHex := fmt.Sprintf("%02x", data[0])

	clientsMu.Lock()
	defer clientsMu.Unlock()

	if !raddr.IP.Equal(serverIP) {
		// Packet from client
		if debugMode {
			logger.Info("0x%s packet received from client: %s:%d", typeHex, raddr.IP, raddr.Port)
		} else if _, exists := clients[raddr.Port]; !exists {
			logger.Info("New client connected: %s:%d", raddr.IP, raddr.Port)
		}

		client, exists := clients[raddr.Port]
		now := time.Now()

		if !exists {
			// Create new UDP socket for this client port
			clientAddr := &net.UDPAddr{IP: net.IPv4zero, Port: raddr.Port}
			cliConn, err := net.ListenUDP("udp", clientAddr)
			if err != nil {
				logger.Error("Failed to create socket for client port %d: %v", raddr.Port, err)
				return
			}

			client = &ClientInfo{
				Addr:       raddr,
				HeaderSent: false,
				LastActive: now,
				Conn:       cliConn,
			}
			clients[raddr.Port] = client

			// Start goroutine to read responses from server and send back to client
			go handleServerToClient(mainConn, client, serverIP, serverPort, localPort, debugMode)
		} else {
			client.LastActive = now
		}

		// Send Proxy Protocol v2 header on first specific packet types
		if !client.HeaderSent && (strings.Contains(typeHex, "01") || strings.Contains(typeHex, "05")) {
			logger.Warn("Send with header for client %d!", raddr.Port)
			messageWithHeader := append(core.EncodeProxyProtocolV2(0x12, raddr.IP, raddr.Port, serverIP, serverPort), data...)
			_, err := client.Conn.WriteToUDP(messageWithHeader, &net.UDPAddr{IP: serverIP, Port: serverPort})
			if err != nil {
				logger.Error("Error sending to server with header: %v", err)
			}
			client.HeaderSent = true
		} else {
			_, err := client.Conn.WriteToUDP(data, &net.UDPAddr{IP: serverIP, Port: serverPort})
			if err != nil {
				logger.Error("Error sending to server: %v", err)
			}
		}

	} else if raddr.Port == serverPort {
		// Packet from server
		if debugMode {
			logger.Info("0x%s packet received from server: %s:%d", typeHex, raddr.IP, raddr.Port)
		}

		// Find client by matching UDP socket local port
		sendPort := 0
		for port, c := range clients {
			if c.Conn.LocalAddr().(*net.UDPAddr).Port == raddr.Port {
				sendPort = port
				break
			}
		}
		if sendPort == 0 {
			logger.Warn("No client found for server response port %d", raddr.Port)
			return
		}

		client := clients[sendPort]
		// Timeout clients inactive for 30 seconds
		if time.Since(client.LastActive) > 30*time.Second {
			logger.Warn("Client %d timed out, closing socket", sendPort)
			client.Conn.Close()
			delete(clients, sendPort)
		} else {
			// Replace server port in data with local listening port if needed
			oldPort := []byte(fmt.Sprintf("%05d", serverPort))
			newPort := []byte(fmt.Sprintf("%05d", localPort))
			dataToSend := data
			if !bytes.Equal(oldPort, newPort) {
				dataToSend = bytes.ReplaceAll(data, oldPort, newPort)
			}

			_, err := mainConn.WriteToUDP(dataToSend, client.Addr)
			if err != nil {
				logger.Error("Error sending response to client: %v", err)
			}
		}
	}
}

// handleServerToClient reads packets from the backend server via the client's UDP socket
// and forwards them back to the client via the main listening socket.
// Also handles client timeout and socket cleanup.
func handleServerToClient(mainConn *net.UDPConn, client *ClientInfo, serverIP net.IP, serverPort, localPort int, debugMode bool) {
	buf := make([]byte, 2048)
	for {
		n, from, err := client.Conn.ReadFromUDP(buf)
		if err != nil {
			logger.Warn("Socket %d closed or error: %v", client.Addr.Port, err)
			clientsMu.Lock()
			delete(clients, client.Addr.Port)
			clientsMu.Unlock()
			return
		}
		respData := make([]byte, n)
		copy(respData, buf[:n])

		if debugMode {
			logger.Info("0x%x packet received from server: %s:%d", respData[0], from.IP, from.Port)
		}

		// Check for client timeout
		if time.Since(client.LastActive) > 30*time.Second {
			logger.Warn("Client %d timed out, closing socket", client.Addr.Port)
			client.Conn.Close()
			clientsMu.Lock()
			delete(clients, client.Addr.Port)
			clientsMu.Unlock()
			return
		}

		_, err = mainConn.WriteToUDP(respData, client.Addr)
		if err != nil {
			logger.Error("Error sending response to client: %v", err)
		}
	}
}

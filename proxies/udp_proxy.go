package proxies

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"uproxy/config"
	"uproxy/core"
)

const udpIdleTimeout = 30 * time.Second
const defaultMaxUDPSessions = 4096

type udpSession struct {
	client     *net.UDPAddr
	conn       *net.UDPConn
	lastActive time.Time // guarded by udpProxy.mu
}

type udpProxy struct {
	listener            *net.UDPConn
	backend             *net.UDPAddr
	network             string
	debug               bool
	idle                time.Duration
	maxSessions         int
	mu                  sync.Mutex
	sessions            map[netip.AddrPort]*udpSession
	closed              bool
	lastCapacityWarning time.Time
}

// StartUDPProxy relays each client's datagrams through a dedicated backend
// socket. Geyser identifies the client by that socket after the first PROXY v2
// header, so subsequent datagrams must use the same socket without a header.
func StartUDPProxy(conf *config.Config, cfg config.ProxyConfig) error {
	idle := udpIdleTimeout
	if cfg.UDPIdleTimeoutSeconds > 0 {
		idle = time.Duration(cfg.UDPIdleTimeoutSeconds) * time.Second
	}
	p, err := newUDPProxy(cfg, conf.DebugMode, idle)
	if err != nil {
		return err
	}
	defer p.Close()
	logger.Info("UDP proxy listening on %s -> %s", p.listener.LocalAddr(), p.backend)
	return p.serve()
}

func newUDPProxy(cfg config.ProxyConfig, debug bool, idle time.Duration) (*udpProxy, error) {
	listenIP := cfg.ListenIP
	if listenIP == "" {
		listenIP = "0.0.0.0"
	}
	bindIP := net.ParseIP(listenIP)
	if bindIP == nil {
		return nil, fmt.Errorf("invalid UDP listen IP %q", listenIP)
	}
	if idle <= 0 {
		return nil, fmt.Errorf("UDP idle timeout must be positive")
	}
	maxSessions := cfg.MaxUDPSessions
	if maxSessions == 0 {
		maxSessions = defaultMaxUDPSessions
	}
	if maxSessions < 0 {
		return nil, fmt.Errorf("UDP session limit must be positive")
	}
	network := "udp6"
	if bindIP.To4() != nil {
		network = "udp4"
	}
	backend, err := net.ResolveUDPAddr(network, net.JoinHostPort(cfg.ServerHost, strconv.Itoa(cfg.ServerPort)))
	if err != nil {
		return nil, fmt.Errorf("resolve UDP backend: %w", err)
	}
	listener, err := net.ListenUDP(network, &net.UDPAddr{IP: bindIP, Port: cfg.ListenPort})
	if err != nil {
		return nil, fmt.Errorf("bind UDP listener: %w", err)
	}
	return &udpProxy{
		listener: listener, backend: backend, network: network,
		debug: debug, idle: idle, maxSessions: maxSessions, sessions: make(map[netip.AddrPort]*udpSession),
	}, nil
}

func (p *udpProxy) serve() error {
	buf := make([]byte, 65535)
	for {
		n, client, err := p.listener.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("read UDP client: %w", err)
		}
		if n == 0 || client == p.backend.AddrPort() {
			continue
		}
		p.forward(client, buf[:n])
	}
}

func (p *udpProxy) forward(client netip.AddrPort, data []byte) {
	key := client
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	session := p.sessions[key]
	packet := data
	isNew := false
	if session == nil {
		isNew = true
		if len(p.sessions) >= p.maxSessions {
			if time.Since(p.lastCapacityWarning) >= time.Minute {
				logger.Warn("UDP session limit (%d) reached; dropping new clients", p.maxSessions)
				p.lastCapacityWarning = time.Now()
			}
			return
		}
		proto := byte(0x12) // UDP4
		if p.network == "udp6" {
			proto = 0x22 // UDP6
		}
		header, err := core.EncodeProxyProtocolV2(proto, client.Addr().AsSlice(), int(client.Port()), p.backend.IP, p.backend.Port)
		if err != nil {
			logger.Warn("Cannot forward UDP client %s: %v", client, err)
			return
		}
		// Let the OS select the backend-facing source IP. The public listen IP
		// may be a different interface (or Geyser may be on loopback).
		conn, err := net.DialUDP(p.network, nil, p.backend)
		if err != nil {
			logger.Error("Cannot connect UDP backend for %s: %v", client, err)
			return
		}
		session = &udpSession{client: net.UDPAddrFromAddrPort(client), conn: conn}
		p.sessions[key] = session
		packet = make([]byte, len(header)+len(data))
		copy(packet, header)
		copy(packet[len(header):], data)
		logger.Info("New UDP client: %s", client)
		go p.relayResponses(key, session)
	}
	session.lastActive = time.Now()
	if isNew {
		if err := session.conn.SetReadDeadline(session.lastActive.Add(p.idle)); err != nil {
			logger.Error("Set UDP deadline for %s: %v", client, err)
			p.removeSessionLocked(key, session)
			return
		}
	}
	if _, err := session.conn.Write(packet); err != nil {
		logger.Error("Forward UDP packet from %s: %v", client, err)
		p.removeSessionLocked(key, session)
	} else if p.debug {
		logger.Info("Forwarded %d UDP bytes from %s", len(data), client)
	}
}

func (p *udpProxy) relayResponses(key netip.AddrPort, session *udpSession) {
	buf := make([]byte, 65535)
	for {
		n, err := session.conn.Read(buf)
		if err != nil {
			p.mu.Lock()
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() &&
				p.sessions[key] == session && time.Since(session.lastActive) < p.idle {
				_ = session.conn.SetReadDeadline(session.lastActive.Add(p.idle))
				p.mu.Unlock()
				continue
			}
			p.removeSessionLocked(key, session)
			p.mu.Unlock()
			return
		}
		if p.debug {
			logger.Info("Received %d UDP bytes for %s", n, session.client)
		}
		if _, err := p.listener.WriteToUDP(buf[:n], session.client); err != nil {
			if !errors.Is(err, net.ErrClosed) {
				logger.Error("Return UDP packet to %s: %v", session.client, err)
			}
			p.mu.Lock()
			p.removeSessionLocked(key, session)
			p.mu.Unlock()
			return
		}
	}
}

func (p *udpProxy) removeSessionLocked(key netip.AddrPort, session *udpSession) {
	if p.sessions[key] == session {
		delete(p.sessions, key)
		_ = session.conn.Close()
	}
}

func (p *udpProxy) Close() error {
	p.mu.Lock()
	p.closed = true
	for key, session := range p.sessions {
		p.removeSessionLocked(key, session)
	}
	p.mu.Unlock()
	return p.listener.Close()
}

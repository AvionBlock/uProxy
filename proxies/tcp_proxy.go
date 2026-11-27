package proxies

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"uproxy/config"
	"uproxy/core"
)

// TCPProxy forwards TCP connections to one backend, optionally announcing the
// client with a PROXY protocol v2 header. For Bedrock this carries the NetherNet
// HTTP signaling (GET /v1/join, POST /v1/join/{networkId}); Geyser's built-in
// signaling reads the header when uProxy's address is listed in
// "haproxy-protocol-whitelisted-ips".
type TCPProxy struct {
	cfg       config.ProxyConfig
	resolver  *core.HostResolver
	useHeader bool

	mu sync.Mutex
	ln net.Listener
}

// NewTCPProxy prepares a TCP proxy for cfg. Call Run to start it.
func NewTCPProxy(cfg config.ProxyConfig) *TCPProxy {
	return &TCPProxy{
		cfg:       cfg,
		resolver:  core.NewHostResolver(cfg.ServerHost, time.Minute),
		useHeader: cfg.UseProxyProtocol(),
	}
}

// StartTCPProxy runs a TCP proxy until ctx is cancelled. Kept for compatibility.
func StartTCPProxy(ctx context.Context, cfg config.ProxyConfig) error {
	return NewTCPProxy(cfg).Run(ctx)
}

// LocalAddr returns the bound address once Run has started, else nil.
func (p *TCPProxy) LocalAddr() net.Addr {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln == nil {
		return nil
	}
	return p.ln.Addr()
}

// Run binds the listener and serves until ctx is cancelled.
func (p *TCPProxy) Run(ctx context.Context) error {
	listenAddr := net.JoinHostPort(p.cfg.ListenAddr().String(), strconv.Itoa(p.cfg.ListenPort))
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("tcp %s: bind %s: %w", p.cfg.Label(), listenAddr, err)
	}
	p.mu.Lock()
	p.ln = ln
	p.mu.Unlock()

	logger.Info("[%s] TCP proxy listening on %s -> %s:%d (proxy protocol: %t)",
		p.cfg.Label(), ln.Addr(), p.cfg.ServerHost, p.cfg.ServerPort, p.useHeader)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	var delay time.Duration
	for {
		clientConn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Timeout() {
				continue
			}
			// Back off on transient accept errors (EMFILE etc.).
			if delay == 0 {
				delay = 5 * time.Millisecond
			} else if delay < time.Second {
				delay *= 2
			}
			logger.Warn("[%s] accept error: %v (retrying in %s)", p.cfg.Label(), err, delay)
			time.Sleep(delay)
			continue
		}
		delay = 0
		go p.handle(ctx, clientConn)
	}
}

func (p *TCPProxy) handle(ctx context.Context, clientConn net.Conn) {
	defer clientConn.Close()

	clientAddr, ok := clientConn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		logger.Warn("[%s] could not parse client address %v", p.cfg.Label(), clientConn.RemoteAddr())
		return
	}
	localAddr, _ := clientConn.LocalAddr().(*net.TCPAddr)

	serverIP, err := p.resolver.IP(ctx)
	if err != nil {
		logger.Error("[%s] resolve %s: %v", p.cfg.Label(), p.cfg.ServerHost, err)
		return
	}
	serverAddr := net.JoinHostPort(serverIP.String(), strconv.Itoa(p.cfg.ServerPort))

	dialer := net.Dialer{Timeout: p.cfg.DialTimeoutOrDefault()}
	if bind := p.cfg.UpstreamBind(); bind != nil {
		dialer.LocalAddr = &net.TCPAddr{IP: bind}
	}
	serverConn, err := dialer.DialContext(ctx, "tcp", serverAddr)
	if err != nil {
		logger.Error("[%s] %s: failed to connect to backend %s: %v", p.cfg.Label(), clientAddr, serverAddr, err)
		return
	}
	defer serverConn.Close()

	start := time.Now()
	logger.Info("[%s] New TCP connection %s -> %s", p.cfg.Label(), clientAddr, serverAddr)

	if p.useHeader {
		var dstIP net.IP
		dstPort := p.cfg.ListenPort
		if localAddr != nil {
			dstIP = localAddr.IP
			dstPort = localAddr.Port
		}
		header := core.EncodeProxyProtocolV2(core.TransportTCP, clientAddr.IP, clientAddr.Port, dstIP, dstPort)
		if _, err := serverConn.Write(header); err != nil {
			logger.Error("[%s] %s: failed to send PROXY header: %v", p.cfg.Label(), clientAddr, err)
			return
		}
	}

	up, down := pipe(ctx, clientConn, serverConn)
	logger.Info("[%s] TCP connection %s closed after %s, up %d B, down %d B",
		p.cfg.Label(), clientAddr, time.Since(start).Round(time.Second), up, down)
}

// pipe copies data in both directions until both sides have finished. Each
// direction half-closes its destination when the source hits EOF, which lets
// HTTP clients and Minecraft alike shut down cleanly.
func pipe(ctx context.Context, client, server net.Conn) (up, down int64) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		up, _ = io.Copy(server, client)
		closeWrite(server)
	}()
	go func() {
		defer wg.Done()
		down, _ = io.Copy(client, server)
		closeWrite(client)
	}()
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		_ = client.Close()
		_ = server.Close()
		<-done
	}
	return up, down
}

func closeWrite(c net.Conn) {
	type closeWriter interface{ CloseWrite() error }
	if cw, ok := c.(closeWriter); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

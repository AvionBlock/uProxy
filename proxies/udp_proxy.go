package proxies

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"uproxy/config"
	"uproxy/core"
)

// udpBufferSize is large enough for any UDP payload; Bedrock traffic is MTU-bound anyway.
const udpBufferSize = 65535

// UDPProxy forwards UDP datagrams between many clients and one backend.
//
// Every client (ip:port) gets its own flow with a dedicated, connected upstream
// socket, so the backend sees one source port per client. This is what both the
// RakNet and the NetherNet PROXY-protocol conventions of Geyser (CloudburstMC
// Network) rely on: the backend maps the relay's source address to the real
// client address carried in the header and replies to that same relay socket.
//
// Header policy per flow:
//   - raknet:    header on the first datagram, and again whenever the upstream direction
//     was idle for RakNetHeaderResendIdle (the backend forgets the mapping after 10 s).
//   - nethernet: header on every datagram (Geyser "webrtc-proxy-protocol-relays").
//   - plain:     no header at all.
//   - auto:      classify the first datagram of the flow (STUN/DTLS => nethernet,
//     anything else => raknet).
type UDPProxy struct {
	cfg      config.ProxyConfig
	resolver *core.HostResolver
	listen   *net.UDPAddr

	useHeader   bool
	mode        config.UDPMode
	idle        time.Duration
	resendIdle  time.Duration
	maxFlows    int
	rewritePong bool

	conn *net.UDPConn

	mu     sync.Mutex
	flows  map[netip.AddrPort]*udpFlow
	closed bool
}

type udpFlow struct {
	key        netip.AddrPort
	client     *net.UDPAddr
	upstream   *net.UDPConn
	kind       core.DatagramKind
	policy     config.UDPMode
	header     []byte
	resendIdle time.Duration

	// Only touched by the listener goroutine.
	headerSent       bool
	lastUpstreamSend time.Time

	created    time.Time
	lastActive atomic.Int64
	closed     atomic.Bool
	bytesUp    atomic.Uint64
	bytesDown  atomic.Uint64
}

// NewUDPProxy prepares a UDP proxy for cfg. Call Run to start it.
func NewUDPProxy(cfg config.ProxyConfig) *UDPProxy {
	return &UDPProxy{
		cfg:         cfg,
		resolver:    core.NewHostResolver(cfg.ServerHost, time.Minute),
		listen:      &net.UDPAddr{IP: cfg.ListenAddr(), Port: cfg.ListenPort},
		useHeader:   cfg.UseProxyProtocol(),
		mode:        cfg.EffectiveUDPMode(),
		idle:        cfg.IdleTimeoutOrDefault(),
		resendIdle:  cfg.HeaderResendIdleOrDefault(),
		maxFlows:    cfg.MaxUDPFlowsOrDefault(),
		rewritePong: cfg.PongRewriteEnabled() && cfg.ListenPort != cfg.ServerPort,
		flows:       make(map[netip.AddrPort]*udpFlow),
	}
}

// StartUDPProxy runs a UDP proxy until ctx is cancelled. Kept for compatibility.
func StartUDPProxy(ctx context.Context, cfg config.ProxyConfig) error {
	return NewUDPProxy(cfg).Run(ctx)
}

// LocalAddr returns the bound address once Run has started, else nil.
func (p *UDPProxy) LocalAddr() *net.UDPAddr {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil {
		return nil
	}
	return p.conn.LocalAddr().(*net.UDPAddr)
}

// FlowCount returns the number of live flows.
func (p *UDPProxy) FlowCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.flows)
}

// Run binds the listener and serves until ctx is cancelled.
func (p *UDPProxy) Run(ctx context.Context) error {
	if !p.useHeader {
		p.mode = config.UDPModePlain
	}

	conn, err := net.ListenUDP("udp", p.listen)
	if err != nil {
		return fmt.Errorf("udp %s: bind %s: %w", p.cfg.Label(), p.listen, err)
	}
	_ = conn.SetReadBuffer(4 << 20)
	_ = conn.SetWriteBuffer(4 << 20)

	p.mu.Lock()
	p.conn = conn
	p.mu.Unlock()

	logger.Info("[%s] UDP proxy listening on %s -> %s:%d (proxy protocol: %s)",
		p.cfg.Label(), conn.LocalAddr(), p.cfg.ServerHost, p.cfg.ServerPort, p.describeMode())

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go p.reaper(ctx)
	go func() {
		<-ctx.Done()
		p.shutdown()
	}()

	buf := make([]byte, udpBufferSize)
	scratch := make([]byte, 0, udpBufferSize+core.ProxyProtocolV2LenIPv6)
	for {
		n, from, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil || p.isClosed() {
				return nil
			}
			logger.Warn("[%s] UDP read error: %v", p.cfg.Label(), err)
			continue
		}
		if n == 0 {
			continue
		}
		data := buf[:n]
		now := time.Now()
		from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())

		f, isNew, err := p.flowFor(ctx, from, data)
		if err != nil {
			logger.Warn("[%s] cannot create flow for %s: %v", p.cfg.Label(), from, err)
			continue
		}
		f.lastActive.Store(now.UnixNano())
		if isNew {
			logger.Info("[%s] New %s client %s (flows: %d)", p.cfg.Label(), f.kind, from, p.FlowCount())
		}

		out := data
		if f.needsHeader(now) {
			scratch = append(scratch[:0], f.header...)
			scratch = append(scratch, data...)
			out = scratch
			if !f.headerSent {
				logger.Debug("[%s] %s: sending PROXY v2 header (%s)", p.cfg.Label(), from, f.policy)
			} else if f.policy == config.UDPModeRakNet {
				logger.Debug("[%s] %s: upstream idle %s, re-sending PROXY v2 header", p.cfg.Label(), from, now.Sub(f.lastUpstreamSend).Round(time.Millisecond))
			}
			f.headerSent = true
		}
		f.lastUpstreamSend = now

		if logger.DebugEnabled() {
			logger.Debug("[%s] 0x%02x %d B from client %s (%s)", p.cfg.Label(), data[0], n, from, f.kind)
		}
		if _, err := f.upstream.Write(out); err != nil {
			if !f.closed.Load() {
				logger.Warn("[%s] %s: error sending to server: %v", p.cfg.Label(), from, err)
			}
			continue
		}
		f.bytesUp.Add(uint64(n))
	}
}

func (p *UDPProxy) describeMode() string {
	if !p.useHeader {
		return "off"
	}
	return string(p.mode)
}

func (p *UDPProxy) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func (p *UDPProxy) shutdown() {
	p.mu.Lock()
	p.closed = true
	flows := make([]*udpFlow, 0, len(p.flows))
	for _, f := range p.flows {
		flows = append(flows, f)
	}
	conn := p.conn
	p.mu.Unlock()

	for _, f := range flows {
		p.closeFlow(f, "shutdown")
	}
	if conn != nil {
		_ = conn.Close()
	}
}

// flowFor returns the flow for a client, creating it when needed.
func (p *UDPProxy) flowFor(ctx context.Context, from netip.AddrPort, first []byte) (*udpFlow, bool, error) {
	p.mu.Lock()
	if f, ok := p.flows[from]; ok {
		p.mu.Unlock()
		return f, false, nil
	}
	if len(p.flows) >= p.maxFlows {
		p.mu.Unlock()
		return nil, false, fmt.Errorf("flow limit %d reached", p.maxFlows)
	}
	p.mu.Unlock()

	f, err := p.newFlow(ctx, from, first)
	if err != nil {
		return nil, false, err
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = f.upstream.Close()
		return nil, false, errors.New("proxy is shutting down")
	}
	if existing, ok := p.flows[from]; ok {
		// Cannot happen while a single goroutine reads, but stay safe.
		p.mu.Unlock()
		_ = f.upstream.Close()
		return existing, false, nil
	}
	p.flows[from] = f
	p.mu.Unlock()

	go p.serveDownstream(f)
	return f, true, nil
}

func (p *UDPProxy) newFlow(ctx context.Context, from netip.AddrPort, first []byte) (*udpFlow, error) {
	serverIP, err := p.resolver.IP(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", p.cfg.ServerHost, err)
	}

	var laddr *net.UDPAddr
	if bind := p.cfg.UpstreamBind(); bind != nil {
		laddr = &net.UDPAddr{IP: bind}
	}
	upstream, err := net.DialUDP("udp", laddr, &net.UDPAddr{IP: serverIP, Port: p.cfg.ServerPort})
	if err != nil {
		return nil, fmt.Errorf("dial backend: %w", err)
	}

	kind := core.ClassifyDatagram(first)
	policy := p.mode
	switch p.mode {
	case config.UDPModeAuto:
		if kind == core.KindNetherNet {
			policy = config.UDPModeNetherNet
		} else {
			policy = config.UDPModeRakNet
			if kind == core.KindUnknown {
				kind = core.KindRakNet
			}
		}
	case config.UDPModeRakNet:
		kind = core.KindRakNet
	case config.UDPModeNetherNet:
		kind = core.KindNetherNet
	case config.UDPModePlain:
		if kind == core.KindUnknown {
			kind = core.KindRakNet
		}
	}

	client := net.UDPAddrFromAddrPort(from)
	now := time.Now()
	f := &udpFlow{
		key:        from,
		client:     client,
		upstream:   upstream,
		kind:       kind,
		policy:     policy,
		header:     core.EncodeProxyProtocolV2(core.TransportUDP, client.IP, client.Port, p.listen.IP, p.listen.Port),
		resendIdle: p.resendIdle,
		created:    now,
	}
	f.lastActive.Store(now.UnixNano())
	return f, nil
}

// needsHeader applies the per-flow header policy.
func (f *udpFlow) needsHeader(now time.Time) bool {
	switch f.policy {
	case config.UDPModeNetherNet:
		return true
	case config.UDPModeRakNet:
		return !f.headerSent || now.Sub(f.lastUpstreamSend) >= f.resendIdle
	default:
		return false
	}
}

func (p *UDPProxy) serveDownstream(f *udpFlow) {
	buf := make([]byte, udpBufferSize)
	consecutiveErrors := 0
	for {
		_ = f.upstream.SetReadDeadline(time.Now().Add(p.idle))
		n, err := f.upstream.Read(buf)
		if err != nil {
			if f.closed.Load() {
				return
			}
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Timeout() {
				p.closeFlow(f, "idle timeout")
				return
			}
			consecutiveErrors++
			logger.Debug("[%s] %s: upstream read error (%d): %v", p.cfg.Label(), f.key, consecutiveErrors, err)
			if consecutiveErrors >= 10 {
				p.closeFlow(f, "upstream errors")
				return
			}
			continue
		}
		consecutiveErrors = 0
		if n == 0 {
			continue
		}
		data := buf[:n]
		f.lastActive.Store(time.Now().UnixNano())

		if p.rewritePong && f.kind == core.KindRakNet && data[0] == core.RakNetIDUnconnectedPong {
			data = core.RewriteUnconnectedPongPorts(data, p.listen.Port)
		}
		if logger.DebugEnabled() {
			logger.Debug("[%s] 0x%02x %d B from server for %s", p.cfg.Label(), data[0], len(data), f.key)
		}
		if _, err := p.conn.WriteToUDPAddrPort(data, f.key); err != nil {
			if p.isClosed() {
				return
			}
			logger.Warn("[%s] %s: error sending to client: %v", p.cfg.Label(), f.key, err)
			continue
		}
		f.bytesDown.Add(uint64(n))
	}
}

func (p *UDPProxy) closeFlow(f *udpFlow, reason string) {
	if !f.closed.CompareAndSwap(false, true) {
		return
	}
	p.mu.Lock()
	if cur, ok := p.flows[f.key]; ok && cur == f {
		delete(p.flows, f.key)
	}
	remaining := len(p.flows)
	p.mu.Unlock()
	_ = f.upstream.Close()

	logger.Info("[%s] Client %s closed (%s) after %s, up %d B, down %d B (flows: %d)",
		p.cfg.Label(), f.key, reason, time.Since(f.created).Round(time.Second),
		f.bytesUp.Load(), f.bytesDown.Load(), remaining)
}

func (p *UDPProxy) reaper(ctx context.Context) {
	interval := p.idle / 4
	if interval < time.Second {
		interval = time.Second
	}
	if interval > 10*time.Second {
		interval = 10 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			cutoff := now.Add(-p.idle).UnixNano()
			var expired []*udpFlow
			p.mu.Lock()
			for _, f := range p.flows {
				if f.lastActive.Load() < cutoff {
					expired = append(expired, f)
				}
			}
			p.mu.Unlock()
			for _, f := range expired {
				p.closeFlow(f, "idle timeout")
			}
		}
	}
}

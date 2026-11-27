package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// Protocol selects what an L4 proxy entry forwards.
type Protocol string

const (
	// ProtocolTCP forwards TCP connections (Minecraft Java, HTTP signaling, ...).
	ProtocolTCP Protocol = "tcp"
	// ProtocolUDP forwards UDP datagrams (RakNet or NetherNet media traffic).
	ProtocolUDP Protocol = "udp"
	// ProtocolBedrock forwards both TCP and UDP on the same port. This is what a
	// NetherNet-era Bedrock server (Geyser, BDS) needs: TCP carries the HTTP
	// signaling, UDP carries RakNet and/or WebRTC gameplay.
	ProtocolBedrock Protocol = "bedrock"
)

// UDPMode controls how PROXY protocol headers are attached to UDP datagrams.
type UDPMode string

const (
	// UDPModeAuto detects the transport per flow from the first datagram.
	UDPModeAuto UDPMode = "auto"
	// UDPModeRakNet sends a header on the first datagram of a flow and again after the
	// flow was idle for RakNetHeaderResendIdle (Geyser forgets the mapping after 10 s).
	UDPModeRakNet UDPMode = "raknet"
	// UDPModeNetherNet sends a header on every datagram, as Geyser's
	// "webrtc-proxy-protocol-relays" expects.
	UDPModeNetherNet UDPMode = "nethernet"
	// UDPModePlain never sends a header (e.g. for Bedrock Dedicated Server).
	UDPModePlain UDPMode = "plain"
)

// Duration is a time.Duration that marshals to/from strings such as "30s".
type Duration time.Duration

// MarshalJSON renders the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON accepts either a Go duration string or a number of seconds.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		v, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", s, err)
		}
		*d = Duration(v)
		return nil
	}
	var secs float64
	if err := json.Unmarshal(b, &secs); err != nil {
		return fmt.Errorf("invalid duration %s", string(b))
	}
	*d = Duration(time.Duration(secs * float64(time.Second)))
	return nil
}

// ProxyConfig describes one L4 proxy listener.
type ProxyConfig struct {
	// Name is an optional label used in logs.
	Name string `json:"name,omitempty"`
	// ListenIP is the local address to bind; empty or "0.0.0.0" binds all IPv4 interfaces, "::" all interfaces.
	ListenIP string `json:"listenIP"`
	// ListenPort is the local port to bind. For "bedrock" it is used for both TCP and UDP.
	ListenPort int `json:"listenPort"`
	// ServerHost is the backend host name or IP.
	ServerHost string `json:"serverHost"`
	// ServerPort is the backend port.
	ServerPort int `json:"serverPort"`

	// Protocol is "tcp", "udp" or "bedrock" (TCP + UDP). When empty the legacy ProtoTCP flag decides.
	Protocol Protocol `json:"protocol,omitempty"`
	// ProtoTCP is the legacy switch kept for old config files: true = tcp, false = udp.
	ProtoTCP *bool `json:"protoTCP,omitempty"`

	// ProxyProtocol enables PROXY protocol v2 headers towards the backend. Defaults to true.
	ProxyProtocol *bool `json:"proxyProtocol,omitempty"`
	// UDPMode is "auto", "raknet", "nethernet" or "plain". Defaults to "auto".
	UDPMode UDPMode `json:"udpMode,omitempty"`
	// RewritePongPort rewrites the port fields of RakNet pongs to ListenPort when it differs from ServerPort. Defaults to true.
	RewritePongPort *bool `json:"rewritePongPort,omitempty"`
	// IdleTimeout closes a UDP flow that saw no traffic in either direction for this long. Defaults to 60s.
	IdleTimeout Duration `json:"idleTimeout,omitempty"`
	// RakNetHeaderResendIdle re-sends the PROXY header on a RakNet flow whose upstream direction was
	// idle for at least this long. Defaults to 10s (CloudburstMC Network's SESSION_TIMEOUT_MS).
	RakNetHeaderResendIdle Duration `json:"raknetHeaderResendIdle,omitempty"`
	// UpstreamBindIP is the local address used for backend sockets. Defaults to ListenIP when it is a
	// specific address, otherwise to the OS default. Geyser whitelists proxies by this address.
	UpstreamBindIP string `json:"upstreamBindIP,omitempty"`
	// MaxUDPFlows caps concurrent UDP flows per listener. Defaults to 10000.
	MaxUDPFlows int `json:"maxUDPFlows,omitempty"`
	// TCPDialTimeout bounds the connection attempt to the backend. Defaults to 10s.
	TCPDialTimeout Duration `json:"tcpDialTimeout,omitempty"`
}

// ReverseProxyConfig describes one HTTPS virtual host served by the L7 reverse proxy.
type ReverseProxyConfig struct {
	ListenIP   string `json:"listenIP,omitempty"`
	ListenPort int    `json:"listenPort"`
	CertFile   string `json:"certFile"`
	KeyFile    string `json:"keyFile"`
	// ServerHost is the backend as host:port (IP or domain). The TLS certificate's domain is matched against the SNI.
	ServerHost string `json:"serverHost"`
	// ServerProto is "http" or "https".
	ServerProto string `json:"serverProto"`
	// Domain is the virtual host to serve; defaults to the first DNS name / common name of the certificate.
	Domain string `json:"domain,omitempty"`
	// PreserveHost keeps the client's Host header instead of rewriting it to ServerHost.
	PreserveHost bool `json:"preserveHost,omitempty"`
	// InsecureSkipVerify disables certificate verification towards an https backend.
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
}

// Config is the root of config.json.
type Config struct {
	DebugMode      bool                 `json:"debugMode"`
	Proxies        []ProxyConfig        `json:"proxies"`
	ReverseProxies []ReverseProxyConfig `json:"reverseProxies"`
}

const (
	DefaultIdleTimeout            = 60 * time.Second
	DefaultRakNetHeaderResendIdle = 10 * time.Second
	DefaultTCPDialTimeout         = 10 * time.Second
	DefaultMaxUDPFlows            = 10000
)

// EffectiveProtocol resolves Protocol, falling back to the legacy ProtoTCP flag.
func (p ProxyConfig) EffectiveProtocol() Protocol {
	if p.Protocol != "" {
		return Protocol(strings.ToLower(string(p.Protocol)))
	}
	if p.ProtoTCP != nil && *p.ProtoTCP {
		return ProtocolTCP
	}
	return ProtocolUDP
}

// UseProxyProtocol reports whether PROXY v2 headers are sent (default true).
func (p ProxyConfig) UseProxyProtocol() bool {
	return p.ProxyProtocol == nil || *p.ProxyProtocol
}

// EffectiveUDPMode returns the UDP header mode (default auto).
func (p ProxyConfig) EffectiveUDPMode() UDPMode {
	if p.UDPMode == "" {
		return UDPModeAuto
	}
	return UDPMode(strings.ToLower(string(p.UDPMode)))
}

// PongRewriteEnabled reports whether RakNet pong ports are rewritten (default true).
func (p ProxyConfig) PongRewriteEnabled() bool {
	return p.RewritePongPort == nil || *p.RewritePongPort
}

// IdleTimeoutOrDefault returns IdleTimeout or the default.
func (p ProxyConfig) IdleTimeoutOrDefault() time.Duration {
	if p.IdleTimeout <= 0 {
		return DefaultIdleTimeout
	}
	return time.Duration(p.IdleTimeout)
}

// HeaderResendIdleOrDefault returns RakNetHeaderResendIdle or the default.
func (p ProxyConfig) HeaderResendIdleOrDefault() time.Duration {
	if p.RakNetHeaderResendIdle <= 0 {
		return DefaultRakNetHeaderResendIdle
	}
	return time.Duration(p.RakNetHeaderResendIdle)
}

// DialTimeoutOrDefault returns TCPDialTimeout or the default.
func (p ProxyConfig) DialTimeoutOrDefault() time.Duration {
	if p.TCPDialTimeout <= 0 {
		return DefaultTCPDialTimeout
	}
	return time.Duration(p.TCPDialTimeout)
}

// MaxUDPFlowsOrDefault returns MaxUDPFlows or the default.
func (p ProxyConfig) MaxUDPFlowsOrDefault() int {
	if p.MaxUDPFlows <= 0 {
		return DefaultMaxUDPFlows
	}
	return p.MaxUDPFlows
}

// Label returns a human-readable name for logs.
func (p ProxyConfig) Label() string {
	if p.Name != "" {
		return p.Name
	}
	return fmt.Sprintf("%s:%d", p.EffectiveProtocol(), p.ListenPort)
}

// ListenAddr returns the IP to bind, defaulting to 0.0.0.0.
func (p ProxyConfig) ListenAddr() net.IP {
	if strings.TrimSpace(p.ListenIP) == "" {
		return net.IPv4zero
	}
	return net.ParseIP(strings.TrimSpace(p.ListenIP))
}

// UpstreamBind returns the local IP for backend sockets, or nil for the OS default.
func (p ProxyConfig) UpstreamBind() net.IP {
	if s := strings.TrimSpace(p.UpstreamBindIP); s != "" {
		return net.ParseIP(s)
	}
	ip := p.ListenAddr()
	if ip == nil || ip.IsUnspecified() {
		return nil
	}
	return ip
}

// Validate checks a proxy entry and normalises simple fields in place.
func (p *ProxyConfig) Validate() error {
	p.ListenIP = strings.TrimSpace(p.ListenIP)
	if p.ListenIP == "" {
		p.ListenIP = "0.0.0.0"
	}
	if p.ListenAddr() == nil {
		return fmt.Errorf("invalid listenIP %q", p.ListenIP)
	}
	if p.UpstreamBindIP != "" && net.ParseIP(strings.TrimSpace(p.UpstreamBindIP)) == nil {
		return fmt.Errorf("invalid upstreamBindIP %q", p.UpstreamBindIP)
	}
	if p.ListenPort <= 0 || p.ListenPort > 65535 {
		return fmt.Errorf("invalid listenPort %d", p.ListenPort)
	}
	if p.ServerPort <= 0 || p.ServerPort > 65535 {
		return fmt.Errorf("invalid serverPort %d", p.ServerPort)
	}
	p.ServerHost = strings.TrimSpace(p.ServerHost)
	if p.ServerHost == "" {
		return errors.New("serverHost is empty")
	}
	switch p.EffectiveProtocol() {
	case ProtocolTCP, ProtocolUDP, ProtocolBedrock:
	default:
		return fmt.Errorf("invalid protocol %q (want tcp, udp or bedrock)", p.Protocol)
	}
	switch p.EffectiveUDPMode() {
	case UDPModeAuto, UDPModeRakNet, UDPModeNetherNet, UDPModePlain:
	default:
		return fmt.Errorf("invalid udpMode %q (want auto, raknet, nethernet or plain)", p.UDPMode)
	}
	return nil
}

// Validate checks a reverse proxy entry.
func (r *ReverseProxyConfig) Validate() error {
	if r.ListenPort <= 0 || r.ListenPort > 65535 {
		return fmt.Errorf("invalid listenPort %d", r.ListenPort)
	}
	r.ServerProto = strings.ToLower(strings.TrimSpace(r.ServerProto))
	if r.ServerProto == "" {
		r.ServerProto = "http"
	}
	if r.ServerProto != "http" && r.ServerProto != "https" {
		return fmt.Errorf("invalid serverProto %q", r.ServerProto)
	}
	r.ServerHost = strings.TrimSpace(r.ServerHost)
	if r.ServerHost == "" {
		return errors.New("serverHost is empty")
	}
	if r.CertFile == "" || r.KeyFile == "" {
		return errors.New("certFile and keyFile are required")
	}
	return nil
}

// Validate validates every entry and returns the first problem found.
func (c *Config) Validate() error {
	for i := range c.Proxies {
		if err := c.Proxies[i].Validate(); err != nil {
			return fmt.Errorf("proxies[%d]: %w", i, err)
		}
	}
	for i := range c.ReverseProxies {
		if err := c.ReverseProxies[i].Validate(); err != nil {
			return fmt.Errorf("reverseProxies[%d]: %w", i, err)
		}
	}
	if len(c.Proxies) == 0 && len(c.ReverseProxies) == 0 {
		return errors.New("config has no proxies")
	}
	return nil
}

// LoadConfig reads path. When the file does not exist an interactive wizard
// creates it. The returned config is validated.
func LoadConfig(path string) (*Config, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		cfg, err := runWizard(os.Stdin, os.Stdout)
		if err != nil {
			return nil, err
		}
		if err := SaveConfig(path, cfg); err != nil {
			return nil, err
		}
		return cfg, cfg.Validate()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

// SaveConfig writes cfg to path as indented JSON.
func SaveConfig(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

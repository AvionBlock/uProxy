package proxies

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"uproxy/config"
	"uproxy/core"
)

var logger = core.Log

type proxyEntry struct {
	cert   tls.Certificate
	proxy  *httputil.ReverseProxy
	target string
}

// ReverseProxy is an SNI-routed HTTPS reverse proxy: one listener per port,
// many domains per listener, each with its own certificate and backend.
//
// It is handy for serving NetherNet signaling over HTTPS on 443 behind a real
// certificate (the Bedrock client also tries https://host when the port is
// 19132 or omitted). The client address is forwarded as X-Forwarded-For /
// X-Real-IP; Geyser reads it when uProxy is listed in "signaling-trusted-proxies".
type ReverseProxy struct {
	configs []config.ReverseProxyConfig

	mu      sync.RWMutex
	domains map[string]*proxyEntry
}

// NewReverseProxy prepares the reverse proxy. Call Run to start it.
func NewReverseProxy(configs []config.ReverseProxyConfig) *ReverseProxy {
	return &ReverseProxy{configs: configs, domains: map[string]*proxyEntry{}}
}

// StartHTTPSReverseProxy runs the reverse proxy until ctx is cancelled. Kept for compatibility.
func StartHTTPSReverseProxy(ctx context.Context, configs []config.ReverseProxyConfig) error {
	return NewReverseProxy(configs).Run(ctx)
}

// Run registers every domain and serves all listen ports until ctx is cancelled.
func (r *ReverseProxy) Run(ctx context.Context) error {
	if len(r.configs) == 0 {
		return nil
	}

	ports := map[string][]config.ReverseProxyConfig{}
	for _, cfg := range r.configs {
		if err := r.register(cfg); err != nil {
			return err
		}
		addr := net.JoinHostPort(listenIPOf(cfg.ListenIP), strconv.Itoa(cfg.ListenPort))
		ports[addr] = append(ports[addr], cfg)
	}

	servers := make([]*http.Server, 0, len(ports))
	errCh := make(chan error, len(ports))
	for addr := range ports {
		server := &http.Server{
			Addr:              addr,
			Handler:           http.HandlerFunc(r.handleRequest),
			ReadHeaderTimeout: 15 * time.Second,
			IdleTimeout:       120 * time.Second,
			TLSConfig: &tls.Config{
				GetCertificate: r.getCertificate,
				MinVersion:     tls.VersionTLS12,
			},
		}
		servers = append(servers, server)
		go func(server *http.Server) {
			logger.Info("[reverse] HTTPS reverse proxy listening on %s", server.Addr)
			err := server.ListenAndServeTLS("", "")
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			errCh <- err
		}(server)
	}

	var firstErr error
	for range servers {
		select {
		case err := <-errCh:
			if err != nil && firstErr == nil {
				firstErr = err
				logger.Error("[reverse] %v", err)
			}
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			for _, s := range servers {
				_ = s.Shutdown(shutdownCtx)
			}
			cancel()
			return firstErr
		}
	}
	return firstErr
}

func listenIPOf(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return "0.0.0.0"
	}
	return ip
}

func (r *ReverseProxy) register(cfg config.ReverseProxyConfig) error {
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return fmt.Errorf("reverse %s: %w", cfg.ServerHost, err)
	}

	domain := strings.ToLower(strings.TrimSpace(cfg.Domain))
	if domain == "" {
		domain = domainFromCert(cert)
	}
	if domain == "" {
		// Legacy behaviour: the backend host doubled as the served domain.
		domain = strings.ToLower(strings.TrimSpace(cfg.ServerHost))
		if h, _, err := net.SplitHostPort(domain); err == nil {
			domain = h
		}
	}

	target := &url.URL{Scheme: cfg.ServerProto, Host: cfg.ServerHost}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.ServerProto == "https" && cfg.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit opt-in
	}

	rp := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
			if ip, _, err := net.SplitHostPort(pr.In.RemoteAddr); err == nil {
				pr.Out.Header.Set("X-Real-IP", ip)
			}
			if cfg.PreserveHost {
				pr.Out.Host = pr.In.Host
			}
		},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			logger.Warn("[reverse] %s %s%s: backend error: %v", req.RemoteAddr, req.Host, req.URL.Path, err)
			http.Error(w, "Bad gateway", http.StatusBadGateway)
		},
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.domains[domain]; dup {
		return fmt.Errorf("reverse: domain %s configured twice", domain)
	}
	r.domains[domain] = &proxyEntry{cert: cert, proxy: rp, target: target.String()}
	logger.Info("[reverse] Registered domain %s -> %s", domain, target)
	return nil
}

func domainFromCert(cert tls.Certificate) string {
	if len(cert.Certificate) == 0 {
		return ""
	}
	leaf := cert.Leaf
	if leaf == nil {
		parsed, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return ""
		}
		leaf = parsed
	}
	for _, name := range leaf.DNSNames {
		if !strings.HasPrefix(name, "*.") {
			return strings.ToLower(name)
		}
	}
	if len(leaf.DNSNames) > 0 {
		return strings.ToLower(leaf.DNSNames[0])
	}
	return strings.ToLower(leaf.Subject.CommonName)
}

func (r *ReverseProxy) lookup(host string) *proxyEntry {
	host = strings.ToLower(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if e, ok := r.domains[host]; ok {
		return e
	}
	// Wildcard certificates: try the parent domain.
	if i := strings.IndexByte(host, '.'); i > 0 {
		if e, ok := r.domains["*"+host[i:]]; ok {
			return e
		}
	}
	return nil
}

func (r *ReverseProxy) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if e := r.lookup(hello.ServerName); e != nil {
		return &e.cert, nil
	}
	logger.Debug("[reverse] no certificate for SNI %q from %s", hello.ServerName, hello.Conn.RemoteAddr())
	return nil, errors.New("no certificate for domain")
}

func (r *ReverseProxy) handleRequest(w http.ResponseWriter, req *http.Request) {
	e := r.lookup(req.Host)
	if e == nil {
		http.Error(w, "Unknown host", http.StatusNotFound)
		return
	}
	e.proxy.ServeHTTP(w, req)
}

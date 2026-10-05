package proxies

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"uproxy/config"
	"uproxy/core"
)

var logger = core.NewLogger()

type proxyEntry struct {
	cert  tls.Certificate
	proxy *httputil.ReverseProxy
}

// StartHTTPSReverseProxy groups virtual hosts by listening port. The routing
// table and certificates are immutable after startup, so requests can use them
// concurrently without a global lock.
func StartHTTPSReverseProxy(configs []config.ReverseProxyConfig) error {
	if len(configs) == 0 {
		return nil
	}
	byPort := make(map[int]map[string]*proxyEntry)
	for _, cfg := range configs {
		host, entry, err := newProxyEntry(cfg)
		if err != nil {
			return err
		}
		if byPort[cfg.ListenPort] == nil {
			byPort[cfg.ListenPort] = make(map[string]*proxyEntry)
		}
		if byPort[cfg.ListenPort][host] != nil {
			return fmt.Errorf("duplicate HTTPS host %s on port %d", host, cfg.ListenPort)
		}
		byPort[cfg.ListenPort][host] = entry
	}

	servers := make([]*http.Server, 0, len(byPort))
	listeners := make([]net.Listener, 0, len(byPort))
	for port, entries := range byPort {
		listener, err := net.Listen("tcp", ":"+strconv.Itoa(port))
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return fmt.Errorf("bind HTTPS port %d: %w", port, err)
		}
		tlsConfig := &tls.Config{
			MinVersion: tls.VersionTLS12,
			GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
				entry := entries[canonicalHost(hello.ServerName)]
				if entry == nil {
					return nil, fmt.Errorf("no certificate for SNI %q", hello.ServerName)
				}
				return &entry.cert, nil
			},
		}
		server := &http.Server{
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       60 * time.Second,
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				entry := entries[canonicalHost(r.Host)]
				if entry == nil {
					http.Error(w, "Unknown host", http.StatusNotFound)
					return
				}
				entry.proxy.ServeHTTP(w, r)
			}),
		}
		servers = append(servers, server)
		listeners = append(listeners, tls.NewListener(listener, tlsConfig))
		logger.Info("HTTPS reverse proxy listening on :%d", port)
	}

	errCh := make(chan error, len(servers))
	for i, server := range servers {
		listener := listeners[i]
		go func() { errCh <- server.Serve(listener) }()
	}
	err := <-errCh
	for _, server := range servers {
		_ = server.Close()
	}
	return fmt.Errorf("HTTPS reverse proxy stopped: %w", err)
}

func newProxyEntry(cfg config.ReverseProxyConfig) (string, *proxyEntry, error) {
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return "", nil, fmt.Errorf("load certificate: %w", err)
	}
	target, err := url.Parse(cfg.ServerProto + "://" + cfg.ServerHost)
	if err != nil || target.Hostname() == "" {
		return "", nil, fmt.Errorf("invalid HTTPS backend %q", cfg.ServerHost)
	}
	host := canonicalHost(cfg.Domain)
	if host == "" {
		// Existing configs without domain keep routing by their backend host.
		host = canonicalHost(target.Hostname())
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(req *httputil.ProxyRequest) {
			req.SetURL(target)
			req.SetXForwarded()
			if ip, _, err := net.SplitHostPort(req.In.RemoteAddr); err == nil {
				req.Out.Header.Set("X-Real-IP", ip)
			}
		},
	}
	logger.Info("Registered HTTPS domain: %s -> %s", host, target)
	return host, &proxyEntry{cert: cert, proxy: proxy}, nil
}

func canonicalHost(host string) string {
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

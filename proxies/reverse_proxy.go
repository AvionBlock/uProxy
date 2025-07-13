package proxies

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"uproxy/config"
	"uproxy/core"
)

var logger = core.NewLogger()

type proxyEntry struct {
	cert      tls.Certificate
	proxy     *httputil.ReverseProxy
	targetURL string
}

var (
	proxyMap   = map[string]*proxyEntry{}
	proxyMutex sync.RWMutex
)

// StartHTTPSReverseProxy starts an HTTPS reverse proxy servers.
// It listens on cfg.ListenPorts and forwards requests to the target servers
// defined by cfg.ServerProto and cfg.ServerHost.
// The proxy sets X-Real-IP and X-Forwarded-For headers based on the client IP.
func StartHTTPSReverseProxy(configs []config.ReverseProxyConfig) error {
	portMap := make(map[int][]config.ReverseProxyConfig)

	for _, cfg := range configs {
		portMap[cfg.ListenPort] = append(portMap[cfg.ListenPort], cfg)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(portMap))

	for port, cfgs := range portMap {
		wg.Add(1)

		go func(port int, cfgs []config.ReverseProxyConfig) {
			defer wg.Done()

			for _, cfg := range cfgs {
				if err := registerProxy(cfg); err != nil {
					errCh <- err
					return
				}
			}

			server := &http.Server{
				Addr:    ":" + strconv.Itoa(port),
				Handler: http.HandlerFunc(handleRequest),
				TLSConfig: &tls.Config{
					GetCertificate: getCertificate,
					MinVersion:     tls.VersionTLS12,
				},
			}

			logger.Info("HTTPS reverse proxy listening on :%d", port)
			if err := server.ListenAndServeTLS("", ""); err != nil {
				errCh <- err
			}
		}(port, cfgs)
	}

	wg.Wait()

	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

func registerProxy(cfg config.ReverseProxyConfig) error {
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return err
	}

	host := strings.ToLower(strings.TrimSpace(cfg.ServerHost))

	target := cfg.ServerProto + "://" + cfg.ServerHost
	reverseProxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = cfg.ServerProto
			req.URL.Host = cfg.ServerHost
			req.Host = req.URL.Host

			if ip, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
				req.Header.Set("X-Real-IP", ip)
				xff := req.Header.Get("X-Forwarded-For")
				if xff == "" {
					req.Header.Set("X-Forwarded-For", ip)
				} else {
					req.Header.Set("X-Forwarded-For", xff+", "+ip)
				}
			}
		},
	}

	proxyMutex.Lock()
	defer proxyMutex.Unlock()
	proxyMap[host] = &proxyEntry{cert: cert, proxy: reverseProxy, targetURL: target}
	logger.Info("Registered domain: %s -> %s", host, target)
	return nil
}

func getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	proxyMutex.RLock()
	defer proxyMutex.RUnlock()

	host := strings.ToLower(hello.ServerName)
	if proxy, ok := proxyMap[host]; ok {
		return &proxy.cert, nil
	}

	logger.Error("No certificate found for SNI: %s", host)
	return nil, errors.New("no certificate for domain")
}

func handleRequest(w http.ResponseWriter, r *http.Request) {
	host := strings.ToLower(r.Host)

	proxyMutex.RLock()
	entry, ok := proxyMap[host]
	proxyMutex.RUnlock()

	if !ok {
		http.Error(w, "Unknown host", http.StatusNotFound)
		return
	}

	entry.proxy.ServeHTTP(w, r)
}

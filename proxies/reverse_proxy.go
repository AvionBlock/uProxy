package proxies

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"uproxy/config"
	"uproxy/core"
)

var logger = core.NewLogger()

// StartHTTPSReverseProxy starts an HTTPS reverse proxy server.
// It listens on cfg.ListenPort and forwards requests to the target server
// defined by cfg.ServerProto and cfg.ServerHost.
// The proxy sets X-Real-IP and X-Forwarded-For headers based on the client IP.
func StartHTTPSReverseProxy(cfg config.ReverseProxyConfig) {
	target := cfg.ServerProto + "://" + cfg.ServerHost

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			// Update the request URL scheme and host to target server
			req.URL.Scheme = cfg.ServerProto
			req.URL.Host = cfg.ServerHost
			req.Host = req.URL.Host // set Host header for backend

			// Set X-Real-IP and X-Forwarded-For headers with client IP
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

	server := &http.Server{
		Addr:    ":" + strconv.Itoa(cfg.ListenPort),
		Handler: proxy,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}

	logger.Info("HTTPS Reverse Proxy listening on :%d, forwarding to %s", cfg.ListenPort, target)

	// Start HTTPS server with given cert and key files
	err := server.ListenAndServeTLS(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		logger.Error("Failed to start HTTPS reverse proxy: %v", err)
	}
}

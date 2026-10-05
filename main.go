package main

import (
	"os"
	"uproxy/config"
	"uproxy/core"
	"uproxy/proxies"
)

var logger = core.NewLogger()

func main() {
	cfg, err := config.LoadConfig("config.json")
	if err != nil {
		logger.Error("Failed to load config: %v", err)
		os.Exit(1)
	}

	errCh := make(chan error, len(cfg.Proxies)+1)
	for _, proxy := range cfg.Proxies {
		p := proxy
		if proxy.ProtoTCP {
			go func() { errCh <- proxies.StartTCPProxy(cfg, p) }()
		} else {
			go func() { errCh <- proxies.StartUDPProxy(cfg, p) }()
		}
	}

	if len(cfg.ReverseProxies) > 0 {
		go func() { errCh <- proxies.StartHTTPSReverseProxy(cfg.ReverseProxies) }()
	}
	if len(cfg.Proxies) == 0 && len(cfg.ReverseProxies) == 0 {
		logger.Error("No proxies configured")
		os.Exit(1)
	}

	logger.Error("Proxy stopped: %v", <-errCh)
	os.Exit(1)
}

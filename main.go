package main

import (
	"uproxy/config"
	"uproxy/core"
	"uproxy/proxies"
)

var logger = core.NewLogger()

func main() {
	config, err := config.LoadConfig("config.json")
	if err != nil {
		logger.Error("Failed to load config: %v", err)
		return
	}

	// Load L4 Proxy
	for _, proxy := range config.Proxies {
		p := proxy
		if proxy.ProtoTCP {
			go proxies.StartTCPProxy(config, p)
		} else {
			go proxies.StartUDPProxy(config, p)
		}
	}

	// Load Reverse L7
	for _, rp := range config.ReverseProxies {
		go proxies.StartHTTPSReverseProxy(rp)
	}

	select {}
}

package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"uproxy/config"
	"uproxy/core"
	"uproxy/proxies"
)

// Version is overridden at build time with -ldflags "-X main.Version=...".
var Version = "dev"

var logger = core.Log

func main() {
	configPath := flag.String("config", envOr("UPROXY_CONFIG", "config.json"), "path to config.json")
	debug := flag.Bool("debug", false, "force debug logging")
	flag.Parse()

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		logger.Error("Failed to load config: %v", err)
		os.Exit(1)
	}
	logger.SetDebug(cfg.DebugMode || *debug)
	logger.Info("uProxy %s starting (%d L4 proxies, %d reverse hosts)", Version, len(cfg.Proxies), len(cfg.ReverseProxies))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	for _, p := range cfg.Proxies {
		wg.Add(1)
		go func(p config.ProxyConfig) {
			defer wg.Done()
			if err := proxies.StartProxy(ctx, p); err != nil {
				logger.Error("[%s] %v", p.Label(), err)
			}
		}(p)
	}

	if len(cfg.ReverseProxies) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := proxies.StartHTTPSReverseProxy(ctx, cfg.ReverseProxies); err != nil {
				logger.Error("[reverse] %v", err)
			}
		}()
	}

	wg.Wait()
	if ctx.Err() != nil {
		logger.Info("Shutting down")
	} else {
		logger.Error("All proxies stopped")
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadLegacyConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := `{
  "debugMode": false,
  "proxies": [
    {"listenIP": "0.0.0.0", "listenPort": 25565, "serverHost": "10.0.0.2", "serverPort": 25565, "protoTCP": true},
    {"listenIP": "0.0.0.0", "listenPort": 19132, "serverHost": "10.0.0.2", "serverPort": 19132, "protoTCP": false}
  ],
  "reverseProxies": []
}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Proxies[0].EffectiveProtocol(); got != ProtocolTCP {
		t.Fatalf("proxies[0] = %s, want tcp", got)
	}
	if got := cfg.Proxies[1].EffectiveProtocol(); got != ProtocolUDP {
		t.Fatalf("proxies[1] = %s, want udp", got)
	}
	for _, p := range cfg.Proxies {
		if !p.UseProxyProtocol() || p.EffectiveUDPMode() != UDPModeAuto {
			t.Fatalf("legacy defaults broken: %+v", p)
		}
	}
}

func TestNewConfigFields(t *testing.T) {
	raw := `{
  "proxies": [{
    "name": "geyser", "listenIP": "", "listenPort": 19132, "serverHost": "geyser.local", "serverPort": 19133,
    "protocol": "bedrock", "udpMode": "nethernet", "proxyProtocol": true, "idleTimeout": "90s",
    "raknetHeaderResendIdle": 5, "rewritePongPort": false, "upstreamBindIP": "10.0.0.1"
  }]
}`
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	p := cfg.Proxies[0]
	if p.ListenIP != "0.0.0.0" {
		t.Fatalf("empty listenIP should default to 0.0.0.0, got %q", p.ListenIP)
	}
	if p.EffectiveProtocol() != ProtocolBedrock || p.EffectiveUDPMode() != UDPModeNetherNet {
		t.Fatalf("protocol/mode: %s/%s", p.EffectiveProtocol(), p.EffectiveUDPMode())
	}
	if p.IdleTimeoutOrDefault() != 90*time.Second {
		t.Fatalf("idleTimeout = %s", p.IdleTimeoutOrDefault())
	}
	if p.HeaderResendIdleOrDefault() != 5*time.Second {
		t.Fatalf("numeric duration = %s, want 5s", p.HeaderResendIdleOrDefault())
	}
	if p.PongRewriteEnabled() {
		t.Fatal("rewritePongPort=false ignored")
	}
	if p.UpstreamBind().String() != "10.0.0.1" {
		t.Fatalf("upstreamBindIP = %v", p.UpstreamBind())
	}
	if p.Label() != "geyser" {
		t.Fatalf("label = %q", p.Label())
	}

	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"idleTimeout":"1m30s"`) {
		t.Fatalf("duration not marshalled as string: %s", out)
	}
}

func TestValidateRejectsBadEntries(t *testing.T) {
	bad := []ProxyConfig{
		{ListenIP: "nope", ListenPort: 1, ServerHost: "x", ServerPort: 1},
		{ListenPort: 0, ServerHost: "x", ServerPort: 1},
		{ListenPort: 1, ServerHost: "", ServerPort: 1},
		{ListenPort: 1, ServerHost: "x", ServerPort: 70000},
		{ListenPort: 1, ServerHost: "x", ServerPort: 1, Protocol: "sctp"},
		{ListenPort: 1, ServerHost: "x", ServerPort: 1, UDPMode: "magic"},
	}
	for i, p := range bad {
		if err := p.Validate(); err == nil {
			t.Errorf("entry %d should fail validation: %+v", i, p)
		}
	}
	empty := Config{}
	if err := empty.Validate(); err == nil {
		t.Error("empty config must fail")
	}
}

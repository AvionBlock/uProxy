package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigRejectsInvalidProxyBeforeBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"proxies":[{"listenIP":"127.0.0.1","listenPort":19132,"serverHost":"127.0.0.1","serverPort":70000}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "serverPort") {
		t.Fatalf("LoadConfig error = %v, want invalid serverPort", err)
	}
}

package proxies

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uproxy/config"
)

func TestReverseProxyUsesPublicDomainAndTrustedClientAddress(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Host + "|" + r.Header.Get("X-Forwarded-For") + "|" + r.Header.Get("X-Real-IP")))
	}))
	defer backend.Close()
	certServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer certServer.Close()
	cert := certServer.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}

	host, entry, err := newProxyEntry(config.ReverseProxyConfig{
		Domain: "Public.Example", ServerHost: strings.TrimPrefix(backend.URL, "http://"),
		ServerProto: "http", CertFile: certPath, KeyFile: keyPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if host != "public.example" || canonicalHost("Public.Example:443") != host {
		t.Fatalf("wrong public host: %q", host)
	}
	request := httptest.NewRequest("GET", "https://Public.Example/test", nil)
	request.RemoteAddr = "203.0.113.7:12345"
	request.Header.Set("X-Forwarded-For", "spoofed")
	recorder := httptest.NewRecorder()
	entry.proxy.ServeHTTP(recorder, request)
	want := strings.TrimPrefix(backend.URL, "http://") + "|203.0.113.7|203.0.113.7"
	if recorder.Code != http.StatusOK || recorder.Body.String() != want {
		t.Fatalf("response = %d %q, want %q", recorder.Code, recorder.Body.String(), want)
	}
}

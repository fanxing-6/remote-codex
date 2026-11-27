package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"remote-codex/internal/auth"
	"remote-codex/internal/config"
	"remote-codex/internal/gateway"
	"remote-codex/internal/logger"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

func TestAutomaticTLSSettings(t *testing.T) {
	c := config.Default()
	c.TunnelMode, c.PublicOrigin, c.AllowedHosts = "custom", "https://chat.example.com", []string{"chat.example.com"}
	path := filepath.Join(t.TempDir(), "config.json")
	settings, err := automaticTLSConfig(c, path)
	if err != nil || settings != nil {
		t.Fatalf("disabled TLS: %v", err)
	}
	c.TLSPort = 18443
	c.TLSAutocert = true
	cacheDir := filepath.Join(filepath.Dir(path), "certs")
	if err := os.Mkdir(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	settings, err = automaticTLSConfig(c, path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(cacheDir)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0700) {
		t.Fatalf("cache permissions: %v", err)
	}
	if !slices.Contains(settings.NextProtos, acme.ALPNProto) || settings.MinVersion != tls.VersionTLS12 {
		t.Fatal("TLS protocol settings")
	}
	_, err = settings.GetCertificate(&tls.ClientHelloInfo{ServerName: "unapproved.example.test"})
	if err == nil {
		t.Fatal("unapproved SNI accepted")
	}
	c.TLSAutocert = false
	settings, err = automaticTLSConfig(c, path)
	if err != nil || settings != nil {
		t.Fatalf("autocert disabled: %v", err)
	}
}

func cacheLocalCertificate(t *testing.T, path, domain string) *x509.CertPool {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: serial, DNSNames: []string{domain}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	data := append(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), certPEM...)
	if err := autocert.DirCache(filepath.Join(filepath.Dir(path), "certs")).Put(context.Background(), domain, data); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("local certificate trust")
	}
	return roots
}

func TestTLSGatewayAndLifecycle(t *testing.T) {
	c := config.Default()
	c.TunnelMode = "custom"
	c.PublicOrigin = "https://bridge.example.test"
	c.AllowedHosts = []string{"bridge.example.test"}
	c.TLSPort, c.TLSAutocert = 18443, true
	path := filepath.Join(t.TempDir(), "config.json")
	settings, err := automaticTLSConfig(c, path)
	if err != nil {
		t.Fatal(err)
	}
	roots := cacheLocalCertificate(t, path, "bridge.example.test")
	store := auth.New(false, nil)
	defer store.RevokeAll()
	g := gateway.New(c, store, logger.New(io.Discard), 0)
	endpoint, err := newTLSEndpoint(0, settings, g, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.shutdown(context.Background())
	address := endpoint.listener.Addr().String()
	if endpoint.listener.Addr().(*net.TCPAddr).IP.String() != "127.0.0.1" {
		t.Fatal("TLS listener address")
	}
	if _, err := newTLSEndpoint(endpoint.listener.Addr().(*net.TCPAddr).Port, settings, g, nil); err == nil {
		t.Fatal("occupied TLS port accepted")
	}
	done := make(chan error, 1)
	go func() { done <- endpoint.server.ServeTLS(endpoint.listener, "", "") }()
	for _, http2 := range []bool{false, true} {
		transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "bridge.example.test"}, ForceAttemptHTTP2: http2}
		client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		for _, item := range []struct {
			path   string
			status int
		}{{"/livez", 200}, {"/", 302}, {"/auth/login", 200}, {"/healthz", 401}, {"/admin", 401}} {
			request, err := http.NewRequest("GET", "https://"+address+item.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Host = "bridge.example.test"
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != item.status {
				t.Fatalf("%s: %d", item.path, response.StatusCode)
			}
			if http2 && response.ProtoMajor != 2 {
				t.Fatal("HTTP/2 unavailable")
			}
			if item.path == "/livez" && string(body) != "{\"ok\":true}\n" {
				t.Fatal("live response")
			}
			if item.path == "/" && response.Header.Get("Location") != "/auth/login" {
				t.Fatal("login redirect")
			}
		}
		transport.CloseIdleConnections()
	}
	dialer := websocket.Dialer{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "bridge.example.test"}, HandshakeTimeout: 5 * time.Second}
	_, response, err := dialer.Dial("wss://"+address+"/bridge", http.Header{"Host": {"bridge.example.test"}, "Origin": {c.PublicOrigin}})
	if err == nil || response == nil || response.StatusCode != 401 {
		t.Fatalf("WebSocket authentication: %v, %v", response, err)
	}
	response.Body.Close()
	// 相同端口更新 SNI 配置后，新连接使用更新后的证书策略。
	c.PublicOrigin, c.AllowedHosts = "https://changed.example.test", []string{"changed.example.test"}
	updated, err := automaticTLSConfig(c, path)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.settings.Store(updated)
	connection, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", address, &tls.Config{RootCAs: roots, ServerName: "bridge.example.test"})
	if err == nil {
		connection.Close()
		t.Fatal("previous SNI accepted after update")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := endpoint.shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal("TLS port was not released", err)
	}
	listener.Close()
}

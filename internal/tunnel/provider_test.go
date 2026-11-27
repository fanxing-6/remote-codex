package tunnel

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"remote-codex/internal/config"
)

func TestCustomProviderLifecycle(t *testing.T) {
	c := config.Default()
	c.TunnelMode = "custom"
	c.PublicOrigin = "https://chat.example.com"
	p := NewProvider(c, t.TempDir(), "")
	if p.Mode() != "custom" || p.Status().State != "disabled" {
		t.Fatal("initial state")
	}
	origin, err := p.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bound := origin.Bind(c)
	if err = config.Validate(bound); err != nil {
		t.Fatal(err)
	}
	if !p.DoctorCheck().OK || p.Status().State != "running" {
		t.Fatal("running state")
	}
	if settings, err := p.TLSConfig(t.TempDir()); err != nil || settings != nil {
		t.Fatal("external proxy TLS", err)
	}
	if _, err = p.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 100 {
				p.Status()
				p.DoctorCheck()
			}
		}()
	}
	p.Stop()
	readers.Wait()
	if p.Status().State != "disabled" {
		t.Fatal("stop state")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = p.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCustomAutocert(t *testing.T) {
	p := &CustomProvider{PublicOrigin: "https://chat.example.com", Autocert: true}
	directory := t.TempDir()
	settings, err := p.TLSConfig(directory)
	if err != nil || settings == nil || settings.GetCertificate == nil {
		t.Fatal("autocert unavailable", err)
	}
	p.PublicOrigin = "http://127.0.0.1:19865"
	if _, err = p.TLSConfig(directory); err == nil {
		t.Fatal("insecure autocert accepted")
	}
}

func TestQuickProviderInputFailure(t *testing.T) {
	p := &QuickProvider{Port: 80, RuntimeDir: t.TempDir()}
	if _, err := p.Start(context.Background()); err == nil {
		t.Fatal("invalid port accepted")
	}
	if p.Status().State != "error" || p.Status().PID != 0 {
		t.Fatal("failed startup state")
	}
	p.Stop()
	p.Stop()
	if p.Status().State != "disabled" {
		t.Fatal("idempotent stop")
	}
}

func TestQuickProviderProcessIntegration(t *testing.T) {
	if os.Getenv("REMOTE_CODEX_NETWORK_TESTS") != "1" {
		t.Skip("enable real Cloudflare network integration")
	}
	path, err := FindCloudflared()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("REMOTE_CODEX_CLOUDFLARED", path)
	if found, err := FindCloudflared(); err != nil || found != path {
		t.Fatal("environment resolution", err)
	}
	p := &QuickProvider{Port: 19865, RuntimeDir: t.TempDir(), Protocol: "http2", CloudflaredPath: path}
	t.Cleanup(p.Stop)
	origin, err := p.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ValidQuickOrigin(origin.URL) || p.Status().PID <= 0 {
		t.Fatal("live provider status")
	}
	pid := p.Status().PID
	if _, err = p.Start(context.Background()); err != nil || p.Status().PID != pid {
		t.Fatal("start not idempotent", err)
	}
	if _, err = p.Restart(context.Background()); err != nil || p.Status().PID == pid {
		t.Fatal("restart did not replace process", err)
	}
	p.Stop()
	if p.Status().PID != 0 {
		t.Fatal("process not released")
	}
}

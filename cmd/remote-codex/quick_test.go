package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"remote-codex/internal/auth"
	"remote-codex/internal/config"
	"remote-codex/internal/gateway"
	"remote-codex/internal/logger"
)

func TestFirstRunOwnerPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c, err := ensureOwner(config.Default(), path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Auth.Password == nil || c.Auth.Password.Algo != "scrypt-v1" || len(c.Auth.Password.Hash) != 64 || len(c.Auth.Password.Salt) != 32 {
		t.Fatal("owner password not generated")
	}
	if c.AllowInsecureLocalhost || c.PublicOrigin != "" || len(c.AllowedHosts) != 0 {
		t.Fatal("unbound quick tunnel exposed an origin")
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(loaded.Auth.Password)
	loaded, err = ensureOwner(loaded, path)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(loaded.Auth.Password)
	if !bytes.Equal(before, after) {
		t.Fatal("existing owner password replaced")
	}
	info, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatal("configuration permissions", err)
	}
	other, err := ensureOwner(config.Default(), filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if *other.Auth.Password == *loaded.Auth.Password {
		t.Fatal("generated passwords reused")
	}
}

func TestQuickGatewayBindingAndCustomSwitch(t *testing.T) {
	c, err := ensureOwner(config.Default(), filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := auth.New(false, c.Auth.Password)
	defer s.Close()
	a := &application{config: c, publicAuth: s, log: logger.New(io.Discard)}
	a.gateway.Store(gateway.New(c, s, a.log, 0))
	request := func(method, host, origin string) int {
		r := httptest.NewRequest(method, "https://"+host+"/livez", nil)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		a.gateway.Load().ServeHTTP(w, r)
		return w.Code
	}
	if request("GET", "pending.trycloudflare.com", "") != 403 {
		t.Fatal("unbound gateway accepted a host")
	}
	for _, mode := range []string{"quick", "custom", "quick"} {
		c.TunnelMode = mode
		c.PublicOrigin = "https://assigned.trycloudflare.com"
		c.AllowedHosts = []string{"assigned.trycloudflare.com"}
		if mode == "custom" {
			c.PublicOrigin = "https://chat.example.com"
			c.AllowedHosts = []string{"chat.example.com"}
		}
		if err := config.Validate(c); err != nil {
			t.Fatal(err)
		}
		next, err := a.prepareGateway(c)
		if err != nil {
			t.Fatal(err)
		}
		a.publish(c, next)
		if request("GET", c.AllowedHosts[0], "") != 200 || request("GET", "evil.example", "") != 403 || request("POST", c.AllowedHosts[0], "https://evil.example") != 403 {
			t.Fatal("host or origin boundary")
		}
		if !s.Cookie(auth.Random(), 3600).Secure {
			t.Fatal("secure cookie disabled")
		}
	}
}

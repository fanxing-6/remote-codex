package gateway

import (
	"context"
	"io"
	"net/http/httptest"
	"os"
	"remote-codex/internal/auth"
	"remote-codex/internal/logger"
	"strings"
	"testing"
	"time"
)

func TestDILAuthenticationAndPathBoundary(t *testing.T) {
	g := New(publicConfig(), auth.New(false, nil), logger.New(io.Discard), 0)
	t.Cleanup(g.Auth.Close)
	session := g.Auth.CookieSession(time.Now().Add(time.Hour))
	base := g.dilBasePath(session)
	if base != g.dilBasePath(session) {
		t.Fatal("capability must remain stable within one session")
	}
	for _, path := range []string{
		base + "https://example.com/runner.html", base + "//example.com/a.js", base + "assets/dil/../private.js",
		base + "assets/dil/a.js?url=https://example.com", base + "assets/dil/a.js%2fsecret",
		base + "deployments/dil/v16/runner.html", base + "assets/dil/a.map", base + "assets/dil/a.js/extra",
		base + "assets/dil/a.js\\secret", base + "auth/logout",
	} {
		r := httptest.NewRequest("GET", g.Config.PublicOrigin+path, nil)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(path, w.Code)
		}
	}
	r := httptest.NewRequest("GET", g.Config.PublicOrigin+dilPrefix+auth.Random()+"/deployments/dil/v15/runner.html", nil)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	if err := g.Auth.Revoke(session.ID); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("GET", g.Config.PublicOrigin+base+"deployments/dil/v15/runner.html", nil)
	w = httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("revoked session retained asset access", w.Code)
	}
}

func TestDILLiveOfficialAssets(t *testing.T) {
	if os.Getenv("REMOTE_CODEX_DIL_TEST") != "1" {
		t.Skip("requires access to the official CDN")
	}
	g := New(publicConfig(), auth.New(false, nil), logger.New(io.Discard), 0)
	t.Cleanup(g.Auth.Close)
	session := g.Auth.CookieSession(time.Now().Add(time.Hour))
	base := g.dilBasePath(session)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	asset, err := g.dil.fetch(ctx, "deployments/dil/v15/runner.html")
	if err != nil {
		t.Fatal(err)
	}
	output, err := rewriteDILRunner(asset.body, base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), base+"assets/dil/") || !strings.Contains(string(output), "sha256-") {
		t.Fatal("runner must preserve its script policy and use authenticated module URLs")
	}
	r := httptest.NewRequest("GET", g.Config.PublicOrigin+base+"deployments/dil/v15/runner.html", nil)
	r.Header.Set("Origin", "null")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Set-Cookie") != "" || w.Header().Get("X-Frame-Options") != "" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") {
		t.Fatal(w.Code, w.Header())
	}
}

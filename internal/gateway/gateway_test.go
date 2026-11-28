package gateway

import (
	"bytes"
	"io"
	"net/http/httptest"
	"remote-codex/internal/auth"
	"remote-codex/internal/config"
	"remote-codex/internal/embedassets"
	"remote-codex/internal/logger"
	"remote-codex/internal/protocol"
	"strings"
	"testing"
	"time"
)

func publicConfig() config.Config {
	c := config.Default()
	c.TunnelMode = "custom"
	c.PublicOrigin = "https://chat.example.com"
	c.AllowedHosts = []string{"chat.example.com"}
	return c
}
func TestPublicHostOriginAndLoginCSRF(t *testing.T) {
	c := publicConfig()
	g := New(c, auth.New(false, nil), logger.New(io.Discard), 0)
	defer g.Auth.RevokeAll()
	for _, item := range []struct {
		method, path, host, origin string
		code                       int
	}{{"GET", "/livez", "chat.example.com", "", 200}, {"GET", "/healthz", "chat.example.com", "", 401}, {"GET", "/", "evil.example", "", 403}, {"POST", "/auth/login", "chat.example.com", "https://sibling.example.com", 403}, {"POST", "/auth/login", "chat.example.com", c.PublicOrigin, 403}, {"POST", "/auth/login", "chat.example.com", "null", 403}} {
		request := httptest.NewRequest(item.method, "https://"+item.host+item.path, nil)
		request.Header.Set("Origin", item.origin)
		recorder := httptest.NewRecorder()
		g.ServeHTTP(recorder, request)
		if recorder.Code != item.code {
			t.Fatal(item.path, recorder.Code)
		}
	}
	request := httptest.NewRequest("GET", c.PublicOrigin+"/bridge", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	recorder := httptest.NewRecorder()
	g.ServeHTTP(recorder, request)
	if recorder.Code != 403 {
		t.Fatal("missing WS origin accepted")
	}
}
func TestOpaqueBinaryQueueBound(t *testing.T) {
	c := config.Default()
	g := New(c, auth.New(false, nil), logger.New(io.Discard), 0)
	item := &claim{output: make(chan []byte, 128)}
	g.active = item
	asset := embedassets.Read("browser.js")
	payload := bytes.Repeat(asset, protocol.MaxFrame/len(asset)+1)[:protocol.MaxFrame]
	g.forward(payload)
	g.forward(payload)
	g.forward(payload)
	deadline := time.Now().Add(time.Second)
	for {
		item.mu.Lock()
		revoked, size := item.revoked, item.bytes
		item.mu.Unlock()
		if size > protocol.MaxQueue {
			t.Fatal("queue exceeded byte bound")
		}
		if revoked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queue overflow did not close controller")
		}
		time.Sleep(time.Millisecond)
	}
	if len(item.output) != 2 {
		t.Fatal("overflow frame was queued")
	}
	if !bytes.Equal(<-item.output, payload) {
		t.Fatal("opaque binary frame changed")
	}
}
func TestHTMLParserAndCSP(t *testing.T) {
	output, err := RewriteHTML(embedassets.Read("login.html"), "https://bridge.example.test")
	if err == nil || output != nil {
		t.Fatal("unexpected entrance accepted")
	}
	source := `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="default-src 'self'; connect-src 'self'; script-src 'self'"><script type="module" src="/app.js"></script></head><body></body></html>`
	output, err = RewriteHTML([]byte(source), "https://bridge.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "wss://bridge.example.test") || !strings.Contains(string(output), "/cwb/browser.js") {
		t.Fatal("CSP or transport missing")
	}
}
func TestExplicitProxyTrust(t *testing.T) {
	g := New(config.Default(), auth.New(false, nil), logger.New(io.Discard), 0)
	for _, item := range []struct {
		remote, forwarded, want string
		valid                   bool
	}{{"127.0.0.1:1000", "192.0.2.8", "192.0.2.8", true}, {"192.0.2.2:1000", "192.0.2.8", "192.0.2.2", true}, {"127.0.0.1:1000", "", "127.0.0.1", true}, {"127.0.0.1:1000", "invalid", "", false}} {
		request := httptest.NewRequest("GET", "https://chat.example.com/", nil)
		request.RemoteAddr = item.remote
		request.Header.Set("CF-Connecting-IP", item.forwarded)
		value, err := g.clientIP(request)
		if (err == nil) != item.valid || value != item.want {
			t.Fatal("proxy trust boundary")
		}
	}
}
func TestPublicTicketExchangeSecurity(t *testing.T) {
	record, err := auth.Hash(auth.Random())
	if err != nil {
		t.Fatal(err)
	}
	c := publicConfig()
	c.Auth.Password = record
	g := New(c, auth.New(false, record), logger.New(io.Discard), 0)
	defer g.Auth.RevokeAll()
	ticket, err := g.Auth.IssueTicket(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	pre := httptest.NewRecorder()
	g.ServeHTTP(pre, httptest.NewRequest("GET", c.PublicOrigin+"/auth/csrf", nil))
	body := []byte(`{"ticket":"` + ticket + `"}`)
	csrf := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(pre.Body.String()), `{"csrf":"`), `"}`)
	badOrigin := httptest.NewRequest("POST", c.PublicOrigin+"/auth/ticket", bytes.NewReader(body))
	badOrigin.Header.Set("Origin", "https://sibling.example.com")
	badOrigin.Header.Set("X-CSRF-Token", csrf)
	for _, cookie := range pre.Result().Cookies() {
		badOrigin.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, badOrigin)
	if rec.Code != 403 {
		t.Fatal("cross-origin ticket accepted", rec.Code)
	}
	good := httptest.NewRequest("POST", c.PublicOrigin+"/auth/ticket", bytes.NewReader(body))
	good.Header.Set("Origin", c.PublicOrigin)
	good.Header.Set("X-CSRF-Token", csrf)
	good.RemoteAddr = "127.0.0.1:1234"
	for _, cookie := range pre.Result().Cookies() {
		good.AddCookie(cookie)
	}
	rec = httptest.NewRecorder()
	g.ServeHTTP(rec, good)
	if rec.Code != 200 {
		t.Fatal("ticket exchange failed", rec.Code)
	}
	var gotSession bool
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "__Host-cwb_session" && cookie.Secure && cookie.HttpOnly {
			gotSession = true
		}
	}
	if !gotSession {
		t.Fatal("missing secure host session cookie")
	}
	replay := httptest.NewRequest("POST", c.PublicOrigin+"/auth/ticket", bytes.NewReader(body))
	replay.Header.Set("Origin", c.PublicOrigin)
	replay.Header.Set("X-CSRF-Token", csrf)
	replay.RemoteAddr = "127.0.0.1:1235"
	for _, cookie := range pre.Result().Cookies() {
		replay.AddCookie(cookie)
	}
	rec = httptest.NewRecorder()
	g.ServeHTTP(rec, replay)
	if rec.Code != 401 {
		t.Fatal("replayed ticket accepted", rec.Code)
	}
}

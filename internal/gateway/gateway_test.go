package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"remote-codex/internal/auth"
	"remote-codex/internal/config"
	"remote-codex/internal/embedassets"
	"remote-codex/internal/logger"
	"remote-codex/internal/protocol"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLifecycleAuthenticationBoundary(t *testing.T) {
	c := publicConfig()
	g := New(c, auth.New(false, nil), logger.New(io.Discard), 0)
	t.Cleanup(g.Auth.Close)
	var wakes, touches atomic.Int64
	g.SetHostLifecycle(func(ctx context.Context) error {
		wakes.Add(1)
		return ctx.Err()
	}, func() { touches.Add(1) }, nil)
	session := g.Auth.CookieSession(time.Now().Add(time.Hour))
	for _, authenticated := range []bool{false, true} {
		for _, item := range []struct {
			method, path string
			wakes        bool
		}{
			{"GET", "/", true},
			{"HEAD", "/index.html", true},
			{"GET", "/cwb/browser.js", true},
			{"POST", "/api/controller", true},
			{"POST", "/api/takeover", true},
			{"GET", "/livez", false},
			{"GET", "/healthz", false},
			{"GET", "/auth/login", false},
			{"GET", "/auth/login.js", false},
			{"GET", "/auth/login.css", false},
			{"GET", "/auth/csrf", false},
			{"HEAD", "/livez", false},
			{"HEAD", "/auth/login", false},
			{"HEAD", "/auth/login.js", false},
			{"HEAD", "/auth/login.css", false},
			{"HEAD", "/auth/csrf", false},
			{"GET", "/auth/ticket", false},
			{"POST", "/auth/login", false},
			{"POST", "/auth/ticket", false},
			{"GET", "/api/session", false},
			{"GET", "/bridge", false},
		} {
			t.Run(item.method+item.path+map[bool]string{true: "/authenticated", false: "/anonymous"}[authenticated], func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				request := httptest.NewRequest(item.method, c.PublicOrigin+item.path, strings.NewReader("{}")).WithContext(ctx)
				request.Header.Set("Origin", c.PublicOrigin)
				if authenticated {
					request.AddCookie(g.Auth.Cookie(session.RawToken, 3600))
					request.Header.Set("X-CSRF-Token", session.CSRF)
				}
				before := wakes.Load()
				response := httptest.NewRecorder()
				g.ServeHTTP(response, request)
				want := int64(0)
				if authenticated && item.wakes {
					want = 1
					if response.Code != 503 {
						t.Fatal("canceled wake must return unavailable", response.Code)
					}
				}
				if wakes.Load()-before != want || touches.Load() != 0 {
					t.Fatal("unexpected lifecycle activity", wakes.Load()-before, touches.Load())
				}
			})
		}
	}
	for _, item := range []struct{ origin, csrf string }{{"https://other.example.com", session.CSRF}, {c.PublicOrigin, "invalid"}} {
		request := httptest.NewRequest("POST", c.PublicOrigin+"/api/controller", strings.NewReader("{}"))
		request.AddCookie(g.Auth.Cookie(session.RawToken, 3600))
		request.Header.Set("Origin", item.origin)
		request.Header.Set("X-CSRF-Token", item.csrf)
		before := wakes.Load()
		response := httptest.NewRecorder()
		g.ServeHTTP(response, request)
		if response.Code != 403 || wakes.Load() != before {
			t.Fatal("rejected request must not wake the host")
		}
	}
}

func TestStandbyHealthAndPendingClients(t *testing.T) {
	c := publicConfig()
	g := New(c, auth.New(false, nil), logger.New(io.Discard), 0)
	t.Cleanup(g.Auth.Close)
	standby := true
	g.SetHostLifecycle(nil, nil, func() bool {
		// 回调允许读取 Gateway 状态，验证调用期间已释放互斥锁。
		g.HasActiveClients()
		return standby
	})
	session := g.Auth.CookieSession(time.Now().Add(time.Hour))
	for _, enabled := range []bool{true, false} {
		standby = enabled
		request := httptest.NewRequest("GET", c.PublicOrigin+"/healthz", nil)
		request.AddCookie(g.Auth.Cookie(session.RawToken, 3600))
		response := httptest.NewRecorder()
		g.ServeHTTP(response, request)
		var health map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
			t.Fatal(err)
		}
		if response.Code != 200 || health["hostStandby"] != enabled || health["shimConnected"] != false || (health["compatStatus"] == "standby") != enabled {
			t.Fatal(response.Code, health)
		}
	}
	if g.HasActiveClients() {
		t.Fatal("new gateway must have no clients")
	}
	item := &claim{id: auth.Random(), session: session.ID, started: time.Now()}
	g.claims[item.id] = item
	if !g.HasActiveClients() {
		t.Fatal("pending claim must prevent idle detach without a controller owner")
	}
	delete(g.claims, item.id)
	if g.HasActiveClients() {
		t.Fatal("removed claim must no longer prevent idle detach")
	}
}

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
	var wakes atomic.Int64
	g.SetHostLifecycle(func(ctx context.Context) error {
		wakes.Add(1)
		return ctx.Err()
	}, nil, nil)
	t.Cleanup(func() {
		if wakes.Load() != 0 {
			t.Error("ticket exchange must not wake the host")
		}
	})
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

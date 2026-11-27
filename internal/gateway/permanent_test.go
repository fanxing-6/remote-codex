package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"remote-codex/internal/auth"
	"remote-codex/internal/logger"
	"testing"
)

func TestPermanentCookieRenewalAndLogoutReload(t *testing.T) {
	password := auth.Random()
	record, err := auth.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	c := publicConfig()
	c.Auth.Password = record
	path := filepath.Join(t.TempDir(), "auth-state.json")
	newGateway := func() *Gateway {
		s := auth.New(false, record)
		if err := s.SetStatePath(path); err != nil {
			t.Fatal(err)
		}
		if err := s.LoadState(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		return New(c, s, logger.New(io.Discard), 0)
	}
	g := newGateway()
	send := func(method, path string, body any, cookies []*http.Cookie, csrf, origin string) *httptest.ResponseRecorder {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, c.PublicOrigin+path, bytes.NewReader(data))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", csrf)
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		return w
	}
	challenge := send("GET", "/auth/csrf", nil, nil, "", "")
	var pre struct{ CSRF string }
	if err = json.Unmarshal(challenge.Body.Bytes(), &pre); err != nil {
		t.Fatal(err)
	}
	login := send("POST", "/auth/login", map[string]string{"password": password}, challenge.Result().Cookies(), pre.CSRF, c.PublicOrigin)
	if login.Code != 200 {
		t.Fatal("password login", login.Code)
	}
	first := login.Result().Cookies()
	if len(first) != 1 || first[0].MaxAge != 34560000 || !first[0].Secure || !first[0].HttpOnly || first[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("permanent cookie")
	}
	ticket, err := g.Auth.IssuePermanentTicket()
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct {
		csrf, origin string
		cookies      []*http.Cookie
	}{{"", c.PublicOrigin, nil}, {pre.CSRF, "https://sibling.example.com", challenge.Result().Cookies()}, {auth.Random(), c.PublicOrigin, challenge.Result().Cookies()}} {
		if got := send("POST", "/auth/ticket", map[string]string{"ticket": ticket}, bad.cookies, bad.csrf, bad.origin); got.Code != 403 {
			t.Fatal("ticket protection", got.Code)
		}
	}
	var second []*http.Cookie
	for i := 0; i < 2; i++ {
		w := send("POST", "/auth/ticket", map[string]string{"ticket": ticket}, challenge.Result().Cookies(), pre.CSRF, c.PublicOrigin)
		if w.Code != 200 || len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].MaxAge != 34560000 {
			t.Fatal("permanent exchange", w.Code)
		}
		second = w.Result().Cookies()
	}
	g.Auth.Close()
	g = newGateway()
	var session auth.Session
	for _, path := range []string{"/api/session", "/"} {
		w := send("GET", path, nil, first, "", "")
		if path == "/api/session" {
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &session) != nil || !session.Permanent {
				t.Fatal("session reload")
			}
		}
		cookies := w.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Value != first[0].Value || cookies[0].MaxAge != 34560000 {
			t.Fatal("sliding renewal", path)
		}
	}
	if w := send("POST", "/auth/logout", nil, first, auth.Random(), c.PublicOrigin); w.Code != 403 {
		t.Fatal("logout CSRF")
	}
	logout := send("POST", "/auth/logout", nil, first, session.CSRF, c.PublicOrigin)
	if logout.Code != 200 || logout.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout", logout.Code)
	}
	g.Auth.Close()
	g = newGateway()
	if w := send("GET", "/healthz", nil, first, "", ""); w.Code != 401 {
		t.Fatal("logout not persisted")
	}
	if w := send("GET", "/healthz", nil, second, "", ""); w.Code != 200 {
		t.Fatal("other device revoked")
	}
	if w := send("POST", "/auth/ticket", map[string]string{"ticket": ticket}, challenge.Result().Cookies(), pre.CSRF, c.PublicOrigin); w.Code != 403 {
		t.Fatal("pre-cookie persisted")
	}
}

func TestCloudflareModeRejectsPasswordCookiesAndTickets(t *testing.T) {
	record, err := auth.Hash(auth.Random())
	if err != nil {
		t.Fatal(err)
	}
	s := auth.New(false, record)
	defer s.Close()
	ticket, err := s.IssuePermanentTicket()
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.RedeemTicket("login", ticket)
	if err != nil {
		t.Fatal(err)
	}
	c := publicConfig()
	c.Auth.Mode = "cloudflare-access"
	g := New(c, s, logger.New(io.Discard), 0)
	challenge := httptest.NewRecorder()
	g.ServeHTTP(challenge, httptest.NewRequest("GET", c.PublicOrigin+"/auth/csrf", nil))
	var pre struct{ CSRF string }
	if err = json.Unmarshal(challenge.Body.Bytes(), &pre); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/auth/login", "/auth/ticket"} {
		r := httptest.NewRequest("POST", c.PublicOrigin+path, bytes.NewBufferString(`{}`))
		r.Header.Set("Origin", c.PublicOrigin)
		r.Header.Set("X-CSRF-Token", pre.CSRF)
		for _, cookie := range challenge.Result().Cookies() {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("password exchange allowed in access mode")
		}
	}
	r := httptest.NewRequest("GET", c.PublicOrigin+"/api/session", nil)
	r.AddCookie(s.Cookie(session.RawToken, auth.PermanentCookieMaxAge))
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 401 || len(w.Result().Cookies()) != 0 {
		t.Fatal("access accepted password session")
	}
}

func TestAuthStateWriteFailureReturnsServerError(t *testing.T) {
	password := auth.Random()
	record, err := auth.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	c := publicConfig()
	c.Auth.Password = record
	s := auth.New(false, record)
	defer s.Close()
	path := filepath.Join(t.TempDir(), "auth-state.json")
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err = s.SetStatePath(path); err != nil {
		t.Fatal(err)
	}
	g := New(c, s, logger.New(io.Discard), 0)
	pre := httptest.NewRecorder()
	g.ServeHTTP(pre, httptest.NewRequest("GET", c.PublicOrigin+"/auth/csrf", nil))
	var challenge struct{ CSRF string }
	if err = json.Unmarshal(pre.Body.Bytes(), &challenge); err != nil {
		t.Fatal(err)
	}
	ticket, err := s.IssueTicket(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		path string
		body map[string]string
	}{{"/auth/login", map[string]string{"password": password}}, {"/auth/ticket", map[string]string{"ticket": ticket}}} {
		data, err := json.Marshal(item.body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", c.PublicOrigin+item.path, bytes.NewReader(data))
		r.Header.Set("Origin", c.PublicOrigin)
		r.Header.Set("X-CSRF-Token", challenge.CSRF)
		for _, cookie := range pre.Result().Cookies() {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != 500 || len(w.Result().Cookies()) != 0 || s.Count() != 0 {
			t.Fatal("write failure granted session", item.path, w.Code)
		}
	}
}

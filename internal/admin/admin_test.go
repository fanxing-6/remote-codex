package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"remote-codex/internal/auth"
	"remote-codex/internal/config"
	"remote-codex/internal/logger"
	"testing"
)

func TestHostOriginCSRFAndOneTimeBootstrap(t *testing.T) {
	dir := t.TempDir()
	c := config.Default()
	c.TunnelMode = "custom"
	c.PublicOrigin = "https://chat.example.com"
	c.AllowedHosts = []string{"chat.example.com"}
	a, err := New(c, auth.New(true, nil), logger.New(io.Discard), Operations{}, dir, filepath.Join(dir, "config.json"), filepath.Join(dir, "tunnel.yml"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Store.RevokeAll()
	for _, host := range []string{"evil.example", "localhost:18766", "127.0.0.1:18765", "127.0.0.1:18766.evil.example"} {
		request := httptest.NewRequest("GET", "http://127.0.0.1:18766/", nil)
		request.Host = host
		recorder := httptest.NewRecorder()
		a.ServeHTTP(recorder, request)
		if recorder.Code != 403 {
			t.Fatal("rebinding accepted", host)
		}
	}
	challenge := httptest.NewRecorder()
	a.ServeHTTP(challenge, httptest.NewRequest("GET", "http://127.0.0.1:18766/api/bootstrap/csrf", nil))
	var pre struct{ CSRF string }
	if json.Unmarshal(challenge.Body.Bytes(), &pre) != nil {
		t.Fatal("challenge")
	}
	code, err := os.ReadFile(filepath.Join(dir, "bootstrap.code"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"code": string(code)})
	if err != nil {
		t.Fatal(err)
	}
	send := func(origin, csrf, site string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", "http://127.0.0.1:18766/api/bootstrap", bytes.NewReader(body))
		request.Header.Set("Origin", origin)
		request.Header.Set("X-CSRF-Token", csrf)
		request.Header.Set("Sec-Fetch-Site", site)
		for _, cookie := range challenge.Result().Cookies() {
			request.AddCookie(cookie)
		}
		recorder := httptest.NewRecorder()
		a.ServeHTTP(recorder, request)
		return recorder
	}
	for _, values := range [][3]string{{"https://evil.example", pre.CSRF, "cross-site"}, {"http://127.0.0.1:18766", auth.Random(), "same-origin"}, {"http://127.0.0.1:18766", pre.CSRF, "cross-site"}} {
		if send(values[0], values[1], values[2]).Code != 403 {
			t.Fatal("CSRF accepted")
		}
	}
	exchange := send("http://127.0.0.1:18766", pre.CSRF, "same-origin")
	if exchange.Code != 200 {
		t.Fatal("bootstrap exchange", exchange.Code)
	}
	if send("http://127.0.0.1:18766", pre.CSRF, "same-origin").Code != 403 {
		t.Fatal("bootstrap reused")
	}
	if _, err = os.Stat(filepath.Join(dir, "bootstrap.code")); !os.IsNotExist(err) {
		t.Fatal("code not consumed")
	}
	for _, cookie := range exchange.Result().Cookies() {
		if cookie.Name == "cwb_admin" && (!cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode) {
			t.Fatal("admin cookie attributes")
		}
	}
	var adminSession struct{ CSRF string }
	if json.Unmarshal(exchange.Body.Bytes(), &adminSession) != nil {
		t.Fatal("admin session")
	}
	issue := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", "http://127.0.0.1:18766/api/auth/ticket", bytes.NewReader([]byte(`{"ttlSeconds":600}`)))
		request.Header.Set("Origin", "http://127.0.0.1:18766")
		request.Header.Set("X-CSRF-Token", adminSession.CSRF)
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		for _, cookie := range exchange.Result().Cookies() {
			request.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, request)
		return rec
	}
	if issue().Code != 409 {
		t.Fatal("ticket issued before password configured")
	}
	record, err := auth.Hash(auth.Random())
	if err != nil {
		t.Fatal(err)
	}
	a.Config.Auth.Password = record
	a.PublicAuth.ChangePassword(record)
	issued := issue()
	if issued.Code != 200 {
		t.Fatal("ticket issue failed", issued.Code)
	}
	var payload struct {
		URL          string `json:"url"`
		ExpiresInSec int    `json:"expiresInSec"`
	}
	if json.Unmarshal(issued.Body.Bytes(), &payload) != nil {
		t.Fatal("ticket payload")
	}
	if payload.ExpiresInSec != 600 || !bytes.Contains([]byte(payload.URL), []byte("/auth/login#ticket=")) || bytes.Contains([]byte(payload.URL), []byte("?")) {
		t.Fatal("unsafe magic link format", payload.URL)
	}
}

package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"remote-codex/internal/auth"
	"remote-codex/internal/config"
	"remote-codex/internal/logger"
)

func TestBootstrapCodeRequiresAdminSession(t *testing.T) {
	dir := t.TempDir()
	store := auth.New(false, nil)
	defer store.Close()
	a, err := New(config.Default(), store, logger.New(io.Discard), Operations{}, dir, filepath.Join(dir, "config.json"), filepath.Join(dir, "tunnel.yml"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Store.Close()
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18766/api/bootstrap/code", nil)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatal("unauthenticated bootstrap code access")
	}
	session := a.Store.CookieSession(time.Now().Add(time.Minute))
	r.AddCookie(a.Store.Cookie(session.RawToken, 60))
	w = httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	var result struct {
		Code string `json:"code"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Code == "" || result.Code != a.code {
		t.Fatal("bootstrap code unavailable")
	}
	if err = a.RotateCode(); err != nil {
		t.Fatal(err)
	}
	if a.code == result.Code {
		t.Fatal("bootstrap code reused after rotation")
	}
}

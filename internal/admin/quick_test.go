package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"remote-codex/internal/auth"
	"remote-codex/internal/config"
	"remote-codex/internal/logger"
)

func TestQuickListenerSettingsPreserveMode(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	c := config.Default()
	c.PublicOrigin = "https://assigned.trycloudflare.com"
	c.AllowedHosts = []string{"assigned.trycloudflare.com"}
	record, err := auth.Hash(auth.Random())
	if err != nil {
		t.Fatal(err)
	}
	c.Auth.Password = record
	public := auth.New(false, record)
	defer public.Close()
	a, err := New(c, public, logger.New(io.Discard), Operations{Save: func(updated config.Config) error { return config.Save(path, updated) }}, directory, path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Store.Close()
	owner := a.Store.CookieSession(time.Now().Add(time.Hour))
	body, err := json.Marshal(map[string]any{"publicOrigin": c.PublicOrigin, "dataPort": 19765, "adminPort": 19766})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:18766/api/listeners", bytes.NewReader(body))
	r.Header.Set("Origin", "http://127.0.0.1:18766")
	r.Header.Set("X-CSRF-Token", owner.CSRF)
	r.AddCookie(a.Store.Cookie(owner.RawToken, 86400))
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Mode() != "quick" || loaded.AllowInsecureLocalhost || loaded.PublicOrigin != "" || len(loaded.AllowedHosts) != 0 || loaded.DataPort != 19765 || loaded.AdminPort != 19766 || *loaded.Auth.Password != *record {
		t.Fatal("quick listener configuration changed authentication or mode")
	}
}

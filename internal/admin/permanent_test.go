package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"remote-codex/internal/auth"
	"remote-codex/internal/config"
	"remote-codex/internal/logger"
	"strings"
	"testing"
	"time"
)

func TestPermanentTicketAPIAndRevocationRoutes(t *testing.T) {
	for _, route := range []string{"auth/revoke-all", "password", "setup", "listeners"} {
		t.Run(route, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "auth-state.json")
			configPath := filepath.Join(dir, "config.json")
			c := config.Default()
			c.TunnelMode = "custom"
			c.PublicOrigin = "https://chat.example.com"
			c.AllowedHosts = []string{"chat.example.com"}
			record, err := auth.Hash(auth.Random())
			if err != nil {
				t.Fatal(err)
			}
			c.Auth.Password = record
			public := auth.New(false, record)
			defer public.Close()
			if err = public.SetStatePath(path); err != nil {
				t.Fatal(err)
			}
			a, err := New(c, public, logger.New(io.Discard), Operations{Save: func(updated config.Config) error { return config.Save(configPath, updated) }}, dir, configPath, "")
			if err != nil {
				t.Fatal(err)
			}
			defer a.Store.Close()
			owner := a.Store.CookieSession(time.Now().Add(time.Hour))
			send := func(route string, body any) *httptest.ResponseRecorder {
				data, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest("POST", "http://127.0.0.1:18766/api/"+route, bytes.NewReader(data))
				r.Header.Set("Origin", "http://127.0.0.1:18766")
				r.Header.Set("X-CSRF-Token", owner.CSRF)
				r.AddCookie(a.Store.Cookie(owner.RawToken, 86400))
				w := httptest.NewRecorder()
				a.ServeHTTP(w, r)
				return w
			}
			var permanent, temporary string
			for _, kind := range []bool{false, true} {
				w := send("auth/ticket", map[string]any{"permanent": kind, "ttlSeconds": 600})
				if w.Code != 200 {
					t.Fatal("ticket API", w.Code)
				}
				var response struct {
					URL          string
					Permanent    bool
					ExpiresInSec int
				}
				if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.Permanent != kind || (kind && response.ExpiresInSec != 0) || (!kind && response.ExpiresInSec != 600) || !strings.HasPrefix(response.URL, c.PublicOrigin+"/auth/login#ticket=") {
					t.Fatal("ticket metadata")
				}
				raw := strings.Split(response.URL, "#ticket=")[1]
				if kind {
					permanent = raw
				} else {
					temporary = raw
				}
			}
			session, err := public.RedeemTicket("device", permanent)
			if err != nil {
				t.Fatal(err)
			}
			body := map[string]any{}
			switch route {
			case "password":
				body["password"] = auth.Random()
			case "setup":
				body = map[string]any{"publicOrigin": c.PublicOrigin, "mode": "password", "password": auth.Random()}
			case "listeners":
				body = map[string]any{"publicOrigin": c.PublicOrigin, "dataPort": c.DataPort, "adminPort": c.AdminPort}
			}
			if w := send(route, body); w.Code != 200 {
				t.Fatal("revocation route", w.Code)
			}
			if public.Count() != 0 || a.Store.Get(owner.ID) == nil {
				t.Fatal("revocation scope")
			}
			if _, err = public.RedeemTicket("temporary", temporary); err == nil {
				t.Fatal("temporary survived")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var state struct {
				Sessions []json.RawMessage
				Tickets  []json.RawMessage
			}
			if err = json.Unmarshal(data, &state); err != nil {
				t.Fatal(err)
			}
			if len(state.Sessions) != 0 || len(state.Tickets) != 0 {
				t.Fatal("persisted revocation")
			}
			reloaded := auth.New(false, a.Config.Auth.Password)
			defer reloaded.Close()
			if err = reloaded.SetStatePath(path); err != nil {
				t.Fatal(err)
			}
			if err = reloaded.LoadState(); err != nil {
				t.Fatal(err)
			}
			if reloaded.Get(session.ID) != nil {
				t.Fatal("session revived")
			}
			if _, err = reloaded.RedeemTicket("permanent", permanent); err == nil {
				t.Fatal("ticket revived")
			}
		})
	}
}

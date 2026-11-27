package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"remote-codex/internal/auth"
	"remote-codex/internal/config"
	"remote-codex/internal/embedassets"
	"remote-codex/internal/gateway"
	"remote-codex/internal/logger"
	"remote-codex/internal/tunnel"
	"strconv"
	"sync"
	"time"
)

type Operations struct {
	Status        func() any
	Doctor        func() any
	Start         func() error
	Stop          func() error
	Reconnect     func() error
	Disconnect    func() error
	Shutdown      func()
	Save          func(config.Config) error
	AutoStart     func(bool) error
	RuntimeConfig func() config.Config
	TunnelRestart func() error
	TunnelStatus  func() tunnel.StatusSnapshot
}
type Admin struct {
	mu                                 sync.Mutex
	Config                             config.Config
	Store                              *auth.Store
	PublicAuth                         *auth.Store
	Log                                *logger.Logger
	Ops                                Operations
	code                               string
	runtimeDir, configPath, tunnelPath string
}

func New(c config.Config, public *auth.Store, log *logger.Logger, ops Operations, runtimeDir, configPath, tunnelPath string) (*Admin, error) {
	a := &Admin{Config: c, Store: auth.NewNamed("cwb_admin"), PublicAuth: public, Log: log, Ops: ops, runtimeDir: runtimeDir, configPath: configPath, tunnelPath: tunnelPath}
	return a, a.RotateCode()
}
func (a *Admin) RotateCode() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.code = auth.Random()
	return config.AtomicWrite(filepath.Join(a.runtimeDir, "bootstrap.code"), []byte(a.code), 0600)
}
func (a *Admin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	currentConfig := a.currentConfig()
	expected := "127.0.0.1:" + strconv.Itoa(currentConfig.AdminPort)
	if r.Host != expected {
		http.Error(w, "HOST_REJECTED", 403)
		return
	}
	change := r.Method != "GET" && r.Method != "HEAD"
	if change && (r.Header.Get("Origin") != "http://"+expected || (r.Header.Get("Sec-Fetch-Site") != "" && r.Header.Get("Sec-Fetch-Site") != "same-origin")) {
		http.Error(w, "ORIGIN_REJECTED", 403)
		return
	}
	if r.URL.Path == "/api/bootstrap/csrf" && r.Method == "GET" {
		csrf := a.Store.Begin(w)
		if csrf == "" {
			http.Error(w, "RATE_LIMITED", 429)
			return
		}
		gateway.JSON(w, 200, map[string]string{"csrf": csrf})
		return
	}
	if r.URL.Path == "/api/bootstrap" && r.Method == "POST" {
		if !a.Store.CheckPre(r, r.Header.Get("X-CSRF-Token")) {
			http.Error(w, "CSRF_REJECTED", 403)
			return
		}
		var body struct {
			Code string `json:"code"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil {
			http.Error(w, "BODY_INVALID", 400)
			return
		}
		a.mu.Lock()
		valid := auth.Equal(a.code, body.Code)
		if valid {
			a.code = ""
			if err := os.Remove(filepath.Join(a.runtimeDir, "bootstrap.code")); err != nil {
				a.mu.Unlock()
				http.Error(w, "BOOTSTRAP_CONSUME_FAILED", 500)
				return
			}
		}
		a.mu.Unlock()
		if !valid {
			http.Error(w, "BOOTSTRAP_REJECTED", 403)
			return
		}
		item := a.Store.CookieSession(timeNowPlusDay())
		if item == nil {
			http.Error(w, "SESSION_LIMIT", 429)
			return
		}
		http.SetCookie(w, a.Store.Cookie(item.RawToken, 86400))
		gateway.JSON(w, 200, item)
		return
	}
	if !stringsAPI(r.URL.Path) {
		a.assets(w, r)
		return
	}
	session := a.Store.FromRequest(r)
	if session == nil {
		http.Error(w, "ADMIN_AUTH_REQUIRED", 401)
		return
	}
	if change && !auth.Equal(session.CSRF, r.Header.Get("X-CSRF-Token")) {
		http.Error(w, "CSRF_REJECTED", 403)
		return
	}
	var err error
	switch {
	case r.URL.Path == "/api/session" && r.Method == "GET":
		gateway.JSON(w, 200, session)
		return
	case r.URL.Path == "/api/bootstrap/rotate" && r.Method == "POST":
		err = a.RotateCode()
	case r.URL.Path == "/api/bootstrap/code" && r.Method == "GET":
		a.mu.Lock()
		code := a.code
		a.mu.Unlock()
		gateway.JSON(w, 200, map[string]string{"code": code})
		return
	case r.URL.Path == "/api/status" && r.Method == "GET":
		gateway.JSON(w, 200, a.Ops.Status())
		return
	case r.URL.Path == "/api/doctor" && r.Method == "GET":
		gateway.JSON(w, 200, a.Ops.Doctor())
		return
	case r.URL.Path == "/api/logs" && r.Method == "GET":
		gateway.JSON(w, 200, a.Log.Events())
		return
	case r.URL.Path == "/api/config" && r.Method == "GET":
		_, cloudflaredErr := tunnel.FindCloudflared()
		cloudflaredFound := cloudflaredErr == nil
		if a.Ops.TunnelStatus != nil {
			cloudflaredFound = a.Ops.TunnelStatus().CloudflaredFound
		}
		safe := map[string]any{"publicOrigin": currentConfig.PublicOrigin, "dataPort": currentConfig.DataPort, "adminPort": currentConfig.AdminPort, "authMode": currentConfig.Auth.Mode, "teamDomain": currentConfig.Auth.TeamDomain, "audience": currentConfig.Auth.Audience, "configured": currentConfig.Auth.Password != nil || currentConfig.Auth.Mode == "cloudflare-access", "tunnelMode": currentConfig.Mode(), "quickTunnelProtocol": currentConfig.QuickTunnelProtocol, "cloudflaredFound": cloudflaredFound, "tlsPort": currentConfig.TLSPort, "tlsAutocert": currentConfig.TLSAutocert}
		gateway.JSON(w, 200, safe)
		return
	case r.URL.Path == "/api/host/start" && r.Method == "POST":
		err = a.Ops.Start()
	case r.URL.Path == "/api/host/stop" && r.Method == "POST":
		err = a.Ops.Stop()
	case r.URL.Path == "/api/host/reconnect" && r.Method == "POST":
		err = a.Ops.Reconnect()
	case r.URL.Path == "/api/controller/disconnect" && r.Method == "POST":
		err = a.Ops.Disconnect()
	case r.URL.Path == "/api/auth/revoke-all" && r.Method == "POST":
		err = a.PublicAuth.RevokeAll()
		a.Log.Add(logger.Event{Event: "sessions_revoked"})
	case r.URL.Path == "/api/auth/ticket" && r.Method == "POST":
		if currentConfig.PublicOrigin == "" {
			http.Error(w, "TUNNEL_NOT_READY", 409)
			return
		}
		if currentConfig.Auth.Mode != "password" || currentConfig.Auth.Password == nil {
			http.Error(w, "TICKET_REQUIRES_PASSWORD_MODE", 409)
			return
		}
		var body struct {
			TTLSeconds int  `json:"ttlSeconds"`
			Permanent  bool `json:"permanent"`
		}
		if r.ContentLength > 0 {
			if decode(w, r, &body) != nil {
				http.Error(w, "BODY_INVALID", 400)
				return
			}
		}
		ttl := 5 * time.Minute
		if body.TTLSeconds >= 60 && body.TTLSeconds <= 900 {
			ttl = time.Duration(body.TTLSeconds) * time.Second
		}
		var ticket string
		var ticketErr error
		if body.Permanent {
			ticket, ticketErr = a.PublicAuth.IssuePermanentTicket()
			ttl = 0
		} else {
			ticket, ticketErr = a.PublicAuth.IssueTicket(ttl)
		}
		if ticketErr != nil {
			err = ticketErr
			break
		}
		a.Log.Add(logger.Event{Event: "login_ticket_issued"})
		gateway.JSON(w, 200, map[string]any{"url": currentConfig.PublicOrigin + "/auth/login#ticket=" + ticket, "permanent": body.Permanent, "expiresInSec": int(ttl.Seconds())})
		return
	case r.URL.Path == "/api/password" && r.Method == "POST":
		var body struct {
			Password string `json:"password"`
		}
		if decode(w, r, &body) != nil {
			http.Error(w, "BODY_INVALID", 400)
			return
		}
		record, hashErr := auth.Hash(body.Password)
		body.Password = ""
		if hashErr != nil {
			http.Error(w, "PASSWORD_LENGTH_INVALID", 400)
			return
		}
		updated := currentConfig
		updated.Auth.Password = record
		updated.Auth.Mode = "password"
		err = a.save(updated)
		if err == nil {
			err = a.PublicAuth.ChangePassword(record)
		}
	case r.URL.Path == "/api/setup" && r.Method == "POST":
		var body struct {
			PublicOrigin        string `json:"publicOrigin"`
			Mode                string `json:"mode"`
			Password            string `json:"password"`
			TeamDomain          string `json:"teamDomain"`
			Audience            string `json:"audience"`
			TunnelMode          string `json:"tunnelMode"`
			QuickTunnelProtocol string `json:"quickTunnelProtocol"`
			TLSPort             int    `json:"tlsPort"`
			TLSAutocert         bool   `json:"tlsAutocert"`
		}
		if decode(w, r, &body) != nil {
			http.Error(w, "BODY_INVALID", 400)
			return
		}
		updated := currentConfig
		if body.TunnelMode != "" {
			updated.TunnelMode = body.TunnelMode
		}
		if body.QuickTunnelProtocol != "" {
			updated.QuickTunnelProtocol = body.QuickTunnelProtocol
		}
		parsed, parseErr := url.Parse(body.PublicOrigin)
		if parseErr != nil {
			http.Error(w, "ORIGIN_INVALID", 400)
			return
		}
		updated.PublicOrigin = body.PublicOrigin
		updated.AllowedHosts = []string{parsed.Host}
		updated.AllowInsecureLocalhost = parsed.Scheme == "http" && parsed.Hostname() == "127.0.0.1"
		updated.TLSPort, updated.TLSAutocert = body.TLSPort, body.TLSAutocert
		updated = tunnel.PrepareSetup(updated)
		previousPassword := updated.Auth.Password
		updated.Auth = config.Auth{Mode: body.Mode, TeamDomain: body.TeamDomain, Audience: body.Audience}
		if body.Mode == "password" {
			if body.Password == "" {
				updated.Auth.Password = previousPassword
			} else {
				updated.Auth.Password, err = auth.Hash(body.Password)
			}
			body.Password = ""
			if err != nil || updated.Auth.Password == nil {
				http.Error(w, "PASSWORD_LENGTH_INVALID", 400)
				return
			}
		}
		err = a.save(updated)
		if err == nil {
			err = a.PublicAuth.ChangePassword(updated.Auth.Password)
		}
	case r.URL.Path == "/api/listeners" && r.Method == "POST":
		var body struct {
			DataPort     int    `json:"dataPort"`
			AdminPort    int    `json:"adminPort"`
			PublicOrigin string `json:"publicOrigin"`
		}
		if decode(w, r, &body) != nil {
			http.Error(w, "BODY_INVALID", 400)
			return
		}
		updated := currentConfig
		updated.DataPort = body.DataPort
		updated.AdminPort = body.AdminPort
		updated.PublicOrigin = body.PublicOrigin
		parsed, parseErr := url.Parse(body.PublicOrigin)
		if parseErr != nil {
			http.Error(w, "ORIGIN_INVALID", 400)
			return
		}
		updated.AllowedHosts = []string{parsed.Host}
		updated.AllowInsecureLocalhost = parsed.Scheme == "http" && parsed.Hostname() == "127.0.0.1"
		updated = tunnel.PrepareListeners(currentConfig, updated)
		err = a.save(updated)
		if err == nil {
			err = a.PublicAuth.RevokeAll()
			if err == nil {
				gateway.JSON(w, 200, map[string]string{"adminOrigin": fmt.Sprintf("http://127.0.0.1:%d", updated.AdminPort)})
				return
			}
		}
	case r.URL.Path == "/api/tunnel/restart" && r.Method == "POST":
		if a.Ops.TunnelRestart == nil {
			http.Error(w, "QUICK_UNAVAILABLE", 409)
			return
		}
		err = a.Ops.TunnelRestart()
	case r.URL.Path == "/api/tunnel/preview" && r.Method == "POST":
		var body struct {
			Hostname string `json:"hostname"`
		}
		if decode(w, r, &body) != nil {
			http.Error(w, "BODY_INVALID", 400)
			return
		}
		preview, planErr := tunnel.Plan(a.tunnelPath, body.Hostname, currentConfig.DataPort, currentConfig.AdminPort)
		if planErr != nil {
			err = planErr
			break
		}
		gateway.JSON(w, 200, preview)
		return
	case r.URL.Path == "/api/tunnel/apply" && r.Method == "POST":
		var body struct {
			Hostname string `json:"hostname"`
			Digest   string `json:"digest"`
			Confirm  bool   `json:"confirm"`
		}
		if decode(w, r, &body) != nil || !body.Confirm {
			http.Error(w, "CONFIRM_REQUIRED", 400)
			return
		}
		preview, planErr := tunnel.Plan(a.tunnelPath, body.Hostname, currentConfig.DataPort, currentConfig.AdminPort)
		if planErr != nil {
			err = planErr
			break
		}
		if !auth.Equal(preview.Digest, body.Digest) {
			http.Error(w, "PREVIEW_STALE", 409)
			return
		}
		_, err = tunnel.Apply(a.tunnelPath, preview)
	case r.URL.Path == "/api/autostart" && r.Method == "POST":
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if decode(w, r, &body) != nil {
			http.Error(w, "BODY_INVALID", 400)
			return
		}
		err = a.Ops.AutoStart(body.Enabled)
	case r.URL.Path == "/api/shutdown" && r.Method == "POST":
		a.Log.Add(logger.Event{Event: "service_shutdown_requested"})
		gateway.JSON(w, 200, map[string]bool{"ok": true})
		go a.Ops.Shutdown()
		return
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.Log.Add(logger.Event{Event: "admin_action_failed", ErrorCode: "ACTION_FAILED"})
		code := "ACTION_FAILED"
		for _, known := range []string{"CLOUDFLARED_NOT_FOUND", "QUICK_START_TIMEOUT", "QUICK_EXITED_BEFORE_URL", "QUICK_RATE_LIMITED", "CONFIG_QUICK_SECURITY_REQUIRED", "CONFIG_ORIGIN_INVALID", "CONFIG_HTTPS_REQUIRED", "QUICK_MODE_REQUIRED"} {
			if err.Error() == known {
				code = known
			}
		}
		gateway.JSON(w, 409, map[string]string{"error": code})
		return
	}
	gateway.JSON(w, 200, map[string]bool{"ok": true})
}
func (a *Admin) save(updated config.Config) error {
	if err := config.Validate(updated); err != nil {
		return err
	}
	if err := a.Ops.Save(updated); err != nil {
		return err
	}
	if a.Ops.RuntimeConfig != nil {
		updated = a.Ops.RuntimeConfig()
	}
	a.mu.Lock()
	a.Config = updated
	a.mu.Unlock()
	return nil
}
func (a *Admin) currentConfig() config.Config {
	if a.Ops.RuntimeConfig != nil {
		return a.Ops.RuntimeConfig()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Config
}
func stringsAPI(path string) bool { return len(path) >= 5 && path[:5] == "/api/" }
func decode(w http.ResponseWriter, r *http.Request, value any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}
func (a *Admin) assets(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "METHOD_REJECTED", 405)
		return
	}
	name := "index.html"
	if r.URL.Path != "/" {
		name = r.URL.Path[1:]
	}
	if name != "index.html" && name != "app.js" && name != "styles.css" {
		http.NotFound(w, r)
		return
	}
	sub, err := fs.Sub(embedassets.Files, "dist/admin-ui")
	if err != nil {
		panic(err)
	}
	data, err := fs.ReadFile(sub, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch filepath.Ext(name) {
	case ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case ".js":
		w.Header().Set("Content-Type", "text/javascript")
	case ".css":
		w.Header().Set("Content-Type", "text/css")
	}
	w.Write(data)
}
func (a *Admin) BootstrapURL() (string, error) {
	data, err := os.ReadFile(filepath.Join(a.runtimeDir, "bootstrap.code"))
	if err != nil {
		return "", errors.New("BOOTSTRAP_CODE_UNAVAILABLE")
	}
	return fmt.Sprintf("http://127.0.0.1:%d/#bootstrap=%s", a.currentConfig().AdminPort, data), nil
}

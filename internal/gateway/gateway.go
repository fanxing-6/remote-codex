package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"path"
	"remote-codex/internal/asar"
	"remote-codex/internal/auth"
	"remote-codex/internal/config"
	"remote-codex/internal/controller"
	"remote-codex/internal/embedassets"
	"remote-codex/internal/host"
	"remote-codex/internal/logger"
	"remote-codex/internal/protocol"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

type claim struct {
	mu          sync.Mutex
	id, session string
	generation  uint64
	socket      *websocket.Conn
	revoked     bool
	timer       *time.Timer
	output      chan []byte
	bytes       int
	writeMu     sync.Mutex
	device      string
	started     time.Time
	log         *logger.Logger
}

func (c *claim) write(kind int, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.socket.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	return c.socket.WriteMessage(kind, data)
}

func (c *claim) close(code int) {
	c.mu.Lock()
	if c.revoked {
		c.mu.Unlock()
		return
	}
	c.revoked = true
	socket := c.socket
	c.mu.Unlock()
	if socket != nil {
		if c.log != nil {
			c.log.Add(logger.Event{Event: "browser_ws_end", Status: code, Generation: c.generation})
		}
		if err := socket.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, "CONTROL_ENDED"), time.Now().Add(12*time.Second)); err != nil {
			if c.log != nil {
				c.log.Add(logger.Event{Event: "browser_ws_closed", ErrorCode: "WS_CLOSE_FRAME_FAILED", Generation: c.generation})
			}
			socket.Close()
			return
		}
		time.AfterFunc(5*time.Second, func() { socket.Close() })
	}
}

type Gateway struct {
	Config         config.Config
	Auth           *auth.Store
	Access         *auth.Access
	Log            *logger.Logger
	Controllers    *controller.Manager
	mu             sync.Mutex
	host           *host.Client
	archive        *asar.Archive
	html           []byte
	claims         map[string]*claim
	active         *claim
	started        time.Time
	trustedProxies []netip.Prefix
	ensureHost     func(context.Context) error
	touchActivity  func()
	hostStandby    func() bool
}

func (g *Gateway) SetHostLifecycle(ensure func(context.Context) error, touch func(), standby func() bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureHost, g.touchActivity, g.hostStandby = ensure, touch, standby
}

func (g *Gateway) HasActiveClients() bool {
	owner, _ := g.Controllers.Status()
	g.mu.Lock()
	defer g.mu.Unlock()
	return owner != "" || len(g.claims) > 0
}

func (g *Gateway) touchHostActivity() {
	g.mu.Lock()
	touch := g.touchActivity
	g.mu.Unlock()
	if touch != nil {
		touch()
	}
}

func (g *Gateway) wakeHost(ctx context.Context) error {
	g.mu.Lock()
	client, ensure := g.host, g.ensureHost
	g.mu.Unlock()
	if client == nil && ensure != nil {
		if err := ensure(ctx); err != nil {
			return err
		}
		g.touchHostActivity()
	} else if client != nil {
		g.touchHostActivity()
	}
	return nil
}

func New(c config.Config, store *auth.Store, log *logger.Logger, initialGeneration uint64) *Gateway {
	g := &Gateway{Config: c, Auth: store, Log: log, claims: map[string]*claim{}, started: time.Now()}
	for _, value := range c.TrustedProxy {
		g.trustedProxies = append(g.trustedProxies, netip.MustParsePrefix(value))
	}
	if c.Auth.Mode == "cloudflare-access" {
		g.Access = auth.NewAccess(c.Auth.TeamDomain, c.Auth.Audience)
	}
	g.Controllers = controller.New(initialGeneration, func(generation uint64) error {
		g.mu.Lock()
		g.active = nil
		client := g.host
		g.mu.Unlock()
		if client == nil {
			return errors.New("HOST_OFFLINE")
		}
		return client.Reset(generation)
	}, func(generation uint64) error {
		g.mu.Lock()
		g.active = nil
		client := g.host
		g.mu.Unlock()
		if client == nil {
			return errors.New("HOST_OFFLINE")
		}
		return client.Idle(generation)
	})
	return g
}
func (g *Gateway) clientIP(r *http.Request) (string, error) {
	endpoint, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return "", errors.New("REMOTE_INVALID")
	}
	source := endpoint.Addr().Unmap()
	for _, prefix := range g.trustedProxies {
		if prefix.Contains(source) {
			value := r.Header.Get("CF-Connecting-IP")
			if value == "" {
				break
			}
			client, err := netip.ParseAddr(value)
			if err != nil {
				return "", errors.New("PROXY_IP_INVALID")
			}
			return client.Unmap().String(), nil
		}
	}
	return source.String(), nil
}
func (g *Gateway) SetHost(client *host.Client, archive *asar.Archive) error {
	source, err := archive.Read("index.html")
	if err != nil {
		return err
	}
	rewritten, err := RewriteHTML(source, g.Config.PublicOrigin)
	if err != nil {
		return err
	}
	g.mu.Lock()
	g.host = client
	g.archive = archive
	g.html = rewritten
	g.mu.Unlock()
	client.Handlers(g.forward, func() {
		snapshot := client.Snapshot()
		code, _ := snapshot["errorCode"].(string)
		allowed := []string{"HOST_PROTOCOL_FAILED", "HOST_TRANSFER_FAILED", "QUEUE_LIMIT", "STALE_GENERATION", "PORT_UNKNOWN", "METHOD_DENIED", "FRAME_LIMIT", "UNSUPPORTED_VALUE", "UNSUPPORTED_TRANSFERABLE", "CHANNEL_UNKNOWN", "HOST_NOT_READY", "GENERATION_INVALID", "PORT_LIMIT_OR_TYPE", "WINDOW_DESTROYED", "DEBUGGER_DETACHED", "RENDERER_GONE", "IPC_SOCKET_ERROR", "FETCH_FAILED", "SHIM_INITIALIZATION_FAILED", "IPC_READ_FAILED", "IPC_CONTROL_INVALID", "IPC_CONTROL_UNKNOWN"}
		if !slices.Contains(allowed, code) && code != "IPC_TIMEOUT" && !strings.HasPrefix(code, "IPC_CLOSE_") {
			code = "HOST_OFFLINE"
		}
		g.Log.Add(logger.Event{Event: "host_offline", ErrorCode: code})
		g.mu.Lock()
		active := g.active
		g.active = nil
		g.mu.Unlock()
		if active != nil {
			active.close(protocol.HostUnavailable)
		}
	})
	return nil
}
func (g *Gateway) ClearHost() {
	g.mu.Lock()
	active := g.active
	g.active = nil
	g.host = nil
	g.archive = nil
	g.html = nil
	g.mu.Unlock()
	if active != nil {
		active.close(protocol.HostUnavailable)
	}
}
func (g *Gateway) forward(data []byte) {
	g.mu.Lock()
	active := g.active
	g.mu.Unlock()
	if active == nil {
		return
	}
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.revoked {
		return
	}
	if active.bytes+len(data) > protocol.MaxQueue {
		go active.close(1009)
		return
	}
	active.bytes += len(data)
	select {
	case active.output <- data:
	default:
		active.bytes -= len(data)
		go active.close(1009)
	}
}
func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}
func (g *Gateway) Health() map[string]any {
	_, generation := g.Controllers.Status()
	g.mu.Lock()
	client, archive, hostStandby := g.host, g.archive, g.hostStandby
	g.mu.Unlock()
	standby := hostStandby != nil && hostStandby()
	result := map[string]any{"platform": runtime.GOOS, "appVersion": "", "assetBuildId": "", "shimConnected": false, "hiddenWindowAlive": false, "hostStandby": standby, "activeControllerGeneration": generation, "uptimeSec": int(time.Since(g.started).Seconds()), "compatStatus": "host_offline"}
	if runtime.GOOS == "windows" {
		result["compatStatus"] = host.WindowsStatus
	}
	if client == nil && standby {
		result["compatStatus"] = "standby"
	}
	if client != nil && archive != nil {
		result["appVersion"] = client.Endpoint.AppVersion
		snapshot := client.Snapshot()
		result["assetBuildId"] = archive.BuildID
		result["shimConnected"] = snapshot["ready"] == true
		result["hiddenWindowAlive"] = snapshot["hiddenWindowAlive"]
		result["compatStatus"] = "macos_local_verified"
		if runtime.GOOS == "windows" {
			result["compatStatus"] = host.WindowsStatus
		}
		if archive.Consistent(client.Endpoint.AssetBuildID) != nil {
			result["shimConnected"] = false
			result["compatStatus"] = "asset_build_changed"
		}
	}
	g.mu.Lock()
	active := g.active
	g.mu.Unlock()
	if active != nil {
		result["controllerDevice"] = active.device
		result["controllerConnectedSec"] = int(time.Since(active.started).Seconds())
	}
	return result
}
func (g *Gateway) session(r *http.Request) *auth.Session {
	if g.Config.Auth.Mode == "password" {
		return g.Auth.FromRequest(r)
	}
	raw := r.Header.Get("Cf-Access-Jwt-Assertion")
	expiry, err := g.Access.Validate(raw)
	if err != nil {
		return nil
	}
	return g.Auth.AccessSession(raw, expiry)
}
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "origin")
	if !slices.Contains(g.Config.AllowedHosts, r.Host) {
		http.Error(w, "HOST_REJECTED", 403)
		return
	}
	stateChange := r.Method != "GET" && r.Method != "HEAD"
	if (stateChange || websocket.IsWebSocketUpgrade(r)) && r.Header.Get("Origin") != g.Config.PublicOrigin {
		http.Error(w, "ORIGIN_REJECTED", 403)
		return
	}
	if r.URL.Path == "/livez" && r.Method == "GET" {
		JSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if r.URL.Path == "/auth/login" && r.Method == "GET" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		w.Write(embedassets.Read("login.html"))
		return
	}
	if r.URL.Path == "/auth/login.js" && r.Method == "GET" {
		w.Header().Set("Content-Type", "text/javascript")
		w.Write(embedassets.Read("login.js"))
		return
	}
	if r.URL.Path == "/auth/login.css" && r.Method == "GET" {
		w.Header().Set("Content-Type", "text/css")
		w.Write(embedassets.Read("login.css"))
		return
	}
	if r.URL.Path == "/auth/csrf" && r.Method == "GET" {
		csrf := g.Auth.Begin(w)
		if csrf == "" {
			http.Error(w, "RATE_LIMITED", 429)
			return
		}
		JSON(w, 200, map[string]string{"csrf": csrf})
		return
	}
	if r.URL.Path == "/auth/login" && r.Method == "POST" {
		if g.Config.Auth.Mode != "password" || !g.Auth.CheckPre(r, r.Header.Get("X-CSRF-Token")) {
			http.Error(w, "CSRF_REJECTED", 403)
			return
		}
		var body struct {
			Password string `json:"password"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil {
			http.Error(w, "BODY_INVALID", 400)
			return
		}
		ip, err := g.clientIP(r)
		if err != nil {
			http.Error(w, "REMOTE_INVALID", 400)
			return
		}
		session, err := g.Auth.Login(ip, body.Password)
		body.Password = ""
		if err != nil {
			code := 500
			if err.Error() == "LOGIN_REJECTED" {
				code = 401
			} else if err.Error() == "RATE_LIMITED" || err.Error() == "SESSION_LIMIT" {
				code = 429
			}
			http.Error(w, "LOGIN_REJECTED", code)
			return
		}
		if session == nil {
			http.Error(w, "SESSION_LIMIT", 429)
			return
		}
		http.SetCookie(w, g.Auth.Cookie(session.RawToken, auth.PermanentCookieMaxAge))
		JSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if r.URL.Path == "/auth/ticket" && r.Method == "POST" {
		if g.Config.Auth.Mode != "password" || !g.Auth.CheckPre(r, r.Header.Get("X-CSRF-Token")) {
			http.Error(w, "CSRF_REJECTED", 403)
			return
		}
		var body struct {
			Ticket string `json:"ticket"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil {
			http.Error(w, "BODY_INVALID", 400)
			return
		}
		ip, err := g.clientIP(r)
		if err != nil {
			http.Error(w, "REMOTE_INVALID", 400)
			return
		}
		session, err := g.Auth.RedeemTicket(ip, body.Ticket)
		body.Ticket = ""
		if err != nil {
			code := 500
			if err.Error() == "TICKET_REJECTED" {
				code = 401
			} else if err.Error() == "RATE_LIMITED" || err.Error() == "SESSION_LIMIT" {
				code = 429
			}
			http.Error(w, "TICKET_REJECTED", code)
			return
		}
		if session == nil {
			http.Error(w, "SESSION_LIMIT", 429)
			return
		}
		http.SetCookie(w, g.Auth.Cookie(session.RawToken, auth.PermanentCookieMaxAge))
		JSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if slices.Contains([]string{"/livez", "/auth/login", "/auth/login.js", "/auth/login.css", "/auth/csrf", "/auth/ticket"}, r.URL.Path) {
		http.Error(w, "METHOD_REJECTED", 405)
		return
	}
	session := g.session(r)
	if session == nil {
		if r.Method == "GET" && (r.URL.Path == "/" || r.URL.Path == "/index.html") {
			http.Redirect(w, r, "/auth/login", 302)
		} else {
			http.Error(w, "AUTH_REQUIRED", 401)
		}
		return
	}
	if stateChange && !auth.Equal(session.CSRF, r.Header.Get("X-CSRF-Token")) {
		http.Error(w, "CSRF_REJECTED", 403)
		return
	}
	if g.Config.Auth.Mode == "password" && session.Permanent && r.Method == "GET" && (r.URL.Path == "/" || r.URL.Path == "/api/session") {
		cookie, err := r.Cookie(g.Auth.CookieName())
		if err != nil {
			http.Error(w, "AUTH_REQUIRED", 401)
			return
		}
		http.SetCookie(w, g.Auth.Cookie(cookie.Value, auth.PermanentCookieMaxAge))
	}
	switch {
	case r.URL.Path == "/auth/logout" && r.Method == "POST":
		if err := g.Auth.Revoke(session.ID); err != nil {
			http.Error(w, "AUTH_STATE_WRITE_FAILED", 500)
			return
		}
		http.SetCookie(w, g.Auth.Cookie("", -1))
		JSON(w, 200, map[string]bool{"ok": true})
	case r.URL.Path == "/healthz" && r.Method == "GET":
		JSON(w, 200, g.Health())
	case r.URL.Path == "/api/session" && r.Method == "GET":
		JSON(w, 200, session)
	case (r.URL.Path == "/api/controller" || r.URL.Path == "/api/takeover") && r.Method == "POST":
		g.acquire(w, r, session, r.URL.Path == "/api/takeover")
	case r.URL.Path == "/bridge" && r.Method == "GET":
		g.websocket(w, r, session)
	case strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/admin"):
		http.NotFound(w, r)
	case r.Method == "GET" || r.Method == "HEAD":
		g.static(w, r, session)
	default:
		http.Error(w, "METHOD_REJECTED", 405)
	}
}
func (g *Gateway) acquire(w http.ResponseWriter, r *http.Request, session *auth.Session, takeover bool) {
	if err := g.wakeHost(r.Context()); err != nil {
		http.Error(w, "HOST_UNAVAILABLE", 503)
		return
	}
	g.mu.Lock()
	if len(g.claims) >= 1024 {
		g.mu.Unlock()
		http.Error(w, "CLAIM_LIMIT", 429)
		return
	}
	client, archive := g.host, g.archive
	g.mu.Unlock()
	if client == nil || archive == nil || archive.Consistent(client.Endpoint.AssetBuildID) != nil {
		http.Error(w, "HOST_UNAVAILABLE", 503)
		return
	}
	device := "浏览器"
	agent := r.UserAgent()
	if strings.Contains(agent, "iPhone") {
		device = "iPhone"
	} else if strings.Contains(agent, "Android") {
		device = "Android"
	} else if strings.Contains(agent, "Macintosh") {
		device = "macOS"
	} else if strings.Contains(agent, "Windows") {
		device = "Windows"
	}
	item := &claim{id: auth.Random(), session: session.ID, device: device, started: time.Now(), log: g.Log}
	var request struct {
		Resume string `json:"resume"`
	}
	if !takeover && r.Body != nil {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request); err != nil {
			http.Error(w, "BODY_INVALID", 400)
			return
		}
	}
	var generation uint64
	var err error
	if request.Resume != "" {
		g.mu.Lock()
		previous := g.claims[request.Resume]
		g.mu.Unlock()
		if previous != nil && previous.session != session.ID {
			http.Error(w, "RESUME_REJECTED", 403)
			return
		}
		if previous != nil {
			generation, err = g.Controllers.Resume(request.Resume, item.id, item.close)
		} else {
			generation, err = g.Controllers.Acquire(item.id, false, item.close)
		}
	} else {
		generation, err = g.Controllers.Acquire(item.id, takeover, item.close)
	}
	if err != nil {
		status := 503
		if err.Error() == "CONTROLLER_OCCUPIED" {
			status = 409
		}
		http.Error(w, "CONTROLLER_UNAVAILABLE", status)
		return
	}
	item.generation = generation
	g.mu.Lock()
	g.claims[item.id] = item
	g.mu.Unlock()
	item.timer = time.AfterFunc(20*time.Second, func() {
		item.mu.Lock()
		unbound := item.socket == nil
		item.mu.Unlock()
		if unbound {
			if err := g.Controllers.Release(item.id, item.generation); err != nil {
				g.Log.Add(logger.Event{Event: "host_idle_failed", ErrorCode: "HOST_IDLE_FAILED", Generation: item.generation})
			}
			g.touchHostActivity()
			g.mu.Lock()
			delete(g.claims, item.id)
			g.mu.Unlock()
		}
	})
	JSON(w, 200, map[string]any{"id": item.id, "generation": generation})
}
func (g *Gateway) websocket(w http.ResponseWriter, r *http.Request, session *auth.Session) {
	var id string
	for _, part := range websocket.Subprotocols(r) {
		if strings.HasPrefix(part, "claim.") {
			id = strings.TrimPrefix(part, "claim.")
		}
	}
	g.mu.Lock()
	item := g.claims[id]
	client := g.host
	g.mu.Unlock()
	if item == nil || client == nil || item.session != session.ID {
		http.Error(w, "CLAIM_REQUIRED", 403)
		return
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == g.Config.PublicOrigin }, Subprotocols: []string{"cwb.v1"}, HandshakeTimeout: 5 * time.Second}
	var connection *websocket.Conn
	err := g.Controllers.With(item.id, item.generation, func() error {
		item.mu.Lock()
		defer item.mu.Unlock()
		if item.revoked || item.socket != nil {
			return errors.New("CLAIM_USED")
		}
		var err error
		connection, err = upgrader.Upgrade(w, r, nil)
		if err != nil {
			return err
		}
		item.socket = connection
		item.output = make(chan []byte, protocol.MaxQueuedFrames)
		return nil
	})
	if err != nil {
		return
	}
	g.touchHostActivity()
	item.timer.Stop()
	defer func() {
		connection.Close()
		g.Auth.Unregister(session.ID, id)
		if err := g.Controllers.Release(id, item.generation); err != nil {
			g.Log.Add(logger.Event{Event: "host_idle_failed", ErrorCode: "HOST_IDLE_FAILED", Generation: item.generation})
		}
		g.touchHostActivity()
		g.mu.Lock()
		delete(g.claims, id)
		if g.active == item {
			g.active = nil
		}
		g.mu.Unlock()
	}()
	connection.SetReadLimit(protocol.MaxFrame)
	connection.SetReadDeadline(time.Now().Add(60 * time.Second))
	connection.SetPongHandler(func(string) error {
		g.touchHostActivity()
		return connection.SetReadDeadline(time.Now().Add(60 * time.Second))
	})
	connection.SetPingHandler(func(data string) error {
		g.touchHostActivity()
		return connection.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(5*time.Second))
	})
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				g.touchHostActivity()
				if err := connection.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					code := "WS_PING_FAILED"
					var network net.Error
					if errors.As(err, &network) && network.Timeout() {
						code = "WS_PING_TIMEOUT"
					}
					g.Log.Add(logger.Event{Event: "browser_ws_closed", ErrorCode: code})
					connection.Close()
					return
				}
			case data := <-item.output:
				item.mu.Lock()
				item.bytes -= len(data)
				valid := !item.revoked
				item.mu.Unlock()
				if !valid {
					return
				}
				begin := time.Now()
				err := item.write(websocket.BinaryMessage, data)
				if err != nil {
					code := "WS_DATA_WRITE_FAILED"
					var network net.Error
					if errors.As(err, &network) && network.Timeout() {
						code = "WS_DATA_WRITE_TIMEOUT"
					}
					g.Log.Add(logger.Event{Event: "browser_ws_closed", ErrorCode: code, DurationMS: time.Since(begin).Milliseconds()})
					connection.Close()
					return
				}
			}
		}
	}()
	activateErr := g.Controllers.With(id, item.generation, func() error {
		if !g.Auth.Register(session.ID, id, func() { item.close(4001) }) {
			item.close(4001)
			return errors.New("SESSION_ENDED")
		}
		g.mu.Lock()
		g.active = item
		g.mu.Unlock()
		activateCtx, activateCancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer activateCancel()
		_, err := client.Call(activateCtx, map[string]any{"kind": "activate", "generation": item.generation})
		return err
	})
	if activateErr != nil {
		g.Log.Add(logger.Event{Event: "browser_ws_closed", ErrorCode: "WS_ACTIVATE_FAILED"})
		return
	}
	for {
		kind, data, err := connection.ReadMessage()
		if err != nil {
			code := "WS_READ_FAILED"
			var network net.Error
			var closed *websocket.CloseError
			var errno syscall.Errno
			if errors.Is(err, net.ErrClosed) {
				code = "WS_LOCAL_CLOSED"
			} else if errors.As(err, &network) {
				code = "WS_NETWORK_FAILED"
				if network.Timeout() {
					code = "WS_READ_TIMEOUT"
				}
			} else if errors.As(err, &closed) {
				code = "WS_PEER_CLOSED"
			} else if errors.Is(err, websocket.ErrReadLimit) {
				code = "WS_FRAME_LIMIT"
			} else if strings.HasPrefix(err.Error(), "websocket: ") {
				code = "WS_PROTOCOL_FRAME_INVALID"
			}
			status := 0
			if errors.As(err, &errno) {
				status = int(errno)
			}
			g.Log.Add(logger.Event{Event: "browser_ws_closed", ErrorCode: code, Status: status, Generation: item.generation})
			return
		}
		g.touchHostActivity()
		if kind == websocket.TextMessage {
			var control protocol.Envelope
			if json.Unmarshal(data, &control) != nil || control.Kind != "probe" {
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			_, err = client.Call(ctx, map[string]any{"kind": "probe"})
			cancel()
			if err != nil {
				g.Log.Add(logger.Event{Event: "browser_ws_closed", ErrorCode: "HOST_PROBE_FAILED"})
				return
			}
			err = item.write(websocket.TextMessage, []byte(`{"kind":"pong"}`))
			if err != nil {
				g.Log.Add(logger.Event{Event: "browser_ws_closed", ErrorCode: "WS_PROBE_WRITE_FAILED"})
				return
			}
			continue
		}
		if kind != websocket.BinaryMessage {
			return
		}
		if err = g.Controllers.With(id, item.generation, func() error { return client.Write(websocket.BinaryMessage, data) }); err != nil {
			return
		}
	}
}
func (g *Gateway) static(w http.ResponseWriter, r *http.Request, session *auth.Session) {
	if err := g.wakeHost(r.Context()); err != nil {
		http.Error(w, "HOST_UNAVAILABLE", 503)
		return
	}
	g.mu.Lock()
	client, archive, html := g.host, g.archive, g.html
	g.mu.Unlock()
	if client == nil || archive == nil || archive.Consistent(client.Endpoint.AssetBuildID) != nil {
		http.Error(w, "HOST_UNAVAILABLE", 503)
		return
	}
	if r.URL.Path == "/cwb/configuration.js" {
		snapshot := client.Snapshot()
		configuration, ok := snapshot["configuration"].(map[string]any)
		if !ok {
			http.Error(w, "HOST_NOT_READY", 503)
			return
		}
		copy := map[string]any{}
		for key, value := range configuration {
			copy[key] = value
		}
		copy["csrf"] = session.CSRF
		copy["assetBuildId"] = archive.BuildID
		data, err := json.Marshal(copy)
		if err != nil {
			http.Error(w, "CONFIGURATION_INVALID", 500)
			return
		}
		w.Header().Set("Content-Type", "text/javascript")
		fmt.Fprintf(w, "window.__cwbConfiguration=%s;", data)
		return
	}
	if r.URL.Path == "/cwb/browser.js" {
		w.Header().Set("Content-Type", "text/javascript")
		w.Write(embedassets.Read("browser.js"))
		return
	}
	if r.URL.Path == "/cwb/mobile.css" {
		w.Header().Set("Content-Type", "text/css")
		w.Write(embedassets.Read("mobile.css"))
		return
	}
	if r.URL.Path == "/" || r.URL.Path == "/index.html" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(html)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	data, err := archive.Read(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data, err = rewriteBrowserAsset(name, data)
	if err != nil {
		http.Error(w, "BROWSER_ATTACHMENT_ASSET_UNSUPPORTED", 503)
		return
	}
	w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(name)))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if name == browserEntryAsset || name == browserComposerAsset {
		w.Header().Set("Cache-Control", "private, no-cache")
	}
	w.Write(data)
}

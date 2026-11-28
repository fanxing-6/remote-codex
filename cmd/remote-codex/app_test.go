package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"remote-codex/internal/auth"
	"remote-codex/internal/config"
	"remote-codex/internal/gateway"
	"remote-codex/internal/host"
	"remote-codex/internal/logger"
	"remote-codex/internal/tunnel"
)

func standbyApplication(t *testing.T) *application {
	t.Helper()
	c := config.Default()
	c.TunnelMode = "custom"
	c.PublicOrigin = "https://chat.example.com"
	c.AllowedHosts = []string{"chat.example.com"}
	store := auth.New(false, nil)
	t.Cleanup(store.Close)
	a := &application{ctx: context.Background(), config: c, publicAuth: store, log: logger.New(io.Discard),
		provider: tunnel.NewProvider(c, "", ""), adapter: host.New(c, "", "./unavailable-standby-host"), hostIdleTimeout: 5 * time.Minute}
	a.enabled.Store(true)
	g := gateway.New(c, store, a.log, 0)
	g.SetHostLifecycle(a.ensureHost, a.touchHostActivity, a.isHostStandby)
	a.gateway.Store(g)
	return a
}

func TestHostSupervisorStandby(t *testing.T) {
	a := standbyApplication(t)
	ctx, cancel := context.WithCancel(a.ctx)
	done := make(chan struct{})
	go func() { defer close(done); a.superviseHost(ctx) }()
	for attempt := 0; attempt < 4; attempt++ {
		a.superviseHostOnce(ctx)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not stop")
	}
	if !a.isHostStandby() || !a.lastHostActivity.IsZero() || len(a.log.Events()) != 0 {
		t.Fatal("standby must leave the host untouched")
	}
	status := a.status().(map[string]any)
	if status["hostMode"] != "on_demand" || status["hostStandby"] != true || status["hostIdleTimeoutSec"] != 300 {
		t.Fatal(status)
	}
}

func TestEnsureHostDisabledAndCanceled(t *testing.T) {
	for _, mode := range []string{"disabled", "manual", "stopping", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			a := standbyApplication(t)
			ctx := a.ctx
			want := "HOST_DISABLED"
			switch mode {
			case "disabled":
				a.enabled.Store(false)
			case "manual":
				a.manualHost = true
			case "stopping":
				a.stopping.Store(true)
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled.Error()
			}
			if err := a.ensureHost(ctx); err == nil || err.Error() != want {
				t.Fatalf("ensureHost = %v, want %s", err, want)
			}
			if !a.lastHostActivity.IsZero() || a.client != nil {
				t.Fatal("rejected demand changed host state")
			}
			if mode != "canceled" && a.isHostStandby() {
				t.Fatal("disabled host reported standby")
			}
		})
	}
}

func TestGatewayLifecycleSurvivesRebuild(t *testing.T) {
	a := standbyApplication(t)
	g, err := a.prepareGateway(a.config)
	if err != nil {
		t.Fatal(err)
	}
	a.gateway.Store(g)
	session := a.publicAuth.CookieSession(time.Now().Add(time.Hour))
	request := httptest.NewRequest("GET", a.config.PublicOrigin+"/healthz", nil)
	request.AddCookie(a.publicAuth.Cookie(session.RawToken, 3600))
	response := httptest.NewRecorder()
	g.ServeHTTP(response, request)
	var health map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || health["hostStandby"] != true || health["compatStatus"] != "standby" {
		t.Fatal(response.Code, health)
	}
	ctx, cancel := context.WithCancel(a.ctx)
	cancel()
	request = httptest.NewRequest("GET", a.config.PublicOrigin+"/", nil).WithContext(ctx)
	request.AddCookie(a.publicAuth.Cookie(session.RawToken, 3600))
	response = httptest.NewRecorder()
	g.ServeHTTP(response, request)
	if response.Code != 503 {
		t.Fatal("canceled wake must fail without attaching", response.Code)
	}
}

// 集成检查仅连接项目目录内明确配置的独立测试宿主。
func isolatedHostApplication(t *testing.T) *application {
	t.Helper()
	command := os.Getenv("REMOTE_CODEX_ISOLATED_HOST_COMMAND")
	if command == "" {
		t.Skip("set REMOTE_CODEX_ISOLATED_HOST_COMMAND to a real isolated host inside the project")
	}
	a := standbyApplication(t)
	a.runtimeDir = t.TempDir()
	a.adapter = host.New(a.config, a.runtimeDir, command)
	identity, err := a.adapter.FindRunningProcess(a.ctx)
	if err != nil {
		t.Fatal(err)
	}
	project, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{identity.ExecutablePath, identity.AsarPath} {
		relative, err := filepath.Rel(project, filename)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatal("isolated host and archive must be inside the project")
		}
	}
	t.Cleanup(func() {
		if err := a.stop(); err != nil {
			t.Error(err)
		}
	})
	return a
}

func TestAuthenticatedDemandSingleFlightIntegration(t *testing.T) {
	a := isolatedHostApplication(t)
	session := a.publicAuth.CookieSession(time.Now().Add(time.Hour))
	var workers sync.WaitGroup
	start := make(chan struct{})
	for index := 0; index < 16; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			request := httptest.NewRequest("GET", a.config.PublicOrigin+"/", nil)
			request.AddCookie(a.publicAuth.Cookie(session.RawToken, 3600))
			response := httptest.NewRecorder()
			a.gateway.Load().ServeHTTP(response, request)
			if response.Code != 200 {
				t.Errorf("authenticated demand = %d", response.Code)
			}
		}()
	}
	close(start)
	workers.Wait()
	connected := 0
	for _, event := range a.log.Events() {
		if event.Event == "host_connected" {
			connected++
		}
	}
	if connected != 1 || a.isHostStandby() || a.lastHostActivity.IsZero() {
		t.Fatal("concurrent demand must attach exactly once", connected)
	}
}

func TestHostIdleDetachIntegration(t *testing.T) {
	a := isolatedHostApplication(t)
	if err := a.ensureHost(a.ctx); err != nil {
		t.Fatal(err)
	}
	g := a.gateway.Load()
	owner := auth.Random()
	generation, err := g.Controllers.Acquire(owner, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.hostIdleTimeout = time.Millisecond
	a.lastHostActivity = time.Now().Add(-time.Hour)
	a.superviseHostOnce(a.ctx)
	if a.client == nil || time.Since(a.lastHostActivity) >= time.Second {
		t.Fatal("active controller must preserve the host and refresh activity")
	}
	if err := g.Controllers.Release(owner, generation); err != nil {
		t.Fatal(err)
	}
	a.lastHostActivity = time.Now().Add(-time.Hour)
	a.superviseHostOnce(a.ctx)
	if !a.isHostStandby() || g.HasActiveClients() {
		t.Fatal("idle host did not return to standby")
	}
	for attempt := 0; attempt < 3; attempt++ {
		a.superviseHostOnce(a.ctx)
	}
	connected, detached := 0, 0
	for _, event := range a.log.Events() {
		switch event.Event {
		case "host_connected":
			connected++
		case "host_idle_detached":
			detached++
		}
	}
	if connected != 1 || detached != 1 {
		t.Fatal("standby must remain silent", connected, detached)
	}
}

func TestClaimAndWebsocketActivityIntegration(t *testing.T) {
	a := isolatedHostApplication(t)
	session := a.publicAuth.CookieSession(time.Now().Add(time.Hour))
	g := a.gateway.Load()
	request := httptest.NewRequest("POST", a.config.PublicOrigin+"/api/controller", strings.NewReader("{}"))
	request.AddCookie(a.publicAuth.Cookie(session.RawToken, 3600))
	request.Header.Set("Origin", a.config.PublicOrigin)
	request.Header.Set("X-CSRF-Token", session.CSRF)
	response := httptest.NewRecorder()
	g.ServeHTTP(response, request)
	var reservation struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &reservation); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || !g.HasActiveClients() {
		t.Fatal("authenticated acquisition must wake the host and reserve control", response.Code)
	}
	a.hostIdleTimeout = time.Millisecond
	a.lastHostActivity = time.Now().Add(-time.Hour)
	a.superviseHostOnce(a.ctx)
	if a.client == nil {
		t.Fatal("unbound claim must preserve the host")
	}
	server := httptest.NewServer(g)
	t.Cleanup(server.Close)
	header := http.Header{"Origin": {a.config.PublicOrigin}, "Host": {"chat.example.com"}, "Cookie": {request.Header.Get("Cookie")}}
	dialer := websocket.Dialer{Subprotocols: []string{"cwb.v1", "claim." + reservation.ID}}
	socket, _, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/bridge", header)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { socket.Close() })
	beforeProbe := time.Now()
	if err := socket.WriteJSON(map[string]string{"kind": "probe"}); err != nil {
		t.Fatal(err)
	}
	if err := socket.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var pong map[string]string
	if err := socket.ReadJSON(&pong); err != nil || pong["kind"] != "pong" {
		t.Fatal("probe failed", pong, err)
	}
	a.mu.Lock()
	activity := a.lastHostActivity
	a.mu.Unlock()
	if activity.Before(beforeProbe) {
		t.Fatal("websocket probe must refresh host activity")
	}
	beforeDisconnect := time.Now()
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for g.HasActiveClients() {
		if time.Now().After(deadline) {
			t.Fatal("disconnected websocket retained controller or claim")
		}
		time.Sleep(time.Millisecond)
	}
	a.mu.Lock()
	activity = a.lastHostActivity
	a.mu.Unlock()
	if activity.Before(beforeDisconnect) {
		t.Fatal("idle countdown must begin at controller release")
	}
	a.lastHostActivity = time.Now().Add(-time.Hour)
	a.superviseHostOnce(a.ctx)
	if !a.isHostStandby() {
		t.Fatal("released controller and claim must permit idle detach")
	}
}

func TestUnhealthyHostWithoutClientsIntegration(t *testing.T) {
	a := isolatedHostApplication(t)
	if err := a.ensureHost(a.ctx); err != nil {
		t.Fatal(err)
	}
	// 释放独立测试宿主的窗口和 IPC，使监督检查检测到连接中断。
	if err := a.adapter.Detach(a.ctx, a.client); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		a.superviseHostOnce(a.ctx)
	}
	if !a.isHostStandby() {
		t.Fatal("unhealthy host must return to standby")
	}
	connected, failed := 0, 0
	for _, event := range a.log.Events() {
		if event.Event == "host_connected" {
			connected++
		}
		if event.Event == "host_probe_failed" {
			failed++
		}
	}
	if connected != 1 || failed != 1 {
		t.Fatal("unhealthy idle host must not reconnect", connected, failed)
	}
}

func TestHostProbeConsecutiveFailures(t *testing.T) {
	a := &application{}
	for failure := 1; failure <= 3; failure++ {
		if reconnect := a.shouldReconnectHost(context.DeadlineExceeded, nil, false); reconnect != (failure == 3) {
			t.Fatalf("failure %d: reconnect = %v", failure, reconnect)
		}
	}
	if a.shouldReconnectHost(nil, nil, false) || a.hostProbeFailures != 0 {
		t.Fatal("successful probe must reset consecutive failures")
	}
	for attempt := 0; attempt < 3; attempt++ {
		if a.shouldReconnectHost(context.DeadlineExceeded, nil, false) {
			t.Fatal("isolated timeout must preserve the host")
		}
		if a.shouldReconnectHost(nil, nil, false) || a.hostProbeFailures != 0 {
			t.Fatal("successful probe must reset consecutive failures")
		}
	}
}

func TestHostProbeImmediateReconnect(t *testing.T) {
	for _, offline := range []bool{false, true} {
		t.Run(map[bool]string{false: "archive_changed", true: "host_offline"}[offline], func(t *testing.T) {
			a := &application{}
			if a.shouldReconnectHost(context.DeadlineExceeded, nil, false) {
				t.Fatal("first timeout must preserve the host")
			}
			var archiveErr error
			if !offline {
				archiveErr = os.ErrNotExist
			}
			if !a.shouldReconnectHost(nil, archiveErr, offline) || a.hostProbeFailures != 0 {
				t.Fatal("unavailable host must reconnect and reset consecutive failures")
			}
			if a.shouldReconnectHost(context.DeadlineExceeded, nil, false) {
				t.Fatal("new host must receive the full timeout tolerance")
			}
		})
	}
}

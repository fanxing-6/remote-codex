package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"remote-codex/internal/admin"
	"remote-codex/internal/asar"
	"remote-codex/internal/auth"
	"remote-codex/internal/config"
	"remote-codex/internal/gateway"
	"remote-codex/internal/host"
	"remote-codex/internal/logger"
	"remote-codex/internal/tunnel"
)

type application struct {
	mu                                 sync.Mutex
	operation                          sync.Mutex
	enabled                            atomic.Bool
	stopping                           atomic.Bool
	config                             config.Config
	configPath, runtimeDir, tunnelPath string
	options                            serveOptions
	adapter                            host.IHostAdapter
	client                             *host.Client
	archive                            *asar.Archive
	publicAuth                         *auth.Store
	log                                *logger.Logger
	gateway                            atomic.Pointer[gateway.Gateway]
	admin                              *admin.Admin
	reservePorts                       func(config.Config, config.Config) (func() error, func(), error)
	ctx                                context.Context
	provider                           tunnel.Provider
	manualHost                         bool
	hostIdleTimeout                    time.Duration
	lastHostActivity                   time.Time
	hostProbeFailures                  int
}

func (a *application) ensureHost(ctx context.Context) error {
	a.operation.Lock()
	defer a.operation.Unlock()
	if a.stopping.Load() || !a.enabled.Load() || a.manualHost {
		return errors.New("HOST_DISABLED")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	connected := a.client != nil
	if connected {
		a.lastHostActivity = time.Now()
	}
	a.mu.Unlock()
	if connected {
		return nil
	}
	return a.startLocked()
}

func (a *application) touchHostActivity() {
	a.mu.Lock()
	a.lastHostActivity = time.Now()
	a.mu.Unlock()
}

func (a *application) isHostStandby() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.enabled.Load() && !a.manualHost && !a.stopping.Load() && a.client == nil
}

func (a *application) start() error {
	a.operation.Lock()
	defer a.operation.Unlock()
	if a.stopping.Load() || !a.enabled.Load() {
		return errors.New("SERVICE_STOPPING")
	}
	return a.startLocked()
}
func (a *application) startLocked() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client != nil {
		a.lastHostActivity = time.Now()
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, 80*time.Second)
	defer cancel()
	identity, err := a.adapter.FindRunningProcess(ctx)
	if err != nil {
		return err
	}
	archive, err := asar.Open(identity.AsarPath)
	if err != nil {
		return err
	}
	client, err := a.adapter.Attach(ctx, identity, archive.BuildID, filepath.Join(a.runtimeDir, "shim.cjs"))
	if err != nil {
		archive.Close()
		return err
	}
	if err = a.gateway.Load().SetHost(client, archive); err != nil {
		a.adapter.Detach(ctx, client)
		archive.Close()
		return err
	}
	a.client, a.archive = client, archive
	a.lastHostActivity = time.Now()
	a.hostProbeFailures = 0
	a.log.Add(logger.Event{Event: "host_connected", DurationMS: int64(client.Endpoint.BootstrapElapsedMS)})
	return nil
}
func (a *application) stop() error {
	a.operation.Lock()
	defer a.operation.Unlock()
	return a.stopLocked()
}
func (a *application) stopLocked() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client == nil {
		return nil
	}
	a.gateway.Load().ClearHost()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := a.adapter.Detach(ctx, a.client)
	a.client = nil
	a.archive.Close()
	a.archive = nil
	a.log.Add(logger.Event{Event: "host_stopped"})
	return err
}
func (a *application) reconnect() error {
	a.operation.Lock()
	defer a.operation.Unlock()
	if a.stopping.Load() {
		return errors.New("SERVICE_STOPPING")
	}
	a.enabled.Store(true)
	if err := a.stopLocked(); err != nil {
		return err
	}
	return a.startLocked()
}
func (a *application) status() any {
	result := a.gateway.Load().Health()
	owner, _ := a.gateway.Load().Controllers.Status()
	result["controllerActive"], result["sessions"] = owner != "", a.publicAuth.Count()
	result["hostStandby"] = a.isHostStandby()
	a.mu.Lock()
	defer a.mu.Unlock()
	result["hostMode"] = "on_demand"
	if a.manualHost {
		result["hostMode"] = "manual"
	}
	result["hostIdleTimeoutSec"] = int(a.hostIdleTimeout.Seconds())
	s := a.provider.Status()
	result["tunnelMode"], result["publicOrigin"] = s.Mode, a.config.PublicOrigin
	result["tunnel"] = s
	result["quickTunnelState"], result["quickTunnelError"], result["quickTunnelPID"] = s.State, s.Error, s.PID
	result["cloudflaredFound"] = s.CloudflaredFound
	result["bootstrapper"] = a.adapter.BootstrapperName()
	result["pid"] = os.Getpid()
	if a.client != nil {
		result["inspectorOpenMs"], result["inspectorClosed"] = a.client.Endpoint.InspectorOpenMS, a.client.Endpoint.InspectorClosed
	}
	return result
}
func (a *application) doctor() any {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	a.mu.Lock()
	adapter, c, client, provider := a.adapter, a.config, a.client, a.provider
	a.mu.Unlock()
	identity, err := adapter.FindRunningProcess(ctx)
	checks := []any{
		map[string]any{"label": "ChatGPT 运行进程", "ok": err == nil, "detail": map[bool]string{true: "已唯一识别当前用户的主进程", false: "请启动 ChatGPT 并检查挂载器配置"}[err == nil]},
		map[string]any{"label": "宿主挂载器", "ok": err == nil, "detail": adapter.BootstrapperName()},
		map[string]any{"label": "应用兼容性", "ok": err == nil && host.VersionSupported(identity.AppVersion), "detail": host.CompatibilityLabel()},
		map[string]any{"label": "独立管理端口", "ok": c.AdminPort != c.DataPort, "detail": fmt.Sprintf("数据面 %d / 本地管理面 %d", c.DataPort, c.AdminPort)},
	}
	connection := client != nil && adapter.ProbeRuntime(ctx, client) == nil
	if a.manualHost && client == nil {
		checks = append(checks, map[string]any{"label": "宿主连接策略", "ok": true, "detail": "手动连接模式，尚未连接宿主"})
	} else if client == nil && a.isHostStandby() {
		checks = append(checks, map[string]any{"label": "宿主连接策略", "ok": true, "detail": "按需待机模式（无远程访问时不注入宿主，手机访问时自动唤醒，闲置 5 分钟自动释放）"})
	} else {
		checks = append(checks, map[string]any{"label": "隐藏宿主窗口与身份", "ok": connection, "detail": map[bool]string{true: "宿主进程身份与 Shim 身份一致", false: "点击连接宿主以建立桥接"}[connection]})
	}
	open := false
	if socket, err := net.DialTimeout("tcp", "127.0.0.1:9229", 300*time.Millisecond); err == nil {
		open = true
		socket.Close()
	}
	checks = append(checks, map[string]any{"label": "临时 Inspector 端口", "ok": !open, "detail": map[bool]string{true: "9229 已关闭", false: "9229 存在监听，请确认监听归属"}[!open]}, provider.DoctorCheck())
	if err == nil {
		checks = append(checks, platformSignature(ctx, identity))
	}
	return map[string]any{"checks": checks, "windowsStatus": host.WindowsStatus, "bootstrapper": adapter.BootstrapperName()}
}
func (a *application) save(c config.Config) error {
	a.operation.Lock()
	defer a.operation.Unlock()
	c = c.Normalized()
	if err := config.Validate(c); err != nil {
		return err
	}
	if a.stopping.Load() {
		return errors.New("SERVICE_STOPPING")
	}
	current := a.snapshot()
	commit, abort, err := a.reservePorts(current, c)
	if err != nil {
		return err
	}
	defer abort()
	a.mu.Lock()
	previous := a.provider
	a.mu.Unlock()
	next := previous
	if tunnel.SettingsFor(current) != tunnel.SettingsFor(c) {
		next = tunnel.NewProvider(c, a.runtimeDir, a.options.CloudflaredPath)
		defer func() {
			a.mu.Lock()
			active := a.provider == next
			a.mu.Unlock()
			if !active {
				next.Stop()
			}
		}()
	}
	origin, err := next.Start(a.ctx)
	if err != nil {
		return err
	}
	c = origin.Bind(c)
	g, err := a.prepareGateway(c)
	if err != nil {
		return err
	}
	if err = config.Save(a.configPath, c); err != nil {
		return err
	}
	a.mu.Lock()
	a.provider = next
	a.mu.Unlock()
	a.publish(c, g)
	if previous != next {
		previous.Stop()
	}
	a.publicAuth.SetInsecureLocalhost(c.AllowInsecureLocalhost && strings.HasPrefix(c.PublicOrigin, "http://127.0.0.1:"))
	return commit()
}
func (a *application) autoStart(enabled bool) error {
	if os.Getenv("REMOTE_CODEX_MANAGED_DESKTOP") == "1" {
		return errors.New("AUTOSTART_MANAGED_BY_DESKTOP")
	}
	return platformAutoStart(enabled, a.configPath, a.tunnelPath)
}

func (a *application) superviseHost(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		a.superviseHostOnce(ctx)
	}
}

func (a *application) superviseHostOnce(ctx context.Context) {
	a.operation.Lock()
	defer a.operation.Unlock()
	if !a.enabled.Load() || a.stopping.Load() || ctx.Err() != nil {
		return
	}
	a.mu.Lock()
	client, adapter, archive := a.client, a.adapter, a.archive
	a.mu.Unlock()
	if client == nil {
		return
	}
	activeClients := a.gateway.Load().HasActiveClients()
	if activeClients {
		a.touchHostActivity()
	}
	a.mu.Lock()
	idle := !activeClients && a.hostIdleTimeout > 0 && time.Since(a.lastHostActivity) >= a.hostIdleTimeout
	a.mu.Unlock()
	if idle {
		a.log.Add(logger.Event{Event: "host_idle_detached"})
		if err := a.stopLocked(); err != nil {
			a.log.Add(logger.Event{Event: "host_detach_failed", ErrorCode: "HOST_DETACH_FAILED"})
		}
		return
	}
	archiveErr := archive.Consistent(client.Endpoint.AssetBuildID)
	var probeErr error
	if archiveErr == nil && client.Snapshot()["ready"] != false {
		probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		probeErr = adapter.ProbeRuntime(probeCtx, client)
		cancel()
	}
	if a.shouldReconnectHost(probeErr, archiveErr, client.Snapshot()["ready"] == false) {
		a.log.Add(logger.Event{Event: "host_probe_failed", ErrorCode: "HOST_PROBE_FAILED"})
		if err := a.stopLocked(); err != nil {
			a.log.Add(logger.Event{Event: "host_detach_failed", ErrorCode: "HOST_DETACH_FAILED"})
		}
		if activeClients && a.enabled.Load() && !a.stopping.Load() {
			if a.startLocked() != nil {
				a.log.Add(logger.Event{Event: "host_attach_deferred", ErrorCode: "HOST_UNAVAILABLE"})
			}
		}
	}
}

func (a *application) shouldReconnectHost(probeErr, archiveErr error, offline bool) bool {
	if offline || archiveErr != nil {
		a.hostProbeFailures = 0
		return true
	}
	if probeErr == nil {
		a.hostProbeFailures = 0
		return false
	}
	a.hostProbeFailures++
	return a.hostProbeFailures >= 3
}

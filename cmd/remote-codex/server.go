package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"remote-codex/internal/admin"
	"remote-codex/internal/auth"
	"remote-codex/internal/config"
	"remote-codex/internal/gateway"
	"remote-codex/internal/host"
	"remote-codex/internal/logger"
	"remote-codex/internal/tray"
	"remote-codex/internal/tunnel"
)

type listeners struct {
	data, admin             net.Listener
	dataServer, adminServer *http.Server
	tls                     *tlsEndpoint
	fail                    func(error)
}

func listen(port int, code string) (net.Listener, error) {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, errors.New(code)
	}
	return l, nil
}
func (s *listeners) serve(server *http.Server, l net.Listener) {
	if err := server.Serve(l); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
		s.fail(err)
	}
}
func (s *listeners) serveTLS(endpoint *tlsEndpoint) {
	if endpoint == nil {
		return
	}
	if err := endpoint.server.ServeTLS(endpoint.listener, "", ""); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
		s.fail(err)
	}
}
func (s *listeners) shutdown(ctx context.Context) error {
	var failures []error
	if s.data != nil {
		s.data.Close()
	}
	if s.admin != nil {
		s.admin.Close()
	}
	if s.dataServer != nil {
		failures = append(failures, s.dataServer.Shutdown(ctx))
	}
	if s.adminServer != nil {
		failures = append(failures, s.adminServer.Shutdown(ctx))
	}
	return errors.Join(append(failures, s.tls.shutdown(ctx))...)
}
func (s *listeners) reserve(previous, next config.Config, configPath string) (func() error, func(), error) {
	var data, adminPort net.Listener
	var endpoint *tlsEndpoint
	var settings *tls.Config
	tlsChanged := previous.TLSPort != next.TLSPort || previous.TLSAutocert != next.TLSAutocert || previous.PublicOrigin != next.PublicOrigin
	abort := func() {
		if data != nil {
			data.Close()
		}
		if adminPort != nil {
			adminPort.Close()
		}
		endpoint.shutdown(context.Background())
	}
	var err error
	if next.DataPort != previous.DataPort {
		data, err = listen(next.DataPort, "DATA_PORT_UNAVAILABLE")
		if err != nil {
			return nil, nil, err
		}
	}
	if next.AdminPort != previous.AdminPort {
		adminPort, err = listen(next.AdminPort, "ADMIN_PORT_UNAVAILABLE")
		if err != nil {
			abort()
			return nil, nil, err
		}
	}
	if tlsChanged {
		settings, err = automaticTLSConfig(next, configPath)
		if err != nil {
			abort()
			return nil, nil, err
		}
		if settings != nil && (s.tls == nil || next.TLSPort != previous.TLSPort) {
			endpoint, err = newTLSEndpoint(next.TLSPort, settings, s.dataServer.Handler, s.dataServer.ErrorLog)
			if err != nil {
				abort()
				return nil, nil, err
			}
		}
	}
	commit := func() error {
		if data != nil {
			old := s.data
			s.data = data
			go s.serve(s.dataServer, data)
			data = nil
			old.Close()
		}
		if adminPort != nil {
			old := s.admin
			s.admin = adminPort
			go s.serve(s.adminServer, adminPort)
			adminPort = nil
			old.Close()
		}
		if tlsChanged {
			if s.tls != nil && settings != nil && previous.TLSPort == next.TLSPort {
				s.tls.settings.Store(settings)
			} else {
				old := s.tls
				s.tls = endpoint
				endpoint = nil
				go s.serveTLS(s.tls)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				return old.shutdown(ctx)
			}
		}
		return nil
	}
	return commit, abort, nil
}

func serve(c config.Config, configPath, runtimeDir, tunnelPath string, options serveOptions) (result error) {
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	errorsChannel := make(chan error, 1)
	s := &listeners{fail: func(err error) {
		select {
		case errorsChannel <- err:
		default:
		}
		cancel()
	}}
	defer func() {
		shutdownCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		result = errors.Join(result, s.shutdown(shutdownCtx))
	}()
	var err error
	s.data, err = listen(c.DataPort, "DATA_PORT_UNAVAILABLE")
	if err != nil {
		return err
	}
	s.admin, err = listen(c.AdminPort, "ADMIN_PORT_UNAVAILABLE")
	if err != nil {
		return err
	}
	c, err = ensureOwner(c, configPath)
	if err != nil {
		return err
	}
	log := logger.New(os.Stdout)
	store := auth.New(c.AllowInsecureLocalhost && strings.HasPrefix(c.PublicOrigin, "http://127.0.0.1:"), c.Auth.Password)
	defer store.Close()
	if err = store.SetStatePath(filepath.Join(filepath.Dir(configPath), "auth-state.json")); err != nil {
		return err
	}
	if c.Auth.Mode == "password" {
		if err = store.LoadState(); err != nil {
			return err
		}
	}
	a := &application{config: c, configPath: configPath, runtimeDir: runtimeDir, tunnelPath: tunnelPath, options: options,
		adapter: host.New(c, runtimeDir, options.BootstrapCommand), provider: tunnel.NewProvider(c, runtimeDir, options.CloudflaredPath),
		publicAuth: store, log: log, manualHost: options.NoHost, ctx: ctx}
	a.enabled.Store(!options.NoHost)
	a.gateway.Store(gateway.New(c, store, log, 0))
	ops := admin.Operations{Status: a.status, Doctor: a.doctor,
		Start: func() error { a.enabled.Store(true); return a.start() }, Stop: func() error { a.enabled.Store(false); return a.stop() },
		Reconnect: a.reconnect, Disconnect: func() error { return a.gateway.Load().Controllers.Disconnect() },
		Shutdown: cancel, Save: a.save, AutoStart: a.autoStart, RuntimeConfig: a.snapshot, TunnelRestart: a.restartTunnel,
		TunnelStatus: func() tunnel.StatusSnapshot { a.mu.Lock(); defer a.mu.Unlock(); return a.provider.Status() }}
	a.admin, err = admin.New(c, store, log, ops, runtimeDir, configPath, tunnelPath)
	if err != nil {
		return err
	}
	defer a.admin.Store.Close()
	defer os.Remove(filepath.Join(runtimeDir, "cli-session.json"))
	defer os.Remove(filepath.Join(runtimeDir, "bootstrap.code"))
	rotateCLI := func() error {
		session := a.admin.Store.CookieSession(time.Now().Add(24 * time.Hour))
		if session == nil {
			return errors.New("CLI_SESSION_LIMIT")
		}
		data, err := json.Marshal(cliCredentials{ID: session.RawToken, CSRF: session.CSRF})
		if err != nil {
			return err
		}
		return config.AtomicWrite(filepath.Join(runtimeDir, "cli-session.json"), data, 0600)
	}
	if err = rotateCLI(); err != nil {
		return err
	}
	server := func(handler http.Handler) *http.Server {
		return &http.Server{Handler: handler, ErrorLog: log.HTTPErrorLog(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	}
	s.dataServer = server(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.gateway.Load().ServeHTTP(w, r) }))
	s.adminServer = server(a.admin)
	settings, err := automaticTLSConfig(c, configPath)
	if err != nil {
		return err
	}
	if settings != nil {
		s.tls, err = newTLSEndpoint(c.TLSPort, settings, s.dataServer.Handler, log.HTTPErrorLog())
		if err != nil {
			return err
		}
	}
	a.reservePorts = func(previous, next config.Config) (func() error, func(), error) {
		return s.reserve(previous, next, configPath)
	}
	go s.serve(s.dataServer, s.data)
	go s.serve(s.adminServer, s.admin)
	go s.serveTLS(s.tls)
	var workers sync.WaitGroup
	for _, work := range []func(context.Context){a.superviseTunnel, a.superviseHost, func(ctx context.Context) {
		ticker := time.NewTicker(12 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := rotateCLI(); err != nil {
					s.fail(err)
					return
				}
			}
		}
	}} {
		workers.Add(1)
		go func() { defer workers.Done(); work(ctx) }()
	}
	var cleanupOnce sync.Once
	var cleanupErr error
	cleanup := func() error {
		cleanupOnce.Do(func() {
			cancel()
			a.stopping.Store(true)
			a.enabled.Store(false)
			workers.Wait()
			a.operation.Lock()
			defer a.operation.Unlock()
			cleanupErr = a.stopLocked()
			a.provider.Stop()
			log.Add(logger.Event{Event: "gateway_stopped"})
		})
		return cleanupErr
	}
	defer func() { result = errors.Join(result, cleanup()) }()
	log.Add(logger.Event{Event: "gateway_started"})
	if !options.NoTray {
		go func() { <-ctx.Done(); tray.Quit() }()
		tray.Run(tray.Actions{
			Open: func() {
				if openUI(a.snapshot(), runtimeDir) != nil {
					log.Add(logger.Event{Event: "open_ui_failed", ErrorCode: "OPEN_UI_FAILED"})
				}
			},
			Start: func() {
				a.enabled.Store(true)
				if a.start() != nil {
					log.Add(logger.Event{Event: "host_attach_failed", ErrorCode: "HOST_ATTACH_FAILED"})
				}
			},
			Stop: func() {
				a.enabled.Store(false)
				if a.stop() != nil {
					log.Add(logger.Event{Event: "host_cleanup_failed", ErrorCode: "HOST_CLEANUP_FAILED"})
				}
			},
			Status: func() string {
				a.mu.Lock()
				defer a.mu.Unlock()
				if a.client != nil {
					return "ChatGPT 已连接"
				}
				return "ChatGPT 等待连接"
			},
			Quit: cancel, Exit: func() {
				if cleanup() != nil {
					os.Exit(1)
				}
			},
		})
	}
	<-ctx.Done()
	select {
	case err = <-errorsChannel:
		return err
	default:
		return nil
	}
}

package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"remote-codex/internal/config"
	"sync/atomic"
	"time"

	"remote-codex/internal/tunnel"
)

func automaticTLSConfig(c config.Config, configPath string) (*tls.Config, error) {
	if err := config.Validate(c); err != nil {
		return nil, err
	}
	if c.TLSPort == 0 || !c.TLSAutocert {
		return nil, nil
	}
	return tunnel.NewProvider(c, "", "").TLSConfig(filepath.Dir(configPath))
}

type tlsEndpoint struct {
	server   *http.Server
	listener net.Listener
	settings atomic.Pointer[tls.Config]
}

func newTLSEndpoint(port int, settings *tls.Config, handler http.Handler, errorLog *log.Logger) (*tlsEndpoint, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("TLS_PORT_UNAVAILABLE: %w", err)
	}
	endpoint := &tlsEndpoint{listener: listener}
	endpoint.settings.Store(settings)
	endpoint.server = &http.Server{
		// net/http 同时将 ReadHeaderTimeout 用作 TLS 握手期限，首次签发需要等待 ACME 验证。
		Handler: handler, ErrorLog: errorLog, ReadHeaderTimeout: 90 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384,
		TLSConfig: &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return endpoint.settings.Load(), nil
		}},
	}
	return endpoint, nil
}

func (e *tlsEndpoint) shutdown(ctx context.Context) error {
	if e == nil {
		return nil
	}
	// 尚未进入 ServeTLS 的预留监听同样需要关闭。
	err := e.server.Shutdown(ctx)
	if err != nil {
		return errors.Join(err, e.server.Close(), e.listener.Close())
	}
	if err := e.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

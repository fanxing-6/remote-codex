package main

import (
	"context"
	"errors"
	"time"

	"remote-codex/internal/auth"
	"remote-codex/internal/config"
	"remote-codex/internal/gateway"
	"remote-codex/internal/host"
	"remote-codex/internal/logger"
	"remote-codex/internal/tunnel"
)

func ensureOwner(c config.Config, path string) (config.Config, error) {
	c = c.Normalized()
	if tunnel.SettingsFor(c).PublicOrigin == "" {
		c = (tunnel.Origin{}).Bind(c)
	}
	if c.Auth.Mode == "password" && c.Auth.Password == nil {
		record, err := auth.Hash(auth.Random())
		if err != nil {
			return c, err
		}
		c.Auth.Password = record
	}
	return c, config.Save(path, c)
}
func (a *application) snapshot() config.Config { a.mu.Lock(); defer a.mu.Unlock(); return a.config }
func (a *application) prepareGateway(c config.Config) (*gateway.Gateway, error) {
	_, generation := a.gateway.Load().Controllers.Status()
	next := gateway.New(c, a.publicAuth, a.log, generation+1)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client != nil {
		if err := next.SetHost(a.client, a.archive); err != nil {
			return nil, err
		}
	}
	return next, nil
}
func (a *application) publish(c config.Config, next *gateway.Gateway) {
	previous := a.gateway.Swap(next)
	previous.ClearHost()
	a.mu.Lock()
	a.config = c
	a.adapter = host.New(c, a.runtimeDir, a.options.BootstrapCommand)
	a.mu.Unlock()
}
func (a *application) restartTunnel() error {
	a.operation.Lock()
	defer a.operation.Unlock()
	if a.stopping.Load() || a.ctx.Err() != nil {
		return errors.New("SERVICE_STOPPING")
	}
	a.mu.Lock()
	provider := a.provider
	a.mu.Unlock()
	c := a.snapshot()
	// 重新连接期间关闭旧公开入口，直到提供者返回当前地址。
	next, err := a.prepareGateway((tunnel.Origin{}).Bind(c))
	if err != nil {
		return err
	}
	a.publish((tunnel.Origin{}).Bind(c), next)
	origin, err := provider.Restart(a.ctx)
	if err != nil {
		return err
	}
	c = origin.Bind(c)
	next, err = a.prepareGateway(c)
	if err != nil {
		provider.Stop()
		return err
	}
	a.publish(c, next)
	a.log.Add(logger.Event{Event: "tunnel_started"})
	return nil
}
func (a *application) superviseTunnel(ctx context.Context) {
	delay := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		state := a.provider.Status().State
		a.mu.Unlock()
		if state != "running" {
			if err := a.restartTunnel(); err != nil {
				a.log.Add(logger.Event{Event: "tunnel_deferred", ErrorCode: "TUNNEL_UNAVAILABLE"})
				delay = min(2*delay, 2*time.Minute)
			} else {
				delay = 10 * time.Second
			}
		} else {
			delay = 10 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

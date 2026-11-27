package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme/autocert"
	"remote-codex/internal/config"
)

type Origin struct {
	URL  string
	Host string
}

func (o Origin) Bind(c config.Config) config.Config {
	c.PublicOrigin = o.URL
	c.AllowedHosts = []string{}
	if o.URL != "" {
		c.AllowedHosts = []string{o.Host}
	}
	return c
}

type StatusSnapshot struct {
	Mode             string `json:"mode"`
	State            string `json:"state"`
	Error            string `json:"error,omitempty"`
	PID              int    `json:"pid"`
	CloudflaredFound bool   `json:"cloudflaredFound"`
}
type DoctorCheck struct {
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type Provider interface {
	Mode() string
	Start(context.Context) (Origin, error)
	Restart(context.Context) (Origin, error)
	Stop()
	Status() StatusSnapshot
	DoctorCheck() DoctorCheck
	TLSConfig(string) (*tls.Config, error)
}

type Settings struct {
	Mode         string
	PublicOrigin string
	DataPort     int
	Protocol     string
	TLSAutocert  bool
}

func SettingsFor(c config.Config) Settings {
	s := Settings{Mode: c.Mode(), PublicOrigin: c.PublicOrigin, DataPort: c.DataPort, Protocol: c.QuickTunnelProtocol, TLSAutocert: c.TLSAutocert}
	if s.Mode == "quick" {
		s.PublicOrigin = ""
	}
	return s
}

func PrepareSetup(c config.Config) config.Config {
	if c.Mode() == "quick" {
		c = (Origin{}).Bind(c)
		c.AllowInsecureLocalhost = false
		c.TLSPort, c.TLSAutocert = 0, false
	}
	return c
}

func PrepareListeners(previous, next config.Config) config.Config {
	if previous.Mode() == "quick" && (next.PublicOrigin == "" || next.PublicOrigin == previous.PublicOrigin) {
		return PrepareSetup(next)
	}
	next.TunnelMode = "custom"
	return next
}

func NewProvider(c config.Config, runtimeDir, cloudflaredPath string) Provider {
	if c.Mode() == "quick" {
		return &QuickProvider{RuntimeDir: runtimeDir, Port: c.DataPort, Protocol: c.QuickTunnelProtocol, CloudflaredPath: cloudflaredPath}
	}
	return &CustomProvider{PublicOrigin: c.PublicOrigin, Autocert: c.TLSAutocert}
}

type QuickProvider struct {
	RuntimeDir      string
	Port            int
	Protocol        string
	CloudflaredPath string
	operation       sync.Mutex
	mu              sync.Mutex
	quick           *Quick
	state           string
	failure         string
}

func (*QuickProvider) Mode() string                          { return "quick" }
func (*QuickProvider) TLSConfig(string) (*tls.Config, error) { return nil, nil }
func (p *QuickProvider) Start(ctx context.Context) (Origin, error) {
	p.operation.Lock()
	defer p.operation.Unlock()
	return p.start(ctx)
}
func (p *QuickProvider) start(ctx context.Context) (Origin, error) {
	p.mu.Lock()
	if p.quick != nil {
		select {
		case <-p.quick.Done():
			p.quick = nil
		default:
			origin := p.quick.Origin
			p.mu.Unlock()
			return Origin{URL: origin, Host: strings.TrimPrefix(origin, "https://")}, nil
		}
	}
	p.state, p.failure = "starting", ""
	p.mu.Unlock()
	q, err := startQuick(ctx, p.RuntimeDir, p.Port, p.Protocol, 45*time.Second, p.CloudflaredPath)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.state, p.failure = "error", err.Error()
		return Origin{}, err
	}
	p.quick, p.state = q, "running"
	return Origin{URL: q.Origin, Host: strings.TrimPrefix(q.Origin, "https://")}, nil
}
func (p *QuickProvider) Restart(ctx context.Context) (Origin, error) {
	p.operation.Lock()
	defer p.operation.Unlock()
	p.stop()
	return p.start(ctx)
}
func (p *QuickProvider) stop() {
	p.mu.Lock()
	q := p.quick
	p.quick = nil
	p.state = "disabled"
	p.mu.Unlock()
	q.Close()
}
func (p *QuickProvider) Stop() { p.operation.Lock(); defer p.operation.Unlock(); p.stop() }
func (p *QuickProvider) Status() StatusSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := StatusSnapshot{Mode: p.Mode(), State: p.state, Error: p.failure}
	if s.State == "" {
		s.State = "disabled"
	}
	_, err := FindCloudflared(p.CloudflaredPath)
	s.CloudflaredFound = err == nil
	if p.quick != nil {
		select {
		case <-p.quick.Done():
			s.State, s.Error = "error", "QUICK_UNEXPECTED_EXIT"
		default:
			s.PID = p.quick.PID()
		}
	}
	return s
}
func (p *QuickProvider) DoctorCheck() DoctorCheck {
	_, err := FindCloudflared(p.CloudflaredPath)
	detail := "已检测到 cloudflared；临时通道使用独立配置"
	if err != nil {
		detail = "CLOUDFLARED_NOT_FOUND：请在桌面应用中下载 cloudflared"
	}
	return DoctorCheck{"Cloudflare Quick Tunnel", err == nil, detail}
}

type CustomProvider struct {
	PublicOrigin string
	Autocert     bool
	mu           sync.Mutex
	running      bool
}

func (*CustomProvider) Mode() string { return "custom" }
func (p *CustomProvider) Start(ctx context.Context) (Origin, error) {
	if err := ctx.Err(); err != nil {
		return Origin{}, err
	}
	u, err := url.Parse(p.PublicOrigin)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return Origin{}, errors.New("CONFIG_ORIGIN_INVALID")
	}
	p.mu.Lock()
	p.running = true
	p.mu.Unlock()
	return Origin{URL: p.PublicOrigin, Host: u.Host}, nil
}
func (p *CustomProvider) Restart(ctx context.Context) (Origin, error) { p.Stop(); return p.Start(ctx) }
func (p *CustomProvider) Stop()                                       { p.mu.Lock(); p.running = false; p.mu.Unlock() }
func (p *CustomProvider) Status() StatusSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := "disabled"
	if p.running {
		state = "running"
	}
	return StatusSnapshot{Mode: p.Mode(), State: state}
}
func (*CustomProvider) DoctorCheck() DoctorCheck {
	return DoctorCheck{"固定域名模式", true, "使用自有域名；支持原生 TLS 或外部反向代理"}
}
func (p *CustomProvider) TLSConfig(configDir string) (*tls.Config, error) {
	if !p.Autocert {
		return nil, nil
	}
	u, err := url.Parse(p.PublicOrigin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return nil, errors.New("CONFIG_TLS_AUTOCERT_INVALID")
	}
	cache := filepath.Join(configDir, "certs")
	if err = os.MkdirAll(cache, 0700); err != nil {
		return nil, err
	}
	if err = os.Chmod(cache, 0700); err != nil {
		return nil, err
	}
	manager := &autocert.Manager{Prompt: autocert.AcceptTOS, HostPolicy: autocert.HostWhitelist(u.Hostname()), Cache: autocert.DirCache(cache)}
	settings := manager.TLSConfig()
	settings.MinVersion = tls.VersionTLS12
	return settings, nil
}

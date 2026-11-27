package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"io"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"remote-codex/internal/embedassets"
	"runtime"
	"slices"
	"strings"
)

type Password struct {
	Algo string `json:"algo"`
	N    int    `json:"N"`
	R    int    `json:"r"`
	P    int    `json:"p"`
	Salt string `json:"salt"`
	Hash string `json:"hash"`
}
type Auth struct {
	Mode       string    `json:"mode"`
	Password   *Password `json:"password,omitempty"`
	TeamDomain string    `json:"teamDomain,omitempty"`
	Audience   string    `json:"audience,omitempty"`
}
type Config struct {
	TunnelMode             string   `json:"tunnelMode"`
	QuickTunnelProtocol    string   `json:"quickTunnelProtocol"`
	PublicOrigin           string   `json:"publicOrigin"`
	AllowedHosts           []string `json:"allowedHosts"`
	DataPort               int      `json:"dataPort"`
	AdminPort              int      `json:"adminPort"`
	TLSPort                int      `json:"tlsPort"`
	TLSAutocert            bool     `json:"tlsAutocert"`
	Auth                   Auth     `json:"auth"`
	AllowInsecureLocalhost bool     `json:"allowInsecureLocalhost"`
	TrustedProxy           []string `json:"trustedProxy"`
	BootstrapPython        string   `json:"bootstrapPython"`
	BootstrapHelper        string   `json:"bootstrapHelper"`
}

func Default() Config {
	return Config{TunnelMode: "quick", QuickTunnelProtocol: "http2", AllowedHosts: []string{}, DataPort: 18765, AdminPort: 18766, Auth: Auth{Mode: "password"}, TrustedProxy: []string{"127.0.0.1/32"}}
}

func (c Config) Mode() string {
	if c.TunnelMode != "" {
		return c.TunnelMode
	}
	if c.PublicOrigin != "" {
		return "custom"
	}
	return "quick"
}

func (c Config) Normalized() Config {
	c.TunnelMode = c.Mode()
	if c.AllowedHosts == nil {
		c.AllowedHosts = []string{}
	}
	return c
}

func ResolvePython(value, configDir string) (string, error) {
	if value != "" && value != "python3" && value != "python" && value != "auto" {
		path, err := exec.LookPath(value)
		if err != nil {
			return "", errors.New("BOOTSTRAP_PYTHON_NOT_FOUND")
		}
		return path, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	venv := filepath.Join(".venv", "bin", "python")
	if runtime.GOOS == "windows" {
		venv = filepath.Join(".venv", "Scripts", "python.exe")
	}
	for _, candidate := range []string{filepath.Join(filepath.Dir(executable), venv), filepath.Join(configDir, venv), "python3", "python"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", errors.New("BOOTSTRAP_PYTHON_NOT_FOUND")
}
func Validate(c Config) error {
	c = c.Normalized()
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	var value any
	if err = json.Unmarshal(data, &value); err != nil {
		return err
	}
	var schemaValue any
	if err = json.Unmarshal(embedassets.Read("config.schema.json"), &schemaValue); err != nil {
		return err
	}
	compiler := jsonschema.NewCompiler()
	if err = compiler.AddResource("urn:remote-codex:config", schemaValue); err != nil {
		return err
	}
	schema, err := compiler.Compile("urn:remote-codex:config")
	if err != nil {
		return err
	}
	if err = schema.Validate(value); err != nil {
		return errors.New("CONFIG_SCHEMA_INVALID")
	}
	if c.DataPort == c.AdminPort {
		return errors.New("CONFIG_PORT_COLLISION")
	}
	if c.Mode() == "quick" {
		if c.AllowInsecureLocalhost || c.TLSPort != 0 || c.TLSAutocert || c.Auth.Mode != "password" {
			return errors.New("CONFIG_QUICK_SECURITY_REQUIRED")
		}
		if c.PublicOrigin == "" {
			if len(c.AllowedHosts) != 0 {
				return errors.New("CONFIG_ORIGIN_INVALID")
			}
			return validateProxies(c)
		}
		if !regexp.MustCompile(`^https://[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.trycloudflare\.com$`).MatchString(c.PublicOrigin) || !slices.Equal(c.AllowedHosts, []string{strings.TrimPrefix(c.PublicOrigin, "https://")}) {
			return errors.New("CONFIG_QUICK_ORIGIN_INVALID")
		}
	}
	origin, err := url.Parse(c.PublicOrigin)
	if err != nil || origin.User != nil || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || !slices.Contains(c.AllowedHosts, origin.Host) || c.DataPort == c.AdminPort {
		return errors.New("CONFIG_ORIGIN_INVALID")
	}
	if origin.Scheme != "https" && !(c.AllowInsecureLocalhost && origin.Scheme == "http" && origin.Hostname() == "127.0.0.1") {
		return errors.New("CONFIG_HTTPS_REQUIRED")
	}
	if c.TLSPort != 0 && (c.TLSPort < 1024 || c.TLSPort > 65535 || c.TLSPort == c.DataPort || c.TLSPort == c.AdminPort) {
		return errors.New("CONFIG_TLS_PORT_INVALID")
	}
	if c.TLSAutocert && (origin.Scheme != "https" || c.TLSPort == 0) {
		return errors.New("CONFIG_TLS_AUTOCERT_INVALID")
	}
	return validateProxies(c)
}
func validateProxies(c Config) error {
	for _, proxy := range c.TrustedProxy {
		if _, err := netip.ParsePrefix(proxy); err != nil {
			return errors.New("CONFIG_PROXY_INVALID")
		}
	}
	return nil
}
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, err
	}
	c := Default()
	c.TunnelMode = ""
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&c); err != nil {
		return c, errors.New("CONFIG_JSON_INVALID")
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return c, errors.New("CONFIG_JSON_INVALID")
	}
	c = c.Normalized()
	return c, Validate(c)
}
func Save(path string, c Config) error {
	c = c.Normalized()
	if err := Validate(c); err != nil {
		return err
	}
	if c.Mode() == "quick" {
		c.PublicOrigin = ""
		c.AllowedHosts = []string{}
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, data, 0600)
}
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(path+".tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer os.Remove(path + ".tmp")
	if err = protectFile(path+".tmp", mode); err != nil {
		file.Close()
		return err
	}
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return replaceFile(path+".tmp", path)
}

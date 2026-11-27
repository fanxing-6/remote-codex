package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestQuickConfigurationAndLegacyMode(t *testing.T) {
	c := Default()
	if c.Mode() != "quick" || c.QuickTunnelProtocol != "http2" || c.BootstrapHelper != "" || c.AllowInsecureLocalhost {
		t.Fatal("quick defaults")
	}
	if err := Validate(c); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.TunnelMode = "unknown" }, func(c *Config) { c.QuickTunnelProtocol = "tcp" },
		func(c *Config) { c.AllowInsecureLocalhost = true }, func(c *Config) {
			c.Auth.Mode = "cloudflare-access"
			c.Auth.TeamDomain = "team.cloudflareaccess.com"
			c.Auth.Audience = "audience"
		},
		func(c *Config) { c.TLSPort = 18443 }, func(c *Config) { c.AllowedHosts = []string{"chat.example.com"} },
		func(c *Config) {
			c.PublicOrigin = "https://chat.example.com"
			c.AllowedHosts = []string{"chat.example.com"}
		},
		func(c *Config) {
			c.PublicOrigin = "https://assigned.trycloudflare.com"
			c.AllowedHosts = []string{"assigned.trycloudflare.com", "evil.example"}
		},
	} {
		invalid := c
		mutate(&invalid)
		if Validate(invalid) == nil {
			t.Fatal("invalid quick configuration accepted")
		}
	}
	path := filepath.Join(t.TempDir(), "config.json")
	c.PublicOrigin = "https://assigned.trycloudflare.com"
	c.AllowedHosts = []string{"assigned.trycloudflare.com"}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.PublicOrigin != "" || len(loaded.AllowedHosts) != 0 {
		t.Fatal("temporary origin persisted", err)
	}
	c = customConfig()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "tunnelMode")
	delete(fields, "quickTunnelProtocol")
	delete(fields, "bootstrapHelper")
	data, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(path)
	if err != nil || loaded.Mode() != "custom" || loaded.PublicOrigin != c.PublicOrigin || loaded.BootstrapHelper != "" {
		t.Fatal("legacy custom configuration", err)
	}
}

func TestPythonResolution(t *testing.T) {
	if _, err := ResolvePython(filepath.Join(t.TempDir(), "missing-python"), t.TempDir()); err == nil || err.Error() != "BOOTSTRAP_PYTHON_NOT_FOUND" {
		t.Fatal("missing explicit Python", err)
	}
	python, err := ResolvePython("", t.TempDir())
	if err != nil {
		t.Skip("Python unavailable")
	}
	if explicit, err := ResolvePython(python, t.TempDir()); err != nil || explicit != python {
		t.Fatal("explicit Python", err)
	}
}

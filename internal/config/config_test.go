package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func customConfig() Config {
	c := Default()
	c.TunnelMode = "custom"
	c.PublicOrigin = "https://chat.example.com"
	c.AllowedHosts = []string{"chat.example.com"}
	return c
}
func TestSchemaAndAtomicWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := Default()
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.PublicOrigin != c.PublicOrigin {
		t.Fatal("configuration round trip", err)
	}
	info, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatal("file permissions")
	}
	c.AdminPort = c.DataPort
	if err = Save(path, c); err == nil {
		t.Fatal("shared port accepted")
	}
	after, err := Load(path)
	if err != nil || after.AdminPort == after.DataPort {
		t.Fatal("invalid configuration replaced file")
	}
	c = customConfig()
	c.PublicOrigin = "http://127.0.0.1:18765"
	c.AllowedHosts = []string{"127.0.0.1:18765"}
	if Validate(c) == nil {
		t.Fatal("insecure localhost was implicit")
	}
	c.AllowInsecureLocalhost = true
	if err = Validate(c); err != nil {
		t.Fatal(err)
	}
}

func TestTLSConfiguration(t *testing.T) {
	for _, item := range []struct {
		name     string
		port     int
		autocert bool
		valid    bool
	}{
		{"disabled", 0, false, true}, {"listener_only", 18443, false, true}, {"automatic", 18443, true, true},
		{"minimum", 1024, true, true}, {"maximum", 65535, true, true}, {"negative", -1, false, false},
		{"privileged", 443, true, false}, {"too_large", 65536, true, false}, {"data_collision", 18765, true, false},
		{"admin_collision", 18766, true, false}, {"missing_port", 0, true, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			c := customConfig()
			c.TLSPort = item.port
			c.TLSAutocert = item.autocert
			if err := Validate(c); (err == nil) != item.valid {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
	c := customConfig()
	c.PublicOrigin = "http://127.0.0.1:18765"
	c.AllowedHosts = []string{"127.0.0.1:18765"}
	c.AllowInsecureLocalhost = true
	c.TLSPort = 18443
	c.TLSAutocert = true
	if Validate(c) == nil {
		t.Fatal("automatic TLS accepted an HTTP origin")
	}
	c = Default()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "tlsPort")
	delete(fields, "tlsAutocert")
	data, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.TLSPort != 0 || loaded.TLSAutocert {
		t.Fatalf("optional TLS defaults: %+v, %v", loaded, err)
	}
}

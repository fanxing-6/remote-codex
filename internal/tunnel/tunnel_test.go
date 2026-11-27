package tunnel

import (
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"remote-codex/internal/config"
	"strings"
	"testing"
)

func TestPreviewBackupAndStaleWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	source := []byte("tunnel: validation\nprotocol: http2\ningress:\n  - hostname: existing.example.test\n    service: http://127.0.0.1:3430\n  - service: http_status:404\n")
	if err := config.AtomicWrite(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	preview, err := Plan(path, "bridge.example.test", 18765, 18766)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview.Diff, "+  - hostname: bridge.example.test") {
		t.Fatal("diff does not contain new ingress")
	}
	before, err := os.ReadFile(path)
	if err != nil || string(before) != string(source) {
		t.Fatal("preview mutated input")
	}
	backup, err := Apply(path, preview)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(backup)
	if err != nil || string(saved) != string(source) {
		t.Fatal("backup mismatch")
	}
	var document struct {
		Ingress []struct{ Hostname, Service string }
	}
	if yaml.Unmarshal([]byte(preview.Content), &document) != nil || len(document.Ingress) != 3 || document.Ingress[0].Hostname != "existing.example.test" || document.Ingress[1].Service != "http://127.0.0.1:18765" || document.Ingress[2].Service != "http_status:404" {
		t.Fatal("ingress order")
	}
	if _, err = Apply(path, preview); err == nil {
		t.Fatal("stale preview accepted")
	}
	if _, err = Plan(path, "admin.example.test", 18766, 18766); err == nil {
		t.Fatal("admin tunnel accepted")
	}
	link := filepath.Join(t.TempDir(), "config.yml")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err = Plan(link, "bridge2.example.test", 18765, 18766); err == nil {
		t.Fatal("symlink accepted")
	}
}

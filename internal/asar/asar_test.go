package asar

import (
	archive "golift.io/asar"
	"os"
	"path/filepath"
	"remote-codex/internal/embedassets"
	"testing"
)

func createArchive(t *testing.T, entries []archive.Entry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.asar")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = archive.WriteArchive(file, entries); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestWhitelistPathsIntegrityAndBinding(t *testing.T) {
	index := embedassets.Read("login.html")
	entries := []archive.Entry{{Name: "webview/index.html", Data: index}, {Name: "webview/assets/browser.js", Data: embedassets.Read("browser.js")}, {Name: "package.json", Data: embedassets.Read("config.schema.json")}}
	path := createArchive(t, entries)
	reader, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err = reader.Read("index.html"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../package.json", "/index.html", "webview/../../package.json", "assets\\browser.js", "assets/../index.html", "package.json", "index.html\x00"} {
		if _, err = reader.Read(name); err == nil {
			t.Fatal("unsafe path accepted", name)
		}
	}
	if err = reader.Consistent(reader.BuildID); err != nil {
		t.Fatal(err)
	}
	if reader.Consistent("changed") == nil {
		t.Fatal("build mismatch accepted")
	}
	second := createArchive(t, []archive.Entry{{Name: "webview/index.html", Data: embedassets.Read("admin-ui/index.html")}})
	other, err := Open(second)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if other.BuildID == reader.BuildID {
		t.Fatal("entry not bound")
	}
	if err = other.Close(); err != nil {
		t.Fatal(err)
	}
	if err = reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(second, path); err != nil {
		t.Fatal(err)
	}
	if reader.Consistent(reader.BuildID) == nil {
		t.Fatal("archive replacement accepted")
	}
}
func TestSymlinkAndUnpackedRejected(t *testing.T) {
	for _, entry := range []archive.Entry{{Name: "webview/linked.html", Link: "webview/index.html"}, {Name: "webview/assets/external.js", Unpacked: true}} {
		path := createArchive(t, []archive.Entry{{Name: "webview/index.html", Data: embedassets.Read("login.html")}, entry})
		if _, err := Open(path); err == nil {
			t.Fatal("unsafe archive entry accepted")
		}
	}
	path := createArchive(t, []archive.Entry{{Name: "webview/index.html", Data: embedassets.Read("login.html")}})
	link := filepath.Join(t.TempDir(), "app.asar")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); err == nil {
		t.Fatal("archive symlink accepted")
	}
}
func TestOfficialArchive(t *testing.T) {
	path := os.Getenv("REMOTE_CODEX_TEST_ASAR")
	if path == "" {
		t.Skip("set REMOTE_CODEX_TEST_ASAR to an authorized application archive")
	}
	reader, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if len(reader.files) < 100 || len(reader.BuildID) != 64 {
		t.Fatal("official manifest")
	}
	if _, err = reader.Read("index.html"); err != nil {
		t.Fatal(err)
	}
}

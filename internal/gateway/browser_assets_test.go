package gateway

import (
	"bytes"
	"os"
	"testing"

	"remote-codex/internal/asar"
)

func TestBrowserAttachmentAsset(t *testing.T) {
	filename := os.Getenv("REMOTE_CODEX_TEST_ASAR")
	if filename == "" {
		t.Skip("set REMOTE_CODEX_TEST_ASAR to an authorized application archive")
	}
	archive, err := asar.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	name := "assets/async/839700.c1fe868f96.js"
	source, err := archive.Read(name)
	if err != nil {
		t.Fatal(err)
	}
	output, err := rewriteBrowserAsset(name, source)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"onPickBrowserFiles:window.__cwbRuntime.mobile?e=>pe({...e,imagesOnly:false}):void 0",
		"supportsFileAttachments:window.__cwbRuntime.mobile||vH",
		"canCopyBrowserFileAttachments:!1",
	} {
		if !bytes.Contains(output, []byte(expected)) {
			t.Fatalf("missing browser attachment setting: %s", expected)
		}
	}
	unchanged, err := rewriteBrowserAsset("other.js", source)
	if err != nil || !bytes.Equal(unchanged, source) {
		t.Fatal("unrelated asset changed")
	}
	if _, err = rewriteBrowserAsset(name, output); err == nil {
		t.Fatal("unsupported asset accepted")
	}
	entry, err := archive.Read(browserEntryAsset)
	if err != nil {
		t.Fatal(err)
	}
	entry, err = rewriteBrowserAsset(browserEntryAsset, entry)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(entry, []byte(`[839700,191628,584602,893680,882456].includes(e)?"?cwb=`+browserAssetRevision)) {
		t.Fatal("browser attachment cache revision missing")
	}
	for _, name := range []string{browserRouteAsset, browserDILFrameAsset, "assets/async/584602.d4ff4f180f.js", "assets/async/893680.05fde0ce2a.js"} {
		source, err := archive.Read(name)
		if err != nil {
			t.Fatal(err)
		}
		output, err := rewriteBrowserAsset(name, source)
		if err != nil {
			t.Fatal(name, err)
		}
		if bytes.Equal(output, source) {
			t.Fatal("adapted asset unchanged", name)
		}
		if name == browserRouteAsset && !bytes.Contains(output, []byte("window.__cwbRuntime.rememberRoute(G)")) {
			t.Fatal("router notification missing")
		}
		if name == browserDILFrameAsset && !bytes.Contains(output, []byte("window.__cwbRuntime.isDILRunner(t)")) {
			t.Fatal("sandbox runner validation missing")
		}
		if name != browserRouteAsset && name != browserDILFrameAsset && !bytes.Contains(output, []byte("window.__cwbConfiguration.dilBasePath")) {
			t.Fatal("DIL proxy reference missing")
		}
	}
	index, err := archive.Read("index.html")
	if err != nil {
		t.Fatal(err)
	}
	index, err = RewriteHTML(index, "https://bridge.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(index, []byte("/"+browserEntryAsset+"?cwb="+browserAssetRevision)) {
		t.Fatal("browser entry cache revision missing")
	}
	for _, asset := range []string{"/cwb/browser.js", "/cwb/mobile.css"} {
		if !bytes.Contains(index, []byte(asset+"?cwb="+browserAssetRevision)) {
			t.Fatalf("browser runtime cache revision missing: %s", asset)
		}
	}
	for _, origin := range []string{"https://assigned.trycloudflare.com", "https://chat.example.com"} {
		source, err := archive.Read("index.html")
		if err != nil {
			t.Fatal(err)
		}
		index, err = RewriteHTML(source, origin)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(index, []byte("wss://"+origin[len("https://"):])) {
			t.Fatal("current origin missing from CSP")
		}
		if !bytes.Contains(index, []byte("interactive-widget=resizes-content")) {
			t.Fatal("mobile viewport missing")
		}
	}
}

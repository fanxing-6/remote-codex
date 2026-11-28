package gateway

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"remote-codex/internal/auth"
	"remote-codex/internal/config"
	"remote-codex/internal/embedassets"
	"remote-codex/internal/logger"
	"remote-codex/internal/protocol"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/net/html"
)

func assetResponse(t *testing.T, name, contentType, encoding, condition, method string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "https://chat.example.com/"+name, nil)
	request.Header.Set("Accept-Encoding", encoding)
	request.Header.Set("If-None-Match", condition)
	response := httptest.NewRecorder()
	buildID := fmt.Sprintf("%x", sha256.Sum256(embedassets.Read("browser.js")))
	var cache gzipAssetCache
	cache.serveStaticAsset(response, request, buildID, name, contentType, data)
	return response
}

func decodedAsset(t *testing.T, response *httptest.ResponseRecorder) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(response.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestStaticAssetCompressionAndValidation(t *testing.T) {
	for _, asset := range []struct{ name, contentType, embedded string }{
		{"cwb/browser.js", "text/javascript", "browser.js"},
		{"cwb/mobile.css", "text/css", "mobile.css"},
		{"index.html", "text/html; charset=utf-8", "admin-ui/index.html"},
		{"config.schema.json", "application/json", "config.schema.json"},
	} {
		t.Run(asset.name, func(t *testing.T) {
			data := embedassets.Read(asset.embedded)
			if len(data) < 1024 {
				t.Fatal("compression test requires an actual asset above the threshold")
			}
			identity := assetResponse(t, asset.name, asset.contentType, "", "", "GET", data)
			compressed := assetResponse(t, asset.name, asset.contentType, "br, gzip;q=0.8", "", "GET", data)
			if identity.Code != 200 || !bytes.Equal(identity.Body.Bytes(), data) || identity.Header().Get("Content-Encoding") != "" {
				t.Fatal("identity representation changed")
			}
			if compressed.Code != 200 || compressed.Header().Get("Content-Encoding") != "gzip" || compressed.Header().Get("Vary") != "Accept-Encoding" {
				t.Fatal(compressed.Code, compressed.Header())
			}
			if !bytes.Equal(decodedAsset(t, compressed), data) || compressed.Body.Len() >= len(data) {
				t.Fatal("gzip must preserve the asset and reduce transferred bytes")
			}
			tag := compressed.Header().Get("ETag")
			if !strings.HasPrefix(tag, `W/"`) || tag != identity.Header().Get("ETag") {
				t.Fatal("encodings must share a deterministic weak ETag", tag)
			}
			cacheControl := "private, no-cache"
			if asset.name == "config.schema.json" {
				cacheControl = "private, max-age=86400"
			}
			if compressed.Header().Get("Cache-Control") != cacheControl {
				t.Fatal("unexpected asset cache policy", compressed.Header())
			}
			for _, encoding := range []string{"", "gzip"} {
				for _, condition := range []string{tag, strings.TrimPrefix(tag, "W/"), `"other", ` + tag, "*"} {
					response := assetResponse(t, asset.name, asset.contentType, encoding, condition, "GET", data)
					if response.Code != 304 || response.Body.Len() != 0 || response.Header().Get("ETag") != tag || response.Header().Get("Cache-Control") != cacheControl || response.Header().Get("Vary") != "Accept-Encoding" {
						t.Fatal("conditional request must return metadata without a body", response.Code, response.Header(), response.Body.Len())
					}
					if response.Header().Get("Content-Length") != "" || response.Header().Get("Content-Encoding") != "" {
						t.Fatal("304 must omit body representation headers")
					}
				}
			}
			head := assetResponse(t, asset.name, asset.contentType, "gzip", "", "HEAD", data)
			if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Encoding") != "gzip" || head.Header().Get("Content-Length") != compressed.Header().Get("Content-Length") {
				t.Fatal("HEAD must describe the GET representation without a body")
			}
			miss := assetResponse(t, asset.name, asset.contentType, "gzip", `W/"other"`, "GET", data)
			if miss.Code != 200 || !bytes.Equal(decodedAsset(t, miss), data) {
				t.Fatal("nonmatching ETag must return the current representation")
			}
		})
	}
}

func TestStaticAssetCompressionNegotiationAndBinary(t *testing.T) {
	data := embedassets.Read("browser.js")
	for _, item := range []struct {
		encoding string
		gzip     bool
	}{
		{"gzip", true}, {"GZIP", true}, {"gzip; q=0.5", true}, {"*", true},
		{"", false}, {"br", false}, {"xgzip", false}, {"gzip;q=0", false},
		{"*;q=1, gzip;q=0", false}, {"gzip;q=invalid", false}, {"gzip;q=NaN", false},
	} {
		t.Run(item.encoding, func(t *testing.T) {
			response := assetResponse(t, "cwb/browser.js", "text/javascript", item.encoding, "", "GET", data)
			if (response.Header().Get("Content-Encoding") == "gzip") != item.gzip {
				t.Fatal(response.Header())
			}
			if !item.gzip && !bytes.Equal(response.Body.Bytes(), data) {
				t.Fatal("declined encoding changed the asset")
			}
		})
	}
	image, err := os.ReadFile("../../desktop/build/icon.png")
	if err != nil {
		t.Fatal(err)
	}
	response := assetResponse(t, "icon.png", "image/png", "gzip", "", "GET", image)
	if response.Header().Get("Content-Encoding") != "" || !bytes.Equal(response.Body.Bytes(), image) {
		t.Fatal("PNG must be sent without gzip")
	}
	for _, name := range []string{"photo.jpg", "photo.webp", "font.woff2", "bundle.br", "bundle.gz"} {
		if compressibleAsset(name, "text/plain") {
			t.Fatal("compressed file extension must prevent recompression", name)
		}
	}
	for _, contentType := range []string{"image/svg+xml", "application/wasm", "application/problem+json"} {
		if !compressibleAsset("asset", contentType) {
			t.Fatal("compressible content type rejected", contentType)
		}
	}
	short := embedassets.Read("login.html")
	response = assetResponse(t, "login.html", "text/html", "gzip", "", "GET", short)
	if len(short) >= 1024 || response.Header().Get("Content-Encoding") != "" || !bytes.Equal(response.Body.Bytes(), short) {
		t.Fatal("small asset must be sent without gzip")
	}
}

func TestStaticAssetVersionsAndCachePolicies(t *testing.T) {
	data := embedassets.Read("browser.js")
	nextData := embedassets.Read("mobile.css")
	buildID := fmt.Sprintf("%x", sha256.Sum256(data))
	nextBuildID := fmt.Sprintf("%x", sha256.Sum256(nextData))
	for _, name := range []string{"index.html", "cwb/browser.js", "cwb/mobile.css", browserEntryAsset, browserComposerAsset} {
		tag := staticAssetETag(buildID, name, data)
		if tag == staticAssetETag(nextBuildID, name, data) || tag == staticAssetETag(buildID, name, nextData) {
			t.Fatal("rewritten or embedded asset update must invalidate its ETag", name)
		}
		response := assetResponse(t, name, "text/javascript", "", "", "GET", data)
		if response.Header().Get("Cache-Control") != "private, no-cache" {
			t.Fatal("rewritten asset must revalidate", name)
		}
	}
	name := "assets/" + buildID + ".js"
	response := assetResponse(t, name, "text/javascript", "", "", "GET", data)
	if response.Header().Get("Cache-Control") != "private, max-age=86400" || staticAssetETag(buildID, name, data) == staticAssetETag(nextBuildID, name, data) {
		t.Fatal("hashed archive asset must retain its versioned daily cache")
	}
}

func TestStaticAssetsConcurrentAndPrivateConfiguration(t *testing.T) {
	data := embedassets.Read("browser.js")
	original := bytes.Clone(data)
	var workers sync.WaitGroup
	for index := 0; index < 16; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			response := assetResponse(t, "cwb/browser.js", "text/javascript", "gzip", "", "GET", data)
			if !bytes.Equal(decodedAsset(t, response), original) {
				t.Error("concurrent response changed the asset")
			}
		}()
	}
	workers.Wait()
	if !bytes.Equal(data, original) {
		t.Fatal("compression mutated the shared source")
	}
	request := httptest.NewRequest("GET", "https://chat.example.com/cwb/configuration.js", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	request.Header.Set("If-None-Match", "*")
	response := httptest.NewRecorder()
	serveAsset(response, request, "cwb/configuration.js", "text/javascript", "no-store", data, nil)
	if response.Code != 200 || response.Header().Get("ETag") != "" || response.Header().Get("Cache-Control") != "no-store" || !bytes.Equal(decodedAsset(t, response), data) {
		t.Fatal("session configuration must be fresh and uncached")
	}
}

func TestGzipAssetCacheConcurrentReuseAndVersionChange(t *testing.T) {
	data := embedassets.Read("browser.js")
	buildID := fmt.Sprintf("%x", sha256.Sum256(data))
	tag := staticAssetETag(buildID, "cwb/browser.js", data)
	g := &Gateway{}
	cache := &g.assets
	cache.bindBuildID(buildID)
	results := make(chan []byte, 16)
	var workers sync.WaitGroup
	for index := 0; index < cap(results); index++ {
		workers.Add(1)
		go func() { defer workers.Done(); results <- cache.compressed(tag, data) }()
	}
	workers.Wait()
	close(results)
	var first []byte
	for result := range results {
		if first == nil {
			first = result
		}
		if &first[0] != &result[0] {
			t.Fatal("concurrent requests must reuse one compressed representation")
		}
	}
	response := httptest.NewRecorder()
	response.Body.Write(first)
	if !bytes.Equal(decodedAsset(t, response), data) || len(cache.entries) != 1 {
		t.Fatal("cached representation differs from the source")
	}
	g.ClearHost()
	cache.bindBuildID(buildID)
	if reused := cache.compressed(tag, data); &reused[0] != &first[0] {
		t.Fatal("standby and same-build attachment must retain static byte cache")
	}
	nextBuildID := fmt.Sprintf("%x", sha256.Sum256(embedassets.Read("mobile.css")))
	cache.bindBuildID(nextBuildID)
	if cache.entries != nil {
		t.Fatal("build change must release obsolete compressed assets")
	}
	regenerated := cache.compressed(staticAssetETag(nextBuildID, "cwb/browser.js", data), data)
	if !bytes.Equal(regenerated, first) || &regenerated[0] == &first[0] {
		t.Fatal("new build must create an independent compressed representation")
	}
}

func BenchmarkGzipAssetUncached(b *testing.B) {
	data := embedassets.Read("browser.js")
	b.ReportAllocs()
	for b.Loop() {
		compressAsset(data)
	}
}

func TestGzipAssetCacheVersionChangeDuringResponses(t *testing.T) {
	data := embedassets.Read("browser.js")
	buildID := fmt.Sprintf("%x", sha256.Sum256(data))
	nextBuildID := fmt.Sprintf("%x", sha256.Sum256(embedassets.Read("mobile.css")))
	tag := staticAssetETag(buildID, "cwb/browser.js", data)
	var cache gzipAssetCache
	var workers sync.WaitGroup
	results := make(chan []byte, 64)
	for index := 0; index < 8; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for attempt := 0; attempt < 8; attempt++ {
				results <- cache.compressed(tag, data)
			}
		}()
	}
	for attempt := 0; attempt < 8; attempt++ {
		cache.bindBuildID(nextBuildID)
		cache.bindBuildID(buildID)
	}
	workers.Wait()
	close(results)
	for encoded := range results {
		response := httptest.NewRecorder()
		response.Body.Write(encoded)
		if !bytes.Equal(decodedAsset(t, response), data) {
			t.Fatal("cache invalidation changed an in-flight response")
		}
	}
}

func BenchmarkGzipAssetCached(b *testing.B) {
	data := embedassets.Read("browser.js")
	tag := staticAssetETag(fmt.Sprintf("%x", sha256.Sum256(data)), "cwb/browser.js", data)
	var cache gzipAssetCache
	cache.compressed(tag, data)
	b.ReportAllocs()
	for b.Loop() {
		cache.compressed(tag, data)
	}
}

func TestLoginAssetCompressionWithoutHost(t *testing.T) {
	c := publicConfig()
	g := New(c, auth.New(false, nil), logger.New(io.Discard), 0)
	t.Cleanup(g.Auth.Close)
	request := httptest.NewRequest("GET", c.PublicOrigin+"/auth/login.js", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	g.ServeHTTP(response, request)
	if response.Code != 200 || response.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(decodedAsset(t, response), embedassets.Read("login.js")) {
		t.Fatal("login JavaScript must be compressed without a host")
	}
	request.Header.Set("If-None-Match", response.Header().Get("ETag"))
	response = httptest.NewRecorder()
	g.ServeHTTP(response, request)
	if response.Code != 304 || response.Body.Len() != 0 {
		t.Fatal("login asset must support revalidation")
	}
}

type measuredConnection struct {
	net.Conn
	received atomic.Int64
}

func (c *measuredConnection) Read(data []byte) (int, error) {
	count, err := c.Conn.Read(data)
	c.received.Add(int64(count))
	return count, err
}

func TestBridgeWebsocketCompression(t *testing.T) {
	for _, compression := range []bool{true, false} {
		t.Run(fmt.Sprint(compression), func(t *testing.T) {
			g := New(publicConfig(), auth.New(false, nil), logger.New(io.Discard), 0)
			t.Cleanup(g.Auth.Close)
			upgrader := g.websocketUpgrader()
			if upgrader.HandshakeTimeout != 15*time.Second {
				t.Fatal("unexpected bridge handshake timeout")
			}
			payload := embedassets.Read("browser.js")
			done := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/bridge" {
					http.NotFound(w, r)
					return
				}
				socket, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					done <- err
					return
				}
				defer socket.Close()
				item := &claim{socket: socket}
				if err := socket.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
					done <- err
					return
				}
				if _, _, err := socket.ReadMessage(); err != nil {
					done <- err
					return
				}
				if err := socket.SetCompressionLevel(flate.BestSpeed); err != nil {
					done <- err
					return
				}
				if err := socket.WriteMessage(websocket.BinaryMessage, payload); err != nil {
					done <- err
					return
				}
				kind, data, err := socket.ReadMessage()
				if err != nil {
					done <- err
					return
				}
				done <- item.write(kind, data)
			}))
			t.Cleanup(server.Close)
			var measured *measuredConnection
			dialer := websocket.Dialer{EnableCompression: compression, Subprotocols: []string{"cwb.v1"}, NetDialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				connection, err := (&net.Dialer{}).DialContext(ctx, network, address)
				if err != nil {
					return nil, err
				}
				measured = &measuredConnection{Conn: connection}
				return measured, nil
			}}
			socket, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/bridge", http.Header{"Origin": {g.Config.PublicOrigin}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { socket.Close() })
			if strings.Contains(response.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate") != compression || socket.Subprotocol() != "cwb.v1" {
				t.Fatal("unexpected bridge negotiation", response.Header)
			}
			if err := socket.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			before := measured.received.Load()
			if err := socket.WriteMessage(websocket.TextMessage, []byte("ready")); err != nil {
				t.Fatal(err)
			}
			kind, data, err := socket.ReadMessage()
			if err != nil || kind != websocket.BinaryMessage || !bytes.Equal(data, payload) {
				t.Fatal("initial state payload changed", err)
			}
			baselineBytes := measured.received.Load() - before
			if compression && baselineBytes >= int64(len(payload)) {
				t.Fatal("negotiated compression did not reduce wire bytes")
			}
			if err := socket.WriteMessage(websocket.BinaryMessage, payload); err != nil {
				t.Fatal(err)
			}
			before = measured.received.Load()
			_, data, err = socket.ReadMessage()
			if err != nil || !bytes.Equal(data, payload) {
				t.Fatal("compressed round trip changed the payload", err)
			}
			if compression && measured.received.Load()-before >= baselineBytes*99/100 {
				t.Fatal("large state compression must reduce wire bytes beyond BestSpeed")
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("bridge round trip did not finish")
			}
		})
	}
}

func TestLifecycleAuthenticationBoundary(t *testing.T) {
	c := publicConfig()
	g := New(c, auth.New(false, nil), logger.New(io.Discard), 0)
	t.Cleanup(g.Auth.Close)
	var wakes, touches atomic.Int64
	g.SetHostLifecycle(func(ctx context.Context) error {
		wakes.Add(1)
		return ctx.Err()
	}, func() { touches.Add(1) }, nil)
	session := g.Auth.CookieSession(time.Now().Add(time.Hour))
	for _, authenticated := range []bool{false, true} {
		for _, item := range []struct {
			method, path string
			wakes        bool
		}{
			{"GET", "/", true},
			{"HEAD", "/index.html", true},
			{"GET", "/cwb/browser.js", true},
			{"POST", "/api/controller", true},
			{"POST", "/api/takeover", true},
			{"GET", "/livez", false},
			{"GET", "/healthz", false},
			{"GET", "/auth/login", false},
			{"GET", "/auth/login.js", false},
			{"GET", "/auth/login.css", false},
			{"GET", "/auth/csrf", false},
			{"HEAD", "/livez", false},
			{"HEAD", "/auth/login", false},
			{"HEAD", "/auth/login.js", false},
			{"HEAD", "/auth/login.css", false},
			{"HEAD", "/auth/csrf", false},
			{"GET", "/auth/ticket", false},
			{"POST", "/auth/login", false},
			{"POST", "/auth/ticket", false},
			{"GET", "/api/session", false},
			{"GET", "/bridge", false},
		} {
			t.Run(item.method+item.path+map[bool]string{true: "/authenticated", false: "/anonymous"}[authenticated], func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				request := httptest.NewRequest(item.method, c.PublicOrigin+item.path, strings.NewReader("{}")).WithContext(ctx)
				request.Header.Set("Origin", c.PublicOrigin)
				if authenticated {
					request.AddCookie(g.Auth.Cookie(session.RawToken, 3600))
					request.Header.Set("X-CSRF-Token", session.CSRF)
				}
				before := wakes.Load()
				response := httptest.NewRecorder()
				g.ServeHTTP(response, request)
				want := int64(0)
				if authenticated && item.wakes {
					want = 1
					if response.Code != 503 {
						t.Fatal("canceled wake must return unavailable", response.Code)
					}
				}
				if wakes.Load()-before != want || touches.Load() != 0 {
					t.Fatal("unexpected lifecycle activity", wakes.Load()-before, touches.Load())
				}
			})
		}
	}
	for _, item := range []struct{ origin, csrf string }{{"https://other.example.com", session.CSRF}, {c.PublicOrigin, "invalid"}} {
		request := httptest.NewRequest("POST", c.PublicOrigin+"/api/controller", strings.NewReader("{}"))
		request.AddCookie(g.Auth.Cookie(session.RawToken, 3600))
		request.Header.Set("Origin", item.origin)
		request.Header.Set("X-CSRF-Token", item.csrf)
		before := wakes.Load()
		response := httptest.NewRecorder()
		g.ServeHTTP(response, request)
		if response.Code != 403 || wakes.Load() != before {
			t.Fatal("rejected request must not wake the host")
		}
	}
}

func TestStandbyHealthAndPendingClients(t *testing.T) {
	c := publicConfig()
	g := New(c, auth.New(false, nil), logger.New(io.Discard), 0)
	t.Cleanup(g.Auth.Close)
	standby := true
	g.SetHostLifecycle(nil, nil, func() bool {
		// 回调允许读取 Gateway 状态，验证调用期间已释放互斥锁。
		g.HasActiveClients()
		return standby
	})
	session := g.Auth.CookieSession(time.Now().Add(time.Hour))
	for _, enabled := range []bool{true, false} {
		standby = enabled
		request := httptest.NewRequest("GET", c.PublicOrigin+"/healthz", nil)
		request.AddCookie(g.Auth.Cookie(session.RawToken, 3600))
		response := httptest.NewRecorder()
		g.ServeHTTP(response, request)
		var health map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
			t.Fatal(err)
		}
		if response.Code != 200 || health["hostStandby"] != enabled || health["shimConnected"] != false || (health["compatStatus"] == "standby") != enabled {
			t.Fatal(response.Code, health)
		}
	}
	if g.HasActiveClients() {
		t.Fatal("new gateway must have no clients")
	}
	item := &claim{id: auth.Random(), session: session.ID, started: time.Now()}
	g.claims[item.id] = item
	if !g.HasActiveClients() {
		t.Fatal("pending claim must prevent idle detach without a controller owner")
	}
	delete(g.claims, item.id)
	if g.HasActiveClients() {
		t.Fatal("removed claim must no longer prevent idle detach")
	}
}

func publicConfig() config.Config {
	c := config.Default()
	c.TunnelMode = "custom"
	c.PublicOrigin = "https://chat.example.com"
	c.AllowedHosts = []string{"chat.example.com"}
	return c
}
func TestPublicHostOriginAndLoginCSRF(t *testing.T) {
	c := publicConfig()
	g := New(c, auth.New(false, nil), logger.New(io.Discard), 0)
	defer g.Auth.RevokeAll()
	for _, item := range []struct {
		method, path, host, origin string
		code                       int
	}{{"GET", "/livez", "chat.example.com", "", 200}, {"GET", "/healthz", "chat.example.com", "", 401}, {"GET", "/", "evil.example", "", 403}, {"POST", "/auth/login", "chat.example.com", "https://sibling.example.com", 403}, {"POST", "/auth/login", "chat.example.com", c.PublicOrigin, 403}, {"POST", "/auth/login", "chat.example.com", "null", 403}} {
		request := httptest.NewRequest(item.method, "https://"+item.host+item.path, nil)
		request.Header.Set("Origin", item.origin)
		recorder := httptest.NewRecorder()
		g.ServeHTTP(recorder, request)
		if recorder.Code != item.code {
			t.Fatal(item.path, recorder.Code)
		}
	}
	request := httptest.NewRequest("GET", c.PublicOrigin+"/bridge", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	recorder := httptest.NewRecorder()
	g.ServeHTTP(recorder, request)
	if recorder.Code != 403 {
		t.Fatal("missing WS origin accepted")
	}
}
func TestOpaqueBinaryQueueBound(t *testing.T) {
	c := config.Default()
	g := New(c, auth.New(false, nil), logger.New(io.Discard), 0)
	item := &claim{output: make(chan []byte, 128)}
	g.active = item
	asset := embedassets.Read("browser.js")
	payload := bytes.Repeat(asset, protocol.MaxFrame/len(asset)+1)[:protocol.MaxFrame]
	g.forward(payload)
	g.forward(payload)
	g.forward(payload)
	deadline := time.Now().Add(time.Second)
	for {
		item.mu.Lock()
		revoked, size := item.revoked, item.bytes
		item.mu.Unlock()
		if size > protocol.MaxQueue {
			t.Fatal("queue exceeded byte bound")
		}
		if revoked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queue overflow did not close controller")
		}
		time.Sleep(time.Millisecond)
	}
	if len(item.output) != 2 {
		t.Fatal("overflow frame was queued")
	}
	if !bytes.Equal(<-item.output, payload) {
		t.Fatal("opaque binary frame changed")
	}
}
func TestHTMLParserAndCSP(t *testing.T) {
	output, err := RewriteHTML(embedassets.Read("login.html"), "https://bridge.example.test")
	if err == nil || output != nil {
		t.Fatal("unexpected entrance accepted")
	}
	source := `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="default-src 'self'; connect-src 'self'; script-src 'self'"><script type="module" src="/app.js"></script></head><body></body></html>`
	output, err = RewriteHTML([]byte(source), "https://bridge.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "wss://bridge.example.test") || !strings.Contains(string(output), "/cwb/browser.js") {
		t.Fatal("CSP or transport missing")
	}
}

func TestDeferredModulesPreloadWithoutExecuting(t *testing.T) {
	source := `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="default-src 'self'; connect-src 'self'; script-src 'self'">` +
		`<link rel="modulepreload" href="/vendor.js"><script type="module" crossorigin="anonymous" referrerpolicy="same-origin" src="/` + browserEntryAsset + `"></script>` +
		`<script type="module" src="/vendor.js"></script><script type="module">window.ready=true</script><script src="/classic.js"></script></head><body></body></html>`
	output, err := RewriteHTML([]byte(source), "https://chat.example.com")
	if err != nil {
		t.Fatal(err)
	}
	document, err := html.Parse(bytes.NewReader(output))
	if err != nil {
		t.Fatal(err)
	}
	preloads := map[string][]map[string]string{}
	modules := 0
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		attributes := map[string]string{}
		for _, attribute := range node.Attr {
			attributes[attribute.Key] = attribute.Val
		}
		if node.Type == html.ElementNode && node.Data == "link" && attributes["rel"] == "modulepreload" {
			preloads[attributes["href"]] = append(preloads[attributes["href"]], attributes)
		}
		if node.Type == html.ElementNode && node.Data == "script" && attributes["type"] == "application/cwb-module" {
			modules++
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	entry := "/" + browserEntryAsset + "?cwb=" + browserAssetRevision
	if modules != 3 || len(preloads) != 2 || len(preloads["/vendor.js"]) != 1 || len(preloads[entry]) != 1 {
		t.Fatal("module downloads must preload once while execution remains deferred", modules, preloads)
	}
	if preloads[entry][0]["crossorigin"] != "anonymous" || preloads[entry][0]["referrerpolicy"] != "same-origin" {
		t.Fatal("preload must preserve module request policy", preloads[entry])
	}
}
func TestExplicitProxyTrust(t *testing.T) {
	g := New(config.Default(), auth.New(false, nil), logger.New(io.Discard), 0)
	for _, item := range []struct {
		remote, forwarded, want string
		valid                   bool
	}{{"127.0.0.1:1000", "192.0.2.8", "192.0.2.8", true}, {"192.0.2.2:1000", "192.0.2.8", "192.0.2.2", true}, {"127.0.0.1:1000", "", "127.0.0.1", true}, {"127.0.0.1:1000", "invalid", "", false}} {
		request := httptest.NewRequest("GET", "https://chat.example.com/", nil)
		request.RemoteAddr = item.remote
		request.Header.Set("CF-Connecting-IP", item.forwarded)
		value, err := g.clientIP(request)
		if (err == nil) != item.valid || value != item.want {
			t.Fatal("proxy trust boundary")
		}
	}
}
func TestPublicTicketExchangeSecurity(t *testing.T) {
	record, err := auth.Hash(auth.Random())
	if err != nil {
		t.Fatal(err)
	}
	c := publicConfig()
	c.Auth.Password = record
	g := New(c, auth.New(false, record), logger.New(io.Discard), 0)
	defer g.Auth.RevokeAll()
	var wakes atomic.Int64
	g.SetHostLifecycle(func(ctx context.Context) error {
		wakes.Add(1)
		return ctx.Err()
	}, nil, nil)
	t.Cleanup(func() {
		if wakes.Load() != 0 {
			t.Error("ticket exchange must not wake the host")
		}
	})
	ticket, err := g.Auth.IssueTicket(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	pre := httptest.NewRecorder()
	g.ServeHTTP(pre, httptest.NewRequest("GET", c.PublicOrigin+"/auth/csrf", nil))
	body := []byte(`{"ticket":"` + ticket + `"}`)
	csrf := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(pre.Body.String()), `{"csrf":"`), `"}`)
	badOrigin := httptest.NewRequest("POST", c.PublicOrigin+"/auth/ticket", bytes.NewReader(body))
	badOrigin.Header.Set("Origin", "https://sibling.example.com")
	badOrigin.Header.Set("X-CSRF-Token", csrf)
	for _, cookie := range pre.Result().Cookies() {
		badOrigin.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, badOrigin)
	if rec.Code != 403 {
		t.Fatal("cross-origin ticket accepted", rec.Code)
	}
	good := httptest.NewRequest("POST", c.PublicOrigin+"/auth/ticket", bytes.NewReader(body))
	good.Header.Set("Origin", c.PublicOrigin)
	good.Header.Set("X-CSRF-Token", csrf)
	good.RemoteAddr = "127.0.0.1:1234"
	for _, cookie := range pre.Result().Cookies() {
		good.AddCookie(cookie)
	}
	rec = httptest.NewRecorder()
	g.ServeHTTP(rec, good)
	if rec.Code != 200 {
		t.Fatal("ticket exchange failed", rec.Code)
	}
	var gotSession bool
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "__Host-cwb_session" && cookie.Secure && cookie.HttpOnly {
			gotSession = true
		}
	}
	if !gotSession {
		t.Fatal("missing secure host session cookie")
	}
	replay := httptest.NewRequest("POST", c.PublicOrigin+"/auth/ticket", bytes.NewReader(body))
	replay.Header.Set("Origin", c.PublicOrigin)
	replay.Header.Set("X-CSRF-Token", csrf)
	replay.RemoteAddr = "127.0.0.1:1235"
	for _, cookie := range pre.Result().Cookies() {
		replay.AddCookie(cookie)
	}
	rec = httptest.NewRecorder()
	g.ServeHTTP(rec, replay)
	if rec.Code != 401 {
		t.Fatal("replayed ticket accepted", rec.Code)
	}
}

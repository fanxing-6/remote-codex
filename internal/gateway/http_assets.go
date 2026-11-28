package gateway

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

type gzipAsset struct {
	once sync.Once
	data []byte
}

type gzipAssetCache struct {
	mu      sync.Mutex
	buildID string
	entries map[string]*gzipAsset
}

func (c *gzipAssetCache) compressed(tag string, source []byte) []byte {
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]*gzipAsset)
	}
	entry := c.entries[tag]
	if entry == nil {
		entry = &gzipAsset{}
		c.entries[tag] = entry
	}
	c.mu.Unlock()
	entry.once.Do(func() { entry.data = compressAsset(source) })
	return entry.data
}

func (c *gzipAssetCache) bindBuildID(buildID string) {
	c.mu.Lock()
	if c.buildID != buildID {
		c.buildID = buildID
		c.entries = nil
	}
	c.mu.Unlock()
}

func compressAsset(source []byte) []byte {
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if err != nil {
		panic(err)
	}
	if _, err := writer.Write(source); err != nil {
		panic(err)
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	return compressed.Bytes()
}

func rewrittenAsset(name string) bool {
	switch name {
	case "index.html", "cwb/browser.js", "cwb/mobile.css", browserEntryAsset, browserComposerAsset, browserRouteAsset, browserDILFrameAsset, "assets/async/584602.d4ff4f180f.js", "assets/async/893680.05fde0ce2a.js":
		return true
	}
	return false
}

func staticAssetETag(buildID, name string, data []byte) string {
	digest := sha256.New()
	fmt.Fprintf(digest, "%s\x00%s\x00", buildID, name)
	if rewrittenAsset(name) {
		digest.Write([]byte(browserAssetRevision))
		digest.Write(data)
	} else if buildID == "" {
		digest.Write(data)
	}
	// gzip 与原始内容使用同一弱 ETag，条件请求由 ServeContent 处理。
	return fmt.Sprintf(`W/"%x"`, digest.Sum(nil))
}

func acceptsGzip(r *http.Request) bool {
	wildcard := false
	for _, value := range r.Header.Values("Accept-Encoding") {
		for _, coding := range strings.Split(value, ",") {
			name, parameters, err := mime.ParseMediaType(strings.TrimSpace(coding))
			if err != nil {
				continue
			}
			quality := 1.0
			if value, ok := parameters["q"]; ok {
				quality, err = strconv.ParseFloat(value, 64)
				if err != nil || !(quality >= 0 && quality <= 1) {
					continue
				}
			}
			if strings.EqualFold(name, "gzip") {
				return quality > 0
			}
			if name == "*" {
				wildcard = quality > 0
			}
		}
	}
	return wildcard
}

func compressibleAsset(name, contentType string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".ico", ".woff", ".woff2", ".br", ".gz", ".zip":
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return strings.HasPrefix(mediaType, "text/") || strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml") ||
		mediaType == "application/javascript" || mediaType == "application/x-javascript" || mediaType == "application/json" ||
		mediaType == "application/xml" || mediaType == "application/wasm"
}

func (c *gzipAssetCache) serveStaticAsset(w http.ResponseWriter, r *http.Request, buildID, name, contentType string, data []byte) {
	cacheControl := "private, max-age=86400"
	if buildID == "" || rewrittenAsset(name) {
		cacheControl = "private, no-cache"
	}
	w.Header().Set("ETag", staticAssetETag(buildID, name, data))
	serveAsset(w, r, name, contentType, cacheControl, data, c)
}

func serveAsset(w http.ResponseWriter, r *http.Request, name, contentType, cacheControl string, data []byte, cache *gzipAssetCache) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", cacheControl)
	if len(data) >= 1024 && compressibleAsset(name, contentType) {
		w.Header().Add("Vary", "Accept-Encoding")
		if acceptsGzip(r) && w.Header().Get("Content-Encoding") == "" {
			if tag := w.Header().Get("ETag"); cache != nil && tag != "" {
				data = cache.compressed(tag, data)
			} else {
				data = compressAsset(data)
			}
			w.Header().Set("Content-Encoding", "gzip")
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if w.Header().Get("ETag") != "" {
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
		return
	}
	if r.Method != http.MethodHead {
		w.Write(data)
	}
}

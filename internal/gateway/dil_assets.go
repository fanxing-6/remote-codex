package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"remote-codex/internal/auth"
	"remote-codex/internal/logger"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/sync/singleflight"
)

const dilOrigin = "https://cdn.platform.openai.com"
const dilPrefix = "/cwb/dil/"
const dilAssetLimit = 8 << 20

var dilRunnerPath = regexp.MustCompile(`^deployments/dil/v15/runner\.html$`)
var dilModulePath = regexp.MustCompile(`^assets/dil/[A-Za-z0-9_-]+\.(js|css|wasm)$`)

type dilAsset struct {
	body        []byte
	contentType string
	expires     time.Time
}

type dilAssets struct {
	mu           sync.Mutex
	capabilities map[string]string
	cache        map[string]dilAsset
	requests     singleflight.Group
	client       *http.Client
}

func (g *Gateway) dilBasePath(session *auth.Session) string {
	d := &g.dil
	d.mu.Lock()
	defer d.mu.Unlock()
	for token, id := range d.capabilities {
		if g.Auth.Get(id) == nil {
			delete(d.capabilities, token)
		} else if id == session.ID {
			return dilPrefix + token + "/"
		}
	}
	if d.capabilities == nil {
		d.capabilities = make(map[string]string)
	}
	// 数量受登录会话上限约束；凭据只授权读取公开的 DIL 资源。
	token := auth.Random()
	d.capabilities[token] = session.ID
	return dilPrefix + token + "/"
}

func allowedDILPath(name string) bool {
	return dilRunnerPath.MatchString(name) || dilModulePath.MatchString(name)
}

func (d *dilAssets) fetch(ctx context.Context, name string) (dilAsset, error) {
	if !allowedDILPath(name) {
		return dilAsset{}, errors.New("DIL_PATH_REJECTED")
	}
	d.mu.Lock()
	asset, ok := d.cache[name]
	d.mu.Unlock()
	if ok && time.Now().Before(asset.expires) {
		return asset, nil
	}
	result, err, _ := d.requests.Do(name, func() (any, error) {
		client := d.client
		if client == nil {
			client = &http.Client{Timeout: 20 * time.Second}
		}
		copyClient := *client
		copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, dilOrigin+"/"+name, nil)
		if err != nil {
			return nil, err
		}
		// Cookie、Origin、Referer 和客户端标识均不传送至上游。
		response, err := copyClient.Do(request)
		if err != nil {
			return nil, errors.New("DIL_UPSTREAM_UNAVAILABLE")
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("DIL_UPSTREAM_STATUS_%d", response.StatusCode)
		}
		contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if err != nil {
			return nil, errors.New("DIL_CONTENT_TYPE_INVALID")
		}
		valid := (dilRunnerPath.MatchString(name) && contentType == "text/html") ||
			(strings.HasSuffix(name, ".js") && (contentType == "text/javascript" || contentType == "application/javascript")) ||
			(strings.HasSuffix(name, ".css") && contentType == "text/css") ||
			(strings.HasSuffix(name, ".wasm") && contentType == "application/wasm")
		if !valid {
			return nil, errors.New("DIL_CONTENT_TYPE_REJECTED")
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, dilAssetLimit+1))
		if err != nil {
			return nil, errors.New("DIL_BODY_INCOMPLETE")
		}
		if len(body) > dilAssetLimit {
			return nil, errors.New("DIL_BODY_LIMIT")
		}
		asset := dilAsset{body: body, contentType: contentType, expires: time.Now().Add(5 * time.Minute)}
		d.mu.Lock()
		if d.cache == nil {
			d.cache = make(map[string]dilAsset)
		}
		for key, value := range d.cache {
			if time.Now().After(value.expires) {
				delete(d.cache, key)
			}
		}
		if len(d.cache) >= 32 {
			d.mu.Unlock()
			return nil, errors.New("DIL_CACHE_LIMIT")
		}
		d.cache[name] = asset
		d.mu.Unlock()
		return asset, nil
	})
	if err != nil {
		return dilAsset{}, err
	}
	return result.(dilAsset), nil
}

func rewriteDILRunner(source []byte, base string) ([]byte, error) {
	document, err := html.Parse(bytes.NewReader(source))
	if err != nil {
		return nil, err
	}
	modules := 0
	bootstraps := 0
	var walk func(*html.Node) error
	walk = func(node *html.Node) error {
		if node.Type == html.ElementNode && node.Data == "script" {
			external := false
			for index, attribute := range node.Attr {
				if attribute.Key != "src" {
					continue
				}
				name := strings.TrimPrefix(attribute.Val, "/")
				if !dilModulePath.MatchString(name) {
					return errors.New("DIL_MODULE_REJECTED")
				}
				node.Attr[index].Val = base + name
				modules++
				external = true
			}
			if !external {
				var source strings.Builder
				for text := node.FirstChild; text != nil; text = text.NextSibling {
					source.WriteString(text.Data)
				}
				digest := sha256.Sum256([]byte(source.String()))
				// v15 仅运行其 CSP 授权的 bootstrap，过滤 CDN 添加的挑战脚本。
				if base64.StdEncoding.EncodeToString(digest[:]) != "1jrO9354XvTqYOM/3p4VL6ADhhgTZ3Lp8RE/yCyqGIc=" {
					node.Parent.RemoveChild(node)
					return nil
				}
				bootstraps++
			}
		}
		for child := node.FirstChild; child != nil; {
			next := child.NextSibling
			if err := walk(child); err != nil {
				return err
			}
			child = next
		}
		return nil
	}
	if err := walk(document); err != nil {
		return nil, err
	}
	if modules != 1 || bootstraps != 1 {
		return nil, errors.New("DIL_RUNNER_UNSUPPORTED")
	}
	var output bytes.Buffer
	if err := html.Render(&output, document); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (g *Gateway) serveDIL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "METHOD_REJECTED", 405)
		return
	}
	token, name, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, dilPrefix), "/")
	if !ok || r.URL.RawQuery != "" || r.URL.RawPath != "" || !allowedDILPath(name) {
		http.Error(w, "DIL_PATH_REJECTED", 403)
		return
	}
	g.dil.mu.Lock()
	id := g.dil.capabilities[token]
	g.dil.mu.Unlock()
	if id == "" || g.Auth.Get(id) == nil {
		http.Error(w, "AUTH_REQUIRED", 401)
		return
	}
	asset, err := g.dil.fetch(r.Context(), name)
	if err != nil {
		g.Log.Add(logger.Event{Event: "dil_asset_failed", ErrorCode: "DIL_ASSET_UNAVAILABLE", Reason: err.Error()})
		http.Error(w, "DIL_ASSET_UNAVAILABLE", 502)
		return
	}
	base := dilPrefix + token + "/"
	body := asset.body
	if dilRunnerPath.MatchString(name) {
		body, err = rewriteDILRunner(body, base)
		if err != nil {
			http.Error(w, "DIL_RUNNER_UNSUPPORTED", 502)
			return
		}
	} else {
		body = bytes.ReplaceAll(body, []byte(dilOrigin+"/assets/dil/"), []byte(g.Config.PublicOrigin+base+"assets/dil/"))
	}
	// 沙盒保持 opaque origin；模块请求只获准读取这些静态资源。
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'; sandbox allow-scripts")
	w.Header().Set("ETag", fmt.Sprintf(`W/"%x"`, sha256.Sum256(body)))
	serveAsset(w, r, name, asset.contentType, "private, max-age=300", body, &g.assets)
}

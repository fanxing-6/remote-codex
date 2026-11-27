package gateway

import (
	"bytes"
	"errors"
)

const browserAssetRevision = "mobile-focus-v2"
const browserEntryAsset = "assets/348410.35ea11b6a0.js"
const browserComposerAsset = "assets/async/839700.c1fe868f96.js"

// 官方资源只在网关响应中适配，移动浏览器复用内置附件传输通道。
func rewriteBrowserAsset(name string, source []byte) ([]byte, error) {
	if name == browserEntryAsset {
		original := []byte(`import("../"+p.u(e))`)
		if bytes.Count(source, original) != 1 {
			return nil, errors.New("BROWSER_ATTACHMENT_ASSET_UNSUPPORTED")
		}
		return bytes.Replace(source, original, []byte(`import("../"+p.u(e)+(e===839700?"?cwb=`+browserAssetRevision+`":""))`), 1), nil
	}
	if name != browserComposerAsset {
		return source, nil
	}
	for _, item := range []struct {
		old, replacement string
		count            int
	}{
		{"onPickBrowserFiles:void 0", "onPickBrowserFiles:window.__cwbRuntime.mobile?e=>pe({...e,imagesOnly:false}):void 0", 1},
		{"supportsFileAttachments:vH", "supportsFileAttachments:window.__cwbRuntime.mobile||vH", 1},
	} {
		if bytes.Count(source, []byte(item.old)) != item.count {
			return nil, errors.New("BROWSER_ATTACHMENT_ASSET_UNSUPPORTED")
		}
		source = bytes.ReplaceAll(source, []byte(item.old), []byte(item.replacement))
	}
	return source, nil
}

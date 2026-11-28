package gateway

import (
	"bytes"
	"errors"
)

const browserAssetRevision = "mobile-floating-work-v2"
const browserEntryAsset = "assets/348410.35ea11b6a0.js"
const browserComposerAsset = "assets/async/839700.c1fe868f96.js"
const browserRouteAsset = "assets/async/191628.7c4ce6e8ec.js"
const browserDILFrameAsset = "assets/async/882456.cabb45e999.js"

func browserBuildID(buildID string) string { return buildID + "." + browserAssetRevision }

// 官方资源只在网关响应中适配，移动浏览器复用内置附件传输通道。
func rewriteBrowserAsset(name string, source []byte) ([]byte, error) {
	if name == browserEntryAsset {
		original := []byte(`import("../"+p.u(e))`)
		if bytes.Count(source, original) != 1 {
			return nil, errors.New("BROWSER_ATTACHMENT_ASSET_UNSUPPORTED")
		}
		return bytes.Replace(source, original, []byte(`import("../"+p.u(e)+([839700,191628,584602,893680,882456].includes(e)?"?cwb=`+browserAssetRevision+`":""))`), 1), nil
	}
	if name == browserRouteAsset {
		original := []byte(`G.startsWith("/space/")||tN.W.navigate(G),pL.O.routeChanged(L,G,X)`)
		if bytes.Count(source, original) != 1 {
			return nil, errors.New("BROWSER_ROUTE_ASSET_UNSUPPORTED")
		}
		return bytes.Replace(source, original, append([]byte(`window.__cwbRuntime.rememberRoute(G),`), original...), 1), nil
	}
	if name == browserDILFrameAsset {
		original := []byte(`if(t.origin===window.location.origin)throw Error("The DIL runner frame must use a different origin")`)
		if bytes.Count(source, original) != 1 {
			return nil, errors.New("BROWSER_DIL_FRAME_UNSUPPORTED")
		}
		// 网关响应强制使用 opaque sandbox，允许当前会话的唯一 runner 地址。
		return bytes.Replace(source, original, []byte(`if(t.origin===window.location.origin&&!window.__cwbRuntime.isDILRunner(t))throw Error("The DIL runner frame must use a different origin")`), 1), nil
	}
	if name == "assets/async/584602.d4ff4f180f.js" || name == "assets/async/893680.05fde0ce2a.js" {
		return rewriteDILReference(source)
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

func rewriteDILReference(source []byte) ([]byte, error) {
	original := []byte("https://cdn.platform.openai.com/deployments/dil/v${")
	if bytes.Count(source, original) != 1 {
		return nil, errors.New("BROWSER_DIL_ASSET_UNSUPPORTED")
	}
	return bytes.Replace(source, original, []byte("${location.origin}${window.__cwbConfiguration.dilBasePath}deployments/dil/v${"), 1), nil
}

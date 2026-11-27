package gateway

import (
	"bytes"
	"errors"
	csp "github.com/templarbit/go-csp-util"
	"golang.org/x/net/html"
	"net/url"
	"strings"
)

func RewriteHTML(source []byte, origin string) ([]byte, error) {
	document, err := html.Parse(bytes.NewReader(source))
	if err != nil {
		return nil, err
	}
	var head *html.Node
	var viewport *html.Node
	policies := 0
	scripts := 0
	var walk func(*html.Node) error
	walk = func(node *html.Node) error {
		if node.Type == html.ElementNode && node.Data == "head" {
			head = node
		}
		if node.Type == html.ElementNode && node.Data == "script" {
			scripts++
			for index, attribute := range node.Attr {
				if attribute.Key == "src" && attribute.Val == "/"+browserEntryAsset {
					node.Attr[index].Val += "?cwb=" + browserAssetRevision
				}
			}
			for index, attribute := range node.Attr {
				if attribute.Key == "type" && attribute.Val == "module" {
					node.Attr[index].Val = "application/cwb-module"
				}
			}
		}
		if node.Type == html.ElementNode && node.Data == "meta" {
			for _, attribute := range node.Attr {
				if attribute.Key == "name" && strings.EqualFold(attribute.Val, "viewport") {
					if viewport != nil {
						return errors.New("HTML_VIEWPORT_DUPLICATE")
					}
					viewport = node
				}
			}
			isCSP := false
			for _, attribute := range node.Attr {
				if attribute.Key == "http-equiv" && strings.EqualFold(attribute.Val, "Content-Security-Policy") {
					isCSP = true
				}
			}
			if isCSP {
				policies++
				found := false
				for i, attribute := range node.Attr {
					if attribute.Key != "content" {
						continue
					}
					directives, err := csp.ParseDirectives(attribute.Val)
					if err != nil {
						return nilOrError("CSP_INVALID")
					}
					hasConnect := false
					for _, directive := range directives {
						if directive.Name == "connect-src" {
							hasConnect = true
						}
					}
					if !hasConnect {
						return nilOrError("CSP_CONNECT_MISSING")
					}
					parsed, err := url.Parse(origin)
					if err != nil {
						return err
					}
					scheme := "wss://"
					if parsed.Scheme == "http" {
						scheme = "ws://"
					}
					if err = directives.AddDirective(csp.Directive{Name: "connect-src", Value: []string{scheme + parsed.Host}}); err != nil {
						return err
					}
					node.Attr[i].Val = directives.String()
					found = true
				}
				if !found {
					return nilOrError("CSP_CONTENT_MISSING")
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err = walk(document); err != nil {
		return nil, err
	}
	if head == nil || policies != 1 || scripts == 0 {
		return nil, errors.New("HTML_STRUCTURE_UNSUPPORTED")
	}
	if viewport == nil {
		viewport = &html.Node{Type: html.ElementNode, Data: "meta", Attr: []html.Attribute{{Key: "name", Val: "viewport"}}}
		head.AppendChild(viewport)
	}
	attributes := viewport.Attr[:0]
	for _, attribute := range viewport.Attr {
		if attribute.Key != "content" {
			attributes = append(attributes, attribute)
		}
	}
	viewport.Attr = append(attributes, html.Attribute{Key: "content", Val: "width=device-width, initial-scale=1.0, viewport-fit=cover, interactive-widget=resizes-content"})
	for _, item := range []struct{ tag, key, value string }{{"link", "href", "/cwb/mobile.css?cwb=" + browserAssetRevision}, {"script", "src", "/cwb/configuration.js"}, {"script", "src", "/cwb/browser.js?cwb=" + browserAssetRevision}} {
		node := &html.Node{Type: html.ElementNode, Data: item.tag, Attr: []html.Attribute{{Key: item.key, Val: item.value}}}
		if item.tag == "link" {
			node.Attr = append(node.Attr, html.Attribute{Key: "rel", Val: "stylesheet"})
		}
		head.AppendChild(node)
	}
	var output bytes.Buffer
	if err = html.Render(&output, document); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
func nilOrError(code string) error { return errors.New(code) }

package gateway

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestViewportRewrite(t *testing.T) {
	for _, meta := range []string{
		`<meta name="viewport" content="width=device-width, initial-scale=1.0">`,
		`<meta name="VIEWPORT" content="width=980, maximum-scale=1">`,
		`<meta name="viewport">`,
		"",
	} {
		source := `<!doctype html><html><head>` + meta + `<meta http-equiv="Content-Security-Policy" content="default-src 'self'; connect-src 'self'; script-src 'self'"><script type="module" src="/app.js"></script></head><body></body></html>`
		output, err := RewriteHTML([]byte(source), "https://bridge.example.test")
		if err != nil {
			t.Fatal(err)
		}
		for attempt := 0; attempt < 2; attempt++ {
			doc, err := html.Parse(bytes.NewReader(output))
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			var walk func(*html.Node)
			walk = func(node *html.Node) {
				if node.Type == html.ElementNode && node.Data == "meta" {
					values := map[string]string{}
					for _, a := range node.Attr {
						values[a.Key] = a.Val
					}
					if strings.EqualFold(values["name"], "viewport") {
						count++
						if values["content"] != "width=device-width, initial-scale=1.0, viewport-fit=cover, interactive-widget=resizes-content" {
							t.Fatalf("viewport: %q", values["content"])
						}
					}
				}
				for child := node.FirstChild; child != nil; child = child.NextSibling {
					walk(child)
				}
			}
			walk(doc)
			if count != 1 {
				t.Fatalf("viewport count: %d", count)
			}
			output, err = RewriteHTML(output, "https://bridge.example.test")
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestDuplicateViewportRejected(t *testing.T) {
	source := `<html><head><meta name="viewport"><meta name="viewport"><meta http-equiv="Content-Security-Policy" content="connect-src 'self'"><script type="module" src="/app.js"></script></head></html>`
	if _, err := RewriteHTML([]byte(source), "https://bridge.example.test"); err == nil {
		t.Fatal("duplicate viewport accepted")
	}
}

package logger

import (
	"bytes"
	"encoding/json"
	"remote-codex/internal/embedassets"
	"testing"
)

func TestHTTPErrorWhitelist(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output)
	input := embedassets.Read("browser.js")
	logger.HTTPErrorLog().Print(string(input))
	var event map[string]any
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if len(event) != 3 || event["event"] != "http_server_error" || event["errorCode"] != "HTTP_SERVER_ERROR" {
		t.Fatal("HTTP error whitelist")
	}
	if bytes.Contains(output.Bytes(), input) {
		t.Fatal("HTTP input retained")
	}
}

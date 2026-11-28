package logger

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"remote-codex/internal/embedassets"
	"testing"
	"time"
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
	if len(event) != 5 || event["event"] != "http_server_error" || event["errorCode"] != "HTTP_SERVER_ERROR" || event["reason"] != "unclassified" || event["errorClass"] != "http_server" {
		t.Fatal("HTTP error whitelist")
	}
	if bytes.Contains(output.Bytes(), input) {
		t.Fatal("HTTP input retained")
	}
}

func TestHTTPAbortedTLSAndHandlerPanicDiagnostics(t *testing.T) {
	l := New(io.Discard)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("private handler context") }))
	server.Config.ErrorLog = l.HTTPErrorLog()
	server.StartTLS()
	defer server.Close()
	connection, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	connection.Close()
	response, err := server.Client().Get(server.URL)
	if response != nil {
		response.Body.Close()
	}
	if err == nil {
		t.Fatal("handler panic did not terminate the response")
	}
	deadline := time.Now().Add(time.Second)
	for len(l.Events()) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	var aborted, panicked bool
	for _, event := range l.Events() {
		if event.Event == "http_client_disconnected" && event.Reason == "handshake_aborted" && event.ErrorCode == "" {
			aborted = true
		}
		if event.ErrorCode == "HTTP_SERVER_ERROR" && event.Reason == "handler_panic" && event.ErrorClass == "handler" {
			panicked = true
		}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("private handler context")) || bytes.Contains(data, []byte(server.Listener.Addr().String())) {
			t.Fatal("diagnostic included request context")
		}
	}
	if !aborted || !panicked {
		t.Fatal(l.Events())
	}
}

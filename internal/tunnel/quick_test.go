package tunnel

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestQuickURLValidation(t *testing.T) {
	for _, origin := range []string{"https://a.trycloudflare.com", "https://quiet-green-bridge.trycloudflare.com", "https://" + strings.Repeat("a", 63) + ".trycloudflare.com"} {
		if !ValidQuickOrigin(origin) || ParseQuickURL("INF |  "+origin+"  |") != origin {
			t.Fatalf("valid URL rejected: %q", origin)
		}
		if ParseQuickURL(`{"message":"`+origin+`"}`) != origin {
			t.Fatal("JSON log URL rejected")
		}
	}
	for _, origin := range []string{
		"http://a.trycloudflare.com", "https://trycloudflare.com", "https://a.b.trycloudflare.com",
		"https://a.trycloudflare.com.evil.example", "https://a.trycloudflare.com@evil.example",
		"https://user@a.trycloudflare.com", "https://a.trycloudflare.com:443", "https://a.trycloudflare.com/",
		"https://a.trycloudflare.com?ticket=x", "https://a.trycloudflare.com#x", "https://-a.trycloudflare.com",
		"https://a-.trycloudflare.com", "https://A.trycloudflare.com", "https://a_try.trycloudflare.com",
		"https://" + strings.Repeat("a", 64) + ".trycloudflare.com", "https://a.trycloudflare.com/path",
	} {
		if ValidQuickOrigin(origin) || ParseQuickURL("INF "+origin+" ") != "" {
			t.Fatalf("invalid URL accepted: %q", origin)
		}
	}
}

func TestCloudflaredNotFound(t *testing.T) {
	_, err := findCloudflared([]string{"", "./missing-cloudflared-executable"})
	if err == nil || err.Error() != "CLOUDFLARED_NOT_FOUND" {
		t.Fatal(err)
	}
}

func TestQuickInputValidation(t *testing.T) {
	for _, item := range []struct {
		port     int
		protocol string
	}{{80, "http2"}, {65536, "http2"}, {19765, "tcp"}} {
		if _, err := StartQuick(context.Background(), t.TempDir(), item.port, item.protocol, time.Second); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
}

// 此测试连接 Cloudflare 公共服务，显式启用后使用真实 cloudflared 验证进程回收。
func TestQuickProcessIntegration(t *testing.T) {
	if os.Getenv("REMOTE_CODEX_NETWORK_TESTS") != "1" {
		t.Skip("set REMOTE_CODEX_NETWORK_TESTS=1 to test the public Quick Tunnel service")
	}
	if _, err := FindCloudflared(); err != nil {
		t.Fatal(err)
	}
	q, err := StartQuick(context.Background(), t.TempDir(), 19765, "http2", 45*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Close)
	if !ValidQuickOrigin(q.Origin) {
		t.Fatal("invalid allocated origin")
	}
	if err := q.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-q.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("process not reaped after unexpected exit")
	}
	if q.Err() == nil {
		t.Fatal("unexpected exit omitted")
	}
	q.Close()
	q.Close()
	if q.cmd.ProcessState == nil || !errors.Is(q.cmd.Process.Signal(os.Interrupt), os.ErrProcessDone) {
		t.Fatal("child process not reaped")
	}
	if _, err := StartQuick(context.Background(), t.TempDir(), 19765, "http2", time.Nanosecond); err == nil || err.Error() != "QUICK_START_TIMEOUT" {
		t.Fatalf("startup deadline: %v", err)
	}
}

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"remote-codex/internal/config"
)

func TestBootstrapperSelection(t *testing.T) {
	t.Setenv("REMOTE_CODEX_BOOTSTRAP_COMMAND", "")
	if _, ok := NewBootstrapper(config.Default(), t.TempDir(), "").(*PythonBootstrapper); !ok {
		t.Fatal("CLI bootstrapper unavailable")
	}
	t.Setenv("REMOTE_CODEX_BOOTSTRAP_COMMAND", "external-bootstrapper")
	b := NewBootstrapper(config.Default(), t.TempDir(), "").(*ExternalCommandBootstrapper)
	if b.Command != "external-bootstrapper" {
		t.Fatal("environment command ignored")
	}
	b = NewBootstrapper(config.Default(), t.TempDir(), "explicit-bootstrapper").(*ExternalCommandBootstrapper)
	if b.Command != "explicit-bootstrapper" {
		t.Fatal("explicit command precedence")
	}
}

func TestExternalCommandValidation(t *testing.T) {
	for _, command := range []string{"", "[]", "[", `[""]`, `[42]`} {
		b := &ExternalCommandBootstrapper{Command: command}
		if _, err := b.Discover(context.Background()); err == nil {
			t.Fatalf("invalid command accepted: %s", command)
		}
	}
	b := &ExternalCommandBootstrapper{Command: "./missing-bootstrapper"}
	if _, err := b.Discover(context.Background()); err == nil {
		t.Fatal("missing executable accepted")
	}
}

// 仅执行真实的 Node-Frida 进程发现，不进行宿主挂载。
func TestExternalNodeDiscoveryIntegration(t *testing.T) {
	command := os.Getenv("REMOTE_CODEX_BOOTSTRAP_TEST_COMMAND")
	if command == "" {
		t.Skip("set REMOTE_CODEX_BOOTSTRAP_TEST_COMMAND to the Electron Node bootstrapper argument array")
	}
	b := &ExternalCommandBootstrapper{Command: command}
	identity, err := b.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeIdentity(data)
	if err != nil || decoded != identity {
		t.Fatal("identity round trip", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = b.Discover(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation not propagated", err)
	}
	options := AttachOptions{PID: identity.PID, StartedAt: identity.StartedAt, AppVersion: identity.AppVersion}
	if _, err = b.Attach(context.Background(), identity, identity.AsarPath, options); err == nil {
		t.Fatal("invalid credentials accepted")
	}
}

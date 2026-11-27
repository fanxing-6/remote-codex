package host

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"remote-codex/internal/auth"
	"remote-codex/internal/config"
	"remote-codex/internal/embedassets"
	"sync"
	"time"
)

type Adapter struct {
	bootstrapper Bootstrapper
}

var prepareMu sync.Mutex

func New(c config.Config, runtimeDir string, command ...string) IHostAdapter {
	value := ""
	if len(command) > 0 {
		value = command[0]
	}
	return &Adapter{bootstrapper: NewBootstrapper(c, runtimeDir, value)}
}

func (a *Adapter) FindRunningProcess(ctx context.Context) (ProcessIdentity, error) {
	return a.bootstrapper.Discover(ctx)
}
func (a *Adapter) BootstrapperName() string { return a.bootstrapper.Name() }

func (a *Adapter) Attach(ctx context.Context, identity ProcessIdentity, assetBuildID, bundlePath string) (*Client, error) {
	if !VersionSupported(identity.AppVersion) {
		return nil, errors.New("APP_VERSION_UNVERIFIED")
	}
	info, err := os.Stat(identity.AsarPath)
	if err != nil {
		return nil, err
	}
	if float64(info.ModTime().UnixNano())/1e9 > identity.StartedAt {
		return nil, errors.New("APP_UPDATED_RESTART_REQUIRED")
	}
	if err = config.AtomicWrite(bundlePath, embedassets.Read("shim.cjs"), 0600); err != nil {
		return nil, err
	}
	token, instance := auth.Random(), auth.Random()
	options := AttachOptions{PID: identity.PID, StartedAt: identity.StartedAt, Token: token, Instance: instance, AppVersion: identity.AppVersion, AssetBuildID: assetBuildID}
	endpoint, err := a.bootstrapper.Attach(ctx, identity, bundlePath, options)
	if err != nil {
		return nil, err
	}
	client, err := Connect(endpoint)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		result, err := client.Call(ctx, map[string]any{"kind": "probe"})
		if err != nil {
			client.Close()
			return nil, err
		}
		if result["ready"] == true {
			return client, nil
		}
		select {
		case <-ctx.Done():
			client.Close()
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	client.Close()
	return nil, errors.New("SHIM_READY_TIMEOUT")
}

func (a *Adapter) Detach(ctx context.Context, client *Client) error {
	result, err := client.Call(ctx, map[string]any{"kind": "stop"})
	client.Close()
	if err != nil {
		return err
	}
	if result["windowReleased"] != true {
		return errors.New("HIDDEN_WINDOW_NOT_RELEASED")
	}
	initial, ok := result["initialWindows"].([]any)
	if !ok {
		return errors.New("WINDOW_SNAPSHOT_MISSING")
	}
	remaining, ok := result["remainingWindows"].([]any)
	if !ok {
		return errors.New("WINDOW_SNAPSHOT_MISSING")
	}
	for _, before := range initial {
		found := false
		for _, after := range remaining {
			if before == after {
				found = true
				break
			}
		}
		if !found {
			return errors.New("ORIGINAL_WINDOWS_CHANGED")
		}
	}
	endpoint, err := url.Parse(client.Endpoint.URL)
	if err != nil {
		return err
	}
	for {
		connection, dialErr := net.DialTimeout("tcp", endpoint.Host, 200*time.Millisecond)
		if dialErr != nil {
			return nil
		}
		connection.Close()
		select {
		case <-ctx.Done():
			return errors.New("IPC_PORT_NOT_RELEASED")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (a *Adapter) ProbeRuntime(ctx context.Context, client *Client) error {
	identity, err := a.bootstrapper.Discover(ctx)
	if err != nil {
		return err
	}
	if identity.PID != client.Endpoint.PID || identity.StartedAt != client.Endpoint.StartedAt {
		return errors.New("PROCESS_CHANGED")
	}
	if identity.AppVersion != client.Endpoint.AppVersion {
		return errors.New("APP_VERSION_CHANGED")
	}
	result, err := client.Call(ctx, map[string]any{"kind": "probe"})
	if err != nil {
		return err
	}
	if result["hiddenWindowAlive"] != true || result["shimInstanceId"] != client.Endpoint.ShimInstanceID {
		return errors.New("SHIM_IDENTITY_CHANGED")
	}
	return nil
}

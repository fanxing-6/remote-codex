package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"remote-codex/internal/config"
)

type AttachOptions struct {
	PID          int     `json:"pid"`
	StartedAt    float64 `json:"startedAt"`
	Token        string  `json:"token"`
	Instance     string  `json:"instance"`
	AppVersion   string  `json:"appVersion"`
	AssetBuildID string  `json:"assetBuildId"`
}

type Bootstrapper interface {
	Discover(context.Context) (ProcessIdentity, error)
	Attach(context.Context, ProcessIdentity, string, AttachOptions) (ShimEndpoint, error)
	Name() string
}

// Command 使用可执行文件路径或 JSON 参数数组，路径中的空格由 JSON 保留。
type ExternalCommandBootstrapper struct {
	Command string
	Label   string
}

func (b *ExternalCommandBootstrapper) Name() string {
	if b.Label != "" {
		return b.Label
	}
	return "external-command"
}

func (b *ExternalCommandBootstrapper) command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	value := strings.TrimSpace(b.Command)
	var argv []string
	if strings.HasPrefix(value, "[") {
		if err := json.Unmarshal([]byte(value), &argv); err != nil {
			return nil, err
		}
	} else if value != "" {
		argv = []string{value}
	}
	if len(argv) == 0 || argv[0] == "" {
		return nil, errors.New("BOOTSTRAP_COMMAND_REQUIRED")
	}
	command := exec.CommandContext(ctx, argv[0], append(argv[1:], args...)...)
	prepareHelper(command)
	return command, nil
}

func decodeIdentity(data []byte) (ProcessIdentity, error) {
	var identity ProcessIdentity
	if err := json.Unmarshal(data, &identity); err != nil {
		return identity, err
	}
	if identity.PID <= 0 || identity.StartedAt <= 0 || identity.AppVersion == "" || !filepath.IsAbs(identity.ExecutablePath) || !filepath.IsAbs(identity.AsarPath) {
		return identity, errors.New("BOOTSTRAP_IDENTITY_INVALID")
	}
	return identity, nil
}

func commandOutput(command *exec.Cmd) ([]byte, error) {
	data, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("BOOTSTRAP_COMMAND_FAILED: %w", err)
	}
	return data, nil
}

func (b *ExternalCommandBootstrapper) Discover(ctx context.Context) (ProcessIdentity, error) {
	command, err := b.command(ctx, "--discover")
	if err != nil {
		return ProcessIdentity{}, err
	}
	data, err := commandOutput(command)
	if err != nil {
		return ProcessIdentity{}, err
	}
	return decodeIdentity(data)
}

func runAttach(command *exec.Cmd, identity ProcessIdentity, options AttachOptions) (ShimEndpoint, error) {
	data, err := json.Marshal(options)
	if err != nil {
		return ShimEndpoint{}, err
	}
	command.Stdin = bytes.NewReader(data)
	data, err = commandOutput(command)
	if err != nil {
		return ShimEndpoint{}, err
	}
	var endpoint ShimEndpoint
	if err = json.Unmarshal(data, &endpoint); err != nil {
		return endpoint, err
	}
	if endpoint.ProcessIdentity != identity || !endpoint.InspectorClosed {
		return endpoint, errors.New("BOOTSTRAP_IDENTITY_MISMATCH")
	}
	address, err := url.Parse(endpoint.URL)
	if err != nil {
		return endpoint, err
	}
	port, err := strconv.Atoi(address.Port())
	if err != nil || port < 1 || port > 65535 || address.Scheme != "ws" || address.Hostname() != "127.0.0.1" || address.User != nil || address.Path != "/internal" || address.RawQuery != "" || address.Fragment != "" {
		return endpoint, errors.New("BOOTSTRAP_ENDPOINT_INVALID")
	}
	endpoint.IPCToken = options.Token
	endpoint.ShimInstanceID = options.Instance
	endpoint.AssetBuildID = options.AssetBuildID
	endpoint.ProtocolVersion = 1
	return endpoint, nil
}

func (b *ExternalCommandBootstrapper) Attach(ctx context.Context, identity ProcessIdentity, shimPath string, options AttachOptions) (ShimEndpoint, error) {
	command, err := b.command(ctx, "--attach", "--bundle", shimPath)
	if err != nil {
		return ShimEndpoint{}, err
	}
	return runAttach(command, identity, options)
}

type PythonBootstrapper struct {
	config     config.Config
	runtimeDir string
}

func (b *PythonBootstrapper) Name() string { return "python" }
func (b *PythonBootstrapper) python() (string, error) {
	return config.ResolvePython(b.config.BootstrapPython, filepath.Dir(b.runtimeDir))
}
func (b *PythonBootstrapper) Attach(ctx context.Context, identity ProcessIdentity, shimPath string, options AttachOptions) (ShimEndpoint, error) {
	path, err := b.prepare()
	if err != nil {
		return ShimEndpoint{}, err
	}
	python, err := b.python()
	if err != nil {
		return ShimEndpoint{}, err
	}
	command := exec.CommandContext(ctx, python, path, "--bundle", shimPath)
	prepareHelper(command)
	return runAttach(command, identity, options)
}

func NewBootstrapper(c config.Config, runtimeDir, command string) Bootstrapper {
	if command == "" {
		command = os.Getenv("REMOTE_CODEX_BOOTSTRAP_COMMAND")
	}
	if command != "" {
		return &ExternalCommandBootstrapper{Command: command, Label: os.Getenv("REMOTE_CODEX_BOOTSTRAPPER_NAME")}
	}
	return &PythonBootstrapper{config: c, runtimeDir: runtimeDir}
}

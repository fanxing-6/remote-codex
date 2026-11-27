//go:build windows

package host

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"remote-codex/internal/config"
	"strconv"
)

//go:embed windows_bootstrap.py
var windowsBootstrap []byte

//go:embed windows_bootstrap.js
var windowsAgent []byte

func (a *PythonBootstrapper) prepare() (string, error) {
	prepareMu.Lock()
	defer prepareMu.Unlock()
	path := filepath.Join(a.runtimeDir, "windows_bootstrap.py")
	if err := config.AtomicWrite(filepath.Join(a.runtimeDir, "windows_bootstrap.js"), windowsAgent, 0600); err != nil {
		return "", err
	}
	return path, config.AtomicWrite(path, windowsBootstrap, 0600)
}

func (a *PythonBootstrapper) Discover(ctx context.Context) (ProcessIdentity, error) {
	return a.findProcess(ctx, 0)
}

func (a *PythonBootstrapper) findProcess(ctx context.Context, pid int) (ProcessIdentity, error) {
	path, err := a.prepare()
	if err != nil {
		return ProcessIdentity{}, err
	}
	arguments := []string{path, "--discover"}
	if pid > 0 {
		arguments = append(arguments, "--pid", strconv.Itoa(pid))
	}
	python, err := a.python()
	if err != nil {
		return ProcessIdentity{}, err
	}
	command := exec.CommandContext(ctx, python, arguments...)
	prepareHelper(command)
	data, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return ProcessIdentity{}, ctx.Err()
		}
		var failure *exec.ExitError
		if errors.As(err, &failure) {
			for _, code := range []string{"INTERACTIVE_SESSION_REQUIRED", "MAIN_PROCESS_NOT_UNIQUE", "PACKAGE_IDENTITY_INVALID", "APP_VERSION_MISSING", "APP_VERSION_NOT_UNIQUE", "ASAR_MISSING"} {
				if bytes.Contains(failure.Stderr, []byte("RuntimeError: "+code)) {
					return ProcessIdentity{}, errors.New(code)
				}
			}
		}
		return ProcessIdentity{}, errors.New("HOST_PROCESS_UNAVAILABLE")
	}
	var identity ProcessIdentity
	err = json.Unmarshal(data, &identity)
	return identity, err
}

func (a *Adapter) DiscoverCandidates(ctx context.Context) ([]AppCandidate, error) {
	identity, err := a.FindRunningProcess(ctx)
	if err != nil {
		return nil, err
	}
	return []AppCandidate{{Platform: "windows", Arch: "amd64", ProductID: "OpenAI.Codex", AppVersion: identity.AppVersion, ExecutablePath: identity.ExecutablePath, AsarPath: identity.AsarPath, SignatureStatus: "MSIX package"}}, nil
}

//go:build darwin

package host

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"remote-codex/internal/config"
	"strings"
)

//go:embed bootstrap.py
var bootstrap []byte

//go:embed darwin_inspector.js
var darwinAgent []byte

func (a *PythonBootstrapper) prepare() (string, error) {
	prepareMu.Lock()
	defer prepareMu.Unlock()
	path := filepath.Join(a.runtimeDir, "bootstrap.py")
	if err := config.AtomicWrite(filepath.Join(a.runtimeDir, "darwin_inspector.js"), darwinAgent, 0600); err != nil {
		return "", err
	}
	return path, config.AtomicWrite(path, bootstrap, 0600)
}

func (a *Adapter) DiscoverCandidates(ctx context.Context) ([]AppCandidate, error) {
	data, err := exec.CommandContext(ctx, "mdfind", "kMDItemCFBundleIdentifier == 'com.openai.codex'").Output()
	if err != nil {
		return nil, errors.New("DISCOVERY_FAILED")
	}
	paths := append(strings.Split(strings.TrimSpace(string(data)), "\n"), "/Applications/ChatGPT.app")
	seen := map[string]bool{}
	var candidates []AppCandidate
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if _, err := os.Stat(filepath.Join(path, "Contents/Resources/app.asar")); err != nil {
			continue
		}
		version, err := exec.CommandContext(ctx, "/usr/libexec/PlistBuddy", "-c", "Print :CFBundleShortVersionString", filepath.Join(path, "Contents/Info.plist")).Output()
		if err != nil {
			return nil, err
		}
		signature := "valid"
		if exec.CommandContext(ctx, "codesign", "--verify", "--deep", "--strict", path).Run() != nil {
			signature = "invalid"
		}
		candidates = append(candidates, AppCandidate{Platform: "darwin", Arch: "arm64", ProductID: "com.openai.codex", AppVersion: strings.TrimSpace(string(version)), ExecutablePath: filepath.Join(path, "Contents/MacOS/ChatGPT"), AsarPath: filepath.Join(path, "Contents/Resources/app.asar"), SignatureStatus: signature})
	}
	return candidates, nil
}

func (a *PythonBootstrapper) Discover(ctx context.Context) (ProcessIdentity, error) {
	path, err := a.prepare()
	if err != nil {
		return ProcessIdentity{}, err
	}
	python, err := a.python()
	if err != nil {
		return ProcessIdentity{}, err
	}
	data, err := exec.CommandContext(ctx, python, path, "--discover").Output()
	if err != nil {
		return ProcessIdentity{}, errors.New("HOST_PROCESS_UNAVAILABLE")
	}
	var identity ProcessIdentity
	err = json.Unmarshal(data, &identity)
	return identity, err
}

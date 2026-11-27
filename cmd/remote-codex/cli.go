package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"remote-codex/internal/config"
	"remote-codex/internal/host"
)

type cliCredentials struct {
	ID   string `json:"id"`
	CSRF string `json:"csrf"`
}

func cliRequest(c config.Config, runtimeDir, path string, body any) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(runtimeDir, "cli-session.json"))
	if err != nil {
		return nil, errors.New("SERVICE_NOT_RUNNING")
	}
	var credentials cliCredentials
	if err = json.Unmarshal(data, &credentials); err != nil {
		return nil, err
	}
	method := "GET"
	var input io.Reader
	if body != nil {
		method = "POST"
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
		input = bytes.NewReader(data)
	}
	origin := "http://127.0.0.1:" + strconv.Itoa(c.AdminPort)
	request, err := http.NewRequest(method, origin+"/api/"+path, input)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Origin", origin)
	request.Header.Set("X-CSRF-Token", credentials.CSRF)
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: "cwb_admin", Value: credentials.ID})
	client := &http.Client{Timeout: 90 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("SERVICE_UNAVAILABLE")
	}
	defer response.Body.Close()
	data, err = io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("ADMIN_STATUS_%d", response.StatusCode)
	}
	return data, nil
}

func openUI(c config.Config, runtimeDir string) error {
	if _, err := cliRequest(c, runtimeDir, "bootstrap/rotate", map[string]any{}); err != nil {
		return err
	}
	code, err := os.ReadFile(filepath.Join(runtimeDir, "bootstrap.code"))
	if err != nil {
		return err
	}
	return openBrowser(fmt.Sprintf("http://127.0.0.1:%d/#bootstrap=%s", c.AdminPort, code))
}

func launchUI(c config.Config, configPath, runtimeDir, tunnelPath string, options serveOptions) error {
	if _, err := cliRequest(c, runtimeDir, "status", nil); err == nil {
		return openUI(c, runtimeDir)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	arguments := []string{"serve", "--config", configPath, "--tunnel-config", tunnelPath}
	if options.NoTray {
		arguments = append(arguments, "--no-tray")
	}
	if options.NoHost {
		arguments = append(arguments, "--no-host")
	}
	if options.CloudflaredPath != "" {
		arguments = append(arguments, "--cloudflared", options.CloudflaredPath)
	}
	if options.BootstrapCommand != "" {
		arguments = append(arguments, "--bootstrap-command", options.BootstrapCommand)
	}
	process := exec.Command(executable, arguments...)
	if err = host.PrepareDetached(process); err != nil {
		return err
	}
	if err = process.Start(); err != nil {
		return err
	}
	go process.Wait()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err = cliRequest(c, runtimeDir, "status", nil); err == nil {
			return openUI(c, runtimeDir)
		}
		if time.Now().After(deadline) {
			return errors.New("SERVICE_START_TIMEOUT")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

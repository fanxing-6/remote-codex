package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"remote-codex/internal/config"
	"remote-codex/internal/host"
)

func openBrowser(url string) error { return exec.Command("open", url).Run() }
func defaultConfigPath(home string) (string, error) {
	return filepath.Join(home, "Library/Application Support/remote-codex/config.json"), nil
}
func ownerTerminal() (*os.File, error) { return os.OpenFile("/dev/tty", os.O_WRONLY, 0) }
func platformSignature(ctx context.Context, identity host.ProcessIdentity) map[string]any {
	ok := exec.CommandContext(ctx, "codesign", "--verify", "--deep", "--strict", filepath.Dir(filepath.Dir(filepath.Dir(identity.ExecutablePath)))).Run() == nil
	return map[string]any{"label": "官方应用签名", "ok": ok, "detail": map[bool]string{true: "codesign 校验通过", false: "签名校验未通过，请检查官方安装包"}[ok]}
}
func platformAutoStart(enabled bool, configPath, tunnelPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	filename := filepath.Join(home, "Library/LaunchAgents/com.remote-codex.bridge.plist")
	if !enabled {
		if _, err := os.Stat(filename); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err := exec.Command("launchctl", "disable", fmt.Sprintf("gui/%d/com.remote-codex.bridge", os.Getuid())).Run(); err != nil {
			return err
		}
		return os.Remove(filename)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	var executableXML, configXML bytes.Buffer
	if err = xml.EscapeText(&executableXML, []byte(executable)); err != nil {
		return err
	}
	if err = xml.EscapeText(&configXML, []byte(configPath)); err != nil {
		return err
	}
	content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>com.remote-codex.bridge</string><key>ProgramArguments</key><array><string>%s</string><string>serve</string><string>--config</string><string>%s</string><string>--no-tray</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><true/></dict></plist>`, executableXML.String(), configXML.String())
	if err = config.AtomicWrite(filename, []byte(content), 0600); err != nil {
		return err
	}
	return exec.Command("launchctl", "enable", fmt.Sprintf("gui/%d/com.remote-codex.bridge", os.Getuid())).Run()
}

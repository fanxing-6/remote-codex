package main

import (
	"context"
	_ "embed"
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"remote-codex/internal/config"
	"remote-codex/internal/host"
	"strings"
	"syscall"
)

//go:embed autostart_windows.ps1
var autoStartScript []byte

func openBrowser(url string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}
func defaultConfigPath(home string) (string, error) {
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "remote-codex", "config.json"), nil
}
func ownerTerminal() (*os.File, error) { return os.OpenFile("CONOUT$", os.O_WRONLY, 0) }
func platformSignature(ctx context.Context, identity host.ProcessIdentity) map[string]any {
	location := strings.ReplaceAll(filepath.Dir(filepath.Dir(identity.ExecutablePath)), "'", "''")
	script := "$ErrorActionPreference='Stop'; $p=@(Get-AppxPackage -Name OpenAI.Codex | Where-Object { $_.InstallLocation -eq '" + location + "' }); if ($p.Count -ne 1 -or $p[0].Status -ne 'Ok' -or $p[0].SignatureKind -eq 'None') { exit 1 }"
	command := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	ok := command.Run() == nil
	return map[string]any{"label": "MSIX 签名与包状态", "ok": ok, "detail": map[bool]string{true: "Windows 注册包签名及状态有效", false: "请检查 OpenAI.Codex 的 Windows 包状态"}[ok]}
}
func platformAutoStart(enabled bool, configPath, tunnelPath string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	filename := filepath.Join(filepath.Dir(configPath), "runtime", "autostart.ps1")
	if err = config.AtomicWrite(filename, autoStartScript, 0600); err != nil {
		return err
	}
	mode := "Disable"
	if enabled {
		mode = "Enable"
	}
	command := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filename, "-Mode", mode, "-Executable", executable, "-ConfigPath", configPath, "-TunnelPath", tunnelPath)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if err = command.Run(); err != nil {
		return errors.New("SCHEDULED_TASK_UPDATE_FAILED")
	}
	return nil
}

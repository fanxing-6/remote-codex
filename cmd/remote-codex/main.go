package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"remote-codex/internal/auth"
	"remote-codex/internal/config"
	"remote-codex/internal/host"
	"remote-codex/internal/tunnel"
)

type serveOptions struct {
	NoTray           bool
	NoHost           bool
	CloudflaredPath  string
	BootstrapCommand string
}

func run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	defaultPath, err := defaultConfigPath(home)
	if err != nil {
		return err
	}
	command, args := "ui", os.Args[1:]
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		command, args = args[0], args[1:]
	}
	if command == "auth" && len(args) > 0 && args[0] == "revoke-all" {
		command, args = "revoke-all", args[1:]
	}
	flags := flag.NewFlagSet("remote-codex", flag.ContinueOnError)
	configPath := flags.String("config", defaultPath, "JSON configuration")
	options := serveOptions{}
	flags.BoolVar(&options.NoTray, "no-tray", false, "Run without the native tray")
	flags.BoolVar(&options.NoHost, "no-host", false, "Connect the host manually from the control panel")
	flags.StringVar(&options.CloudflaredPath, "cloudflared", "", "cloudflared executable path")
	flags.StringVar(&options.BootstrapCommand, "bootstrap-command", "", "Bootstrap executable path or JSON argument array")
	tunnelPath := flags.String("tunnel-config", filepath.Join(home, ".cloudflared/config.yml"), "Tunnel configuration path")
	ticketTTL := flags.Int("ttl", 300, "One-time login link TTL in seconds (60-900)")
	permanentTicket := flags.Bool("permanent", false, "Issue a permanent reusable login link")
	if err = flags.Parse(args); err != nil {
		return err
	}
	c, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	runtimeDir := filepath.Join(filepath.Dir(*configPath), "runtime")
	switch command {
	case "serve":
		return serve(c, *configPath, runtimeDir, *tunnelPath, options)
	case "ui":
		return launchUI(c, *configPath, runtimeDir, *tunnelPath, options)
	case "init":
		if _, err := os.Stat(*configPath); err == nil {
			return errors.New("CONFIG_ALREADY_EXISTS")
		}
		password := auth.Random()
		record, err := auth.Hash(password)
		if err != nil {
			return err
		}
		c.Auth.Password = record
		terminal, err := ownerTerminal()
		if err != nil {
			return errors.New("INIT_REQUIRES_TERMINAL")
		}
		defer terminal.Close()
		if err = config.Save(*configPath, c); err != nil {
			return err
		}
		fmt.Fprintln(terminal, "Owner Password：", password)
		return nil
	case "status", "doctor":
		data, err := cliRequest(c, runtimeDir, command, nil)
		if err != nil {
			if command == "doctor" {
				a := &application{config: c, adapter: host.New(c, runtimeDir, options.BootstrapCommand), provider: tunnel.NewProvider(c, runtimeDir, options.CloudflaredPath)}
				return json.NewEncoder(os.Stdout).Encode(a.doctor())
			}
			return err
		}
		fmt.Println(string(data))
		return nil
	case "bootstrap":
		data, err := cliRequest(c, runtimeDir, "bootstrap/code", nil)
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	case "stop", "revoke-all":
		path := "shutdown"
		if command == "revoke-all" {
			path = "auth/revoke-all"
		}
		_, err = cliRequest(c, runtimeDir, path, map[string]any{})
		return err
	case "ticket":
		data, err := cliRequest(c, runtimeDir, "auth/ticket", map[string]any{"ttlSeconds": *ticketTTL, "permanent": *permanentTicket})
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	default:
		return errors.New("COMMAND_UNSUPPORTED")
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "remote-codex:", err)
		os.Exit(1)
	}
}

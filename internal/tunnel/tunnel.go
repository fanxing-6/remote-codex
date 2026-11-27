package tunnel

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/pmezard/go-difflib/difflib"
	"gopkg.in/yaml.v3"
	"net/url"
	"os"
	"remote-codex/internal/config"
	"strings"
	"sync"
	"time"
)

type Preview struct {
	Diff       string `json:"diff"`
	Digest     string `json:"digest"`
	DNSCommand string `json:"dnsCommand"`
	Content    string `json:"-"`
	Original   []byte `json:"-"`
}

var writeMu sync.Mutex

func field(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
func scalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}
func Plan(filename, hostname string, dataPort, adminPort int) (Preview, error) {
	parsed, err := url.Parse("https://" + hostname)
	if err != nil || parsed.Hostname() != hostname || parsed.Port() != "" || strings.ContainsAny(hostname, " /\r\n\t'") || dataPort == adminPort || dataPort < 1024 || dataPort > 65535 {
		return Preview{}, errors.New("TUNNEL_TARGET_INVALID")
	}
	info, err := os.Lstat(filename)
	if err != nil {
		return Preview{}, err
	}
	if !info.Mode().IsRegular() {
		return Preview{}, errors.New("TUNNEL_SYMLINK_DENIED")
	}
	input, err := os.ReadFile(filename)
	if err != nil {
		return Preview{}, err
	}
	if len(input) > 1<<20 {
		return Preview{}, errors.New("TUNNEL_SIZE_LIMIT")
	}
	var document yaml.Node
	if err = yaml.Unmarshal(input, &document); err != nil {
		return Preview{}, errors.New("TUNNEL_YAML_INVALID")
	}
	if len(document.Content) != 1 {
		return Preview{}, errors.New("TUNNEL_DOCUMENT_INVALID")
	}
	root := document.Content[0]
	ingress := field(root, "ingress")
	tunnel := field(root, "tunnel")
	if ingress == nil || ingress.Kind != yaml.SequenceNode || len(ingress.Content) == 0 || tunnel == nil {
		return Preview{}, errors.New("TUNNEL_INGRESS_INVALID")
	}
	last := ingress.Content[len(ingress.Content)-1]
	service := field(last, "service")
	if service == nil || service.Value != "http_status:404" || field(last, "hostname") != nil {
		return Preview{}, errors.New("TUNNEL_CATCHALL_REQUIRED")
	}
	for _, rule := range ingress.Content {
		if host := field(rule, "hostname"); host != nil && host.Value == hostname {
			return Preview{}, errors.New("TUNNEL_HOST_EXISTS")
		}
		if target := field(rule, "service"); target != nil {
			u, err := url.Parse(target.Value)
			if err == nil && u.Port() == fmt.Sprint(adminPort) {
				return Preview{}, errors.New("TUNNEL_ADMIN_EXPOSURE")
			}
		}
	}
	rule := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{scalar("hostname"), scalar(hostname), scalar("service"), scalar(fmt.Sprintf("http://127.0.0.1:%d", dataPort))}}
	encoded, err := yaml.Marshal(rule)
	if err != nil {
		return Preview{}, err
	}
	lines := strings.SplitAfter(string(input), "\n")
	index := last.Line - 1
	if index < 0 || index >= len(lines) {
		return Preview{}, errors.New("TUNNEL_LAYOUT_UNSUPPORTED")
	}
	column := strings.Index(lines[index], "-")
	if column < 0 || strings.TrimSpace(lines[index][:column]) != "" {
		return Preview{}, errors.New("TUNNEL_LAYOUT_UNSUPPORTED")
	}
	ending := "\n"
	if strings.HasSuffix(lines[index], "\r\n") {
		ending = "\r\n"
	}
	parts := strings.Split(strings.TrimSuffix(string(encoded), "\n"), "\n")
	var insertion strings.Builder
	for number, line := range parts {
		insertion.WriteString(strings.Repeat(" ", column))
		if number == 0 {
			insertion.WriteString("- ")
		} else {
			insertion.WriteString("  ")
		}
		insertion.WriteString(line)
		insertion.WriteString(ending)
	}
	output := []byte(strings.Join(lines[:index], "") + insertion.String() + strings.Join(lines[index:], ""))
	var verified yaml.Node
	if err = yaml.Unmarshal(output, &verified); err != nil {
		return Preview{}, errors.New("TUNNEL_YAML_INVALID")
	}
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{A: difflib.SplitLines(string(input)), B: difflib.SplitLines(string(output)), FromFile: "config.yml", ToFile: "config.yml proposed", Context: 3})
	if err != nil {
		return Preview{}, err
	}
	hash := sha256.New()
	hash.Write(input)
	hash.Write(output)
	return Preview{Diff: diff, Digest: hex.EncodeToString(hash.Sum(nil)), DNSCommand: "cloudflared tunnel route dns " + tunnel.Value + " " + hostname, Content: string(output), Original: input}, nil
}
func Apply(filename string, preview Preview) (string, error) {
	writeMu.Lock()
	defer writeMu.Unlock()
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("TUNNEL_FILE_INVALID")
	}
	current, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	if string(current) != string(preview.Original) {
		return "", errors.New("TUNNEL_PREVIEW_STALE")
	}
	var document yaml.Node
	if yaml.Unmarshal([]byte(preview.Content), &document) != nil {
		return "", errors.New("TUNNEL_YAML_INVALID")
	}
	backup := filename + ".bak." + time.Now().UTC().Format("20060102T150405.000000000")
	if err = config.AtomicWrite(backup, current, 0600); err != nil {
		return "", err
	}
	if err = config.AtomicWrite(filename, []byte(preview.Content), info.Mode().Perm()); err != nil {
		return "", err
	}
	return backup, nil
}

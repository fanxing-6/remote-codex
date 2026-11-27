package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"remote-codex/internal/config"
)

var quickOrigin = regexp.MustCompile(`^https://[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.trycloudflare\.com$`)
var logURLs = regexp.MustCompile(`https://[^\s"<>]+`)

func ValidQuickOrigin(origin string) bool { return quickOrigin.MatchString(origin) }

func ParseQuickURL(line string) string {
	for _, candidate := range logURLs.FindAllString(line, -1) {
		if ValidQuickOrigin(candidate) {
			return candidate
		}
	}
	return ""
}

func findCloudflared(candidates []string) (string, error) {
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", errors.New("CLOUDFLARED_NOT_FOUND")
}

func FindCloudflared(paths ...string) (string, error) {
	candidates := append(append([]string{}, paths...), os.Getenv("REMOTE_CODEX_CLOUDFLARED"), "cloudflared")
	if runtime.GOOS == "darwin" {
		candidates = append(candidates, "/opt/homebrew/bin/cloudflared", "/usr/local/bin/cloudflared")
	}
	if runtime.GOOS == "windows" {
		for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LOCALAPPDATA")} {
			if base != "" {
				candidates = append(candidates, filepath.Join(base, "cloudflared", "cloudflared.exe"))
			}
		}
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			candidates = append(candidates, filepath.Join(base, "Microsoft", "WinGet", "Links", "cloudflared.exe"))
		}
		if base := os.Getenv("ChocolateyInstall"); base != "" {
			candidates = append(candidates, filepath.Join(base, "bin", "cloudflared.exe"))
		}
		if base := os.Getenv("ProgramData"); base != "" {
			candidates = append(candidates, filepath.Join(base, "chocolatey", "bin", "cloudflared.exe"))
		}
		if base := os.Getenv("USERPROFILE"); base != "" {
			candidates = append(candidates, filepath.Join(base, "scoop", "shims", "cloudflared.exe"))
		}
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			candidates = append(candidates, filepath.Join(base, "Programs", "cloudflared", "cloudflared.exe"))
		}
	}
	return findCloudflared(candidates)
}

type Quick struct {
	Origin     string
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	done       chan struct{}
	mu         sync.Mutex
	err        error
	startError string
}

// StartQuick 的 context 控制子进程的整个生命周期；timeout 仅限制取得地址的等待时间。
func StartQuick(ctx context.Context, runtimeDir string, port int, protocol string, timeout time.Duration) (*Quick, error) {
	return startQuick(ctx, runtimeDir, port, protocol, timeout, "")
}

func startQuick(ctx context.Context, runtimeDir string, port int, protocol string, timeout time.Duration, cloudflaredPath string) (*Quick, error) {
	if port < 1024 || port > 65535 {
		return nil, errors.New("QUICK_PORT_INVALID")
	}
	if protocol == "" {
		protocol = "http2"
	}
	if protocol != "http2" && protocol != "quic" && protocol != "auto" {
		return nil, errors.New("QUICK_PROTOCOL_INVALID")
	}
	binary, err := FindCloudflared(cloudflaredPath)
	if err != nil {
		return nil, err
	}
	filename, err := filepath.Abs(filepath.Join(runtimeDir, "quick-tunnel.yml"))
	if err != nil {
		return nil, err
	}
	if err = config.AtomicWrite(filename, []byte("{}\n"), 0600); err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(life, binary, "tunnel", "--config", filename, "--no-autoupdate", "--protocol", protocol, "--metrics", "127.0.0.1:0", "--url", fmt.Sprintf("http://127.0.0.1:%d", port))
	prepareQuick(command)
	// 清除可覆盖命令参数的 Tunnel 环境变量，保留网络代理等用户环境。
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(name), "TUNNEL_") {
			command.Env = append(command.Env, entry)
		}
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err = command.Start(); err != nil {
		stderr.Close()
		cancel()
		return nil, errors.New("QUICK_START_FAILED")
	}
	release, err := trackQuick(command)
	if err != nil {
		cancel()
		command.Wait()
		return nil, err
	}
	q := &Quick{cmd: command, cancel: cancel, done: make(chan struct{})}
	urls := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "error code: 1015") {
				q.mu.Lock()
				q.startError = "QUICK_RATE_LIMITED"
				q.mu.Unlock()
			}
			if origin := ParseQuickURL(scanner.Text()); origin != "" {
				select {
				case urls <- origin:
				default:
				}
			}
		}
		if scanner.Err() != nil {
			cancel()
		}
		waitErr := command.Wait()
		q.mu.Lock()
		q.err = errors.Join(scanner.Err(), waitErr, release())
		q.mu.Unlock()
		close(q.done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case origin := <-urls:
		q.Origin = origin
		return q, nil
	case <-q.done:
		cancel()
		q.mu.Lock()
		code := q.startError
		q.mu.Unlock()
		if code != "" {
			return nil, errors.New(code)
		}
		return nil, errors.New("QUICK_EXITED_BEFORE_URL")
	case <-timer.C:
		q.Close()
		return nil, errors.New("QUICK_START_TIMEOUT")
	case <-ctx.Done():
		q.Close()
		return nil, ctx.Err()
	}
}

func (q *Quick) Done() <-chan struct{} { return q.done }
func (q *Quick) Err() error            { q.mu.Lock(); defer q.mu.Unlock(); return q.err }
func (q *Quick) PID() int              { return q.cmd.Process.Pid }
func (q *Quick) Close() {
	if q == nil {
		return
	}
	q.cancel()
	<-q.done
}

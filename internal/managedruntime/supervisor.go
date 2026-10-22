package managedruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/3899/ncmm/internal/filelock"
)

const RestartExitCode = 75

type SupervisorOptions struct {
	Root           string
	Image          string
	ImageVersion   string
	Home           string
	Args           []string
	Output         io.Writer
	StartupTimeout time.Duration
	StopTimeout    time.Duration
	PollInterval   time.Duration
}

type childProcess struct {
	cmd  *exec.Cmd
	done chan error
}

func runtimeEnvironment(root string) []string {
	result := make([]string, 0, len(os.Environ())+2)
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, "NCMM_RUNTIME_MANAGED=") || strings.HasPrefix(item, "NCMM_RUNTIME_DIR=") {
			continue
		}
		result = append(result, item)
	}
	return append(result, "NCMM_RUNTIME_MANAGED=1", "NCMM_RUNTIME_DIR="+root)
}

func startChild(options SupervisorOptions, slot Slot) (*childProcess, error) {
	path, err := location(options.Root, slot)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path, options.Args...)
	cmd.Env = runtimeEnvironment(options.Root)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = options.Output, options.Output, os.Stdin
	cmd.Dir = options.Home
	prepareChild(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	child := &childProcess{cmd: cmd, done: make(chan error, 1)}
	go func() { child.done <- cmd.Wait() }()
	return child, nil
}

func stopChild(child *childProcess, timeout time.Duration) {
	_ = signalChild(child.cmd, false)
	select {
	case <-child.done:
		return
	case <-time.After(timeout):
		_ = signalChild(child.cmd, true)
		<-child.done
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return 1
}

func healthy(client *http.Client, home string, pid int, version string) bool {
	data, err := os.ReadFile(filepath.Join(home, "webui.instance.lock"))
	if err != nil || len(data) > 16<<10 {
		return false
	}
	var metadata struct {
		PID        int    `json:"pid"`
		InstanceID string `json:"instanceId"`
		Listen     string `json:"listen"`
	}
	if json.Unmarshal(data, &metadata) != nil || metadata.PID != pid || metadata.InstanceID == "" {
		return false
	}
	host, port, err := net.SplitHostPort(metadata.Listen)
	if err != nil || port == "0" {
		return false
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return false
	}
	response, err := client.Get("http://" + net.JoinHostPort(host, port) + "/api/v1/instance")
	if err != nil {
		return false
	}
	defer response.Body.Close()
	var instance struct {
		InstanceID string `json:"instanceId"`
		Version    string `json:"version"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&instance) != nil {
		return false
	}
	return instance.InstanceID == metadata.InstanceID && strings.TrimPrefix(instance.Version, "v") == strings.TrimPrefix(version, "v")
}

func awaitHealth(ctx context.Context, child *childProcess, options SupervisorOptions, slot Slot) error {
	deadline := time.NewTimer(options.StartupTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(options.PollInterval)
	defer ticker.Stop()
	// 不经过外部代理，探测仅连接本实例记录的回环地址。
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	successes := 0
	for {
		select {
		case <-ctx.Done():
			stopChild(child, options.StopTimeout)
			return ctx.Err()
		case err := <-child.done:
			return fmt.Errorf("新程序在健康确认前退出: %w", errors.Join(err, fmt.Errorf("exit %d", exitCode(err))))
		case <-deadline.C:
			stopChild(child, options.StopTimeout)
			return fmt.Errorf("新程序启动健康检查超时")
		case <-ticker.C:
			if healthy(client, options.Home, child.cmd.Process.Pid, slot.Version) {
				successes++
				if successes >= 2 {
					return nil
				}
			} else {
				successes = 0
			}
		}
	}
}

// Serve 是容器内唯一监督者；候选程序通过实例和版本检查后才成为统一 CLI 入口。
func Serve(ctx context.Context, options SupervisorOptions) error {
	if options.Output == nil {
		options.Output = os.Stdout
	}
	if options.StartupTimeout <= 0 {
		options.StartupTimeout = 30 * time.Second
	}
	if options.StopTimeout <= 0 {
		options.StopTimeout = 5 * time.Second
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 250 * time.Millisecond
	}
	if err := checkRoot(options.Root); err != nil {
		return err
	}
	lease, err := filelock.TryAcquire(filepath.Join(options.Root, "launcher.lock"))
	if err != nil {
		return fmt.Errorf("此运行目录已有启动器: %w", err)
	}
	defer lease.Close()
	if err := Initialize(options.Root, options.Image, options.ImageVersion); err != nil {
		return err
	}
	failures := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		slot, err := Candidate(options.Root)
		if err != nil {
			return err
		}
		fmt.Fprintf(options.Output, "[launcher] 正在启动 %s\n", slot.Version)
		child, err := startChild(options, slot)
		if err == nil {
			err = awaitHealth(ctx, child, options, slot)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintf(options.Output, "[launcher] %s，尝试回滚\n", err)
			if rejectErr := Reject(options.Root, slot, err.Error()); rejectErr != nil {
				return errors.Join(err, rejectErr)
			}
			failures++
			if failures > 1 {
				return fmt.Errorf("回滚程序也未通过健康检查: %w", err)
			}
			continue
		}
		if err := Confirm(options.Root, slot); err != nil {
			stopChild(child, options.StopTimeout)
			return err
		}
		fmt.Fprintf(options.Output, "[launcher] %s 已通过健康检查\n", slot.Version)
		select {
		case <-ctx.Done():
			stopChild(child, options.StopTimeout)
			return nil
		case childErr := <-child.done:
			if exitCode(childErr) == RestartExitCode {
				failures = 0
				continue
			}
			if childErr == nil {
				return nil
			}
			if rejectErr := Reject(options.Root, slot, "运行程序异常退出: "+childErr.Error()); rejectErr != nil {
				return errors.Join(childErr, rejectErr)
			}
			failures++
			if failures > 1 {
				return childErr
			}
		}
	}
}

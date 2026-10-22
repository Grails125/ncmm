package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWebInstanceLockRejectsEquivalentHomeAndPreservesMetadata(t *testing.T) {
	parent := t.TempDir()
	home := filepath.Join(parent, "home")
	first, err := acquireWebInstanceLock(home)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := first.writeMetadata("127.0.0.1:3899", "1.2.0")
	if err != nil {
		t.Fatal(err)
	}

	equivalentHome := filepath.Join(home, "..", filepath.Base(home))
	_, err = acquireWebInstanceLock(equivalentHome)
	if !errors.Is(err, errWebUIAlreadyRunning) {
		t.Fatalf("second instance error = %v; want already running", err)
	}
	var runningErr *instanceRunningError
	if !errors.As(err, &runningErr) {
		t.Fatalf("second instance error type = %T", err)
	}
	if runningErr.Metadata.PID != os.Getpid() || runningErr.Metadata.Listen != "127.0.0.1:3899" || runningErr.Metadata.InstanceID != metadata.InstanceID {
		t.Fatalf("unexpected diagnostic metadata: %+v", runningErr.Metadata)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := acquireWebInstanceLock(home)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWebInstanceLocksAllowDifferentHomes(t *testing.T) {
	first, err := acquireWebInstanceLock(filepath.Join(t.TempDir(), "first"))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := acquireWebInstanceLock(filepath.Join(t.TempDir(), "second"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
}

func TestServerStartsSchedulerOnlyAfterListenerIsBound(t *testing.T) {
	home := t.TempDir()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()

	server := newLifecycleTestServer(t, context.Background(), home, occupied.Addr().String())
	if server.scheduler.isActive() {
		t.Fatal("scheduler active before Server.Run")
	}
	err = server.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "bind WebUI listener") {
		t.Fatalf("Server.Run() error = %v; want listener bind error", err)
	}
	if server.scheduler.isActive() {
		t.Fatal("scheduler active after listener bind failure")
	}
}

func TestServerRunEphemeralPortPreservesHostValidation(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost"} {
		t.Run(host, func(t *testing.T) {
			home := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			server := newLifecycleTestServer(t, ctx, home, net.JoinHostPort(host, "0"))
			done := make(chan struct{})
			var runErr error
			go func() {
				runErr = server.Run(ctx)
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
					if runErr != nil {
						t.Errorf("Server.Run() shutdown: %v", runErr)
					}
				case <-time.After(5 * time.Second):
					t.Error("Server.Run() did not stop")
				}
			})

			var metadata instanceMetadata
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				var err error
				metadata, err = readInstanceMetadata(filepath.Join(home, InstanceLockFilename))
				if err == nil && metadata.Listen != "" && metadata.InstanceID != "" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			_, port, err := net.SplitHostPort(metadata.Listen)
			if err != nil || port == "0" || metadata.InstanceID == "" {
				t.Fatalf("listener did not publish an assigned port: metadata=%+v, err=%v", metadata, err)
			}

			client := &http.Client{Timeout: 2 * time.Second}
			defer client.CloseIdleConnections()
			for _, test := range []struct {
				name string
				host string
				want int
			}{
				{name: "assigned address", host: metadata.Listen, want: http.StatusOK},
				{name: "configured hostname", host: net.JoinHostPort(host, port), want: http.StatusOK},
				{name: "wrong hostname", host: net.JoinHostPort("attacker.example", port), want: http.StatusMisdirectedRequest},
				{name: "wrong port", host: net.JoinHostPort(host, "1"), want: http.StatusMisdirectedRequest},
			} {
				t.Run(test.name, func(t *testing.T) {
					req, err := http.NewRequest(http.MethodGet, "http://"+metadata.Listen+"/api/v1/instance", nil)
					if err != nil {
						t.Fatal(err)
					}
					req.Host = test.host
					response, err := client.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					defer response.Body.Close()
					if response.StatusCode != test.want {
						t.Fatalf("Host %q status = %d; want %d", test.host, response.StatusCode, test.want)
					}
					if test.want == http.StatusOK {
						var instance struct {
							InstanceID string `json:"instanceId"`
						}
						if err := json.NewDecoder(response.Body).Decode(&instance); err != nil {
							t.Fatal(err)
						}
						if instance.InstanceID != metadata.InstanceID {
							t.Fatalf("instance ID = %q; want %q", instance.InstanceID, metadata.InstanceID)
						}
					}
				})
			}

			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Server.Run() did not stop")
			}
			if server.opts.Listen != net.JoinHostPort(host, port) {
				t.Fatalf("listener changed configured hostname: got %q", server.opts.Listen)
			}
		})
	}
}

func TestServerRunRejectsSecondInstanceForSameHome(t *testing.T) {
	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	firstListen := availableListen(t)
	first := newLifecycleTestServer(t, ctx, home, firstListen)
	firstResult := make(chan error, 1)
	go func() { firstResult <- first.Run(ctx) }()

	metadata := waitForInstanceMetadata(t, filepath.Join(home, InstanceLockFilename), firstListen)
	deadline := time.Now().Add(5 * time.Second)
	for !first.scheduler.isActive() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !first.scheduler.isActive() {
		cancel()
		t.Fatalf("scheduler was not activated after listener bind; metadata=%+v", metadata)
	}

	second := newLifecycleTestServer(t, context.Background(), home, availableListen(t))
	err := second.Run(context.Background())
	if !errors.Is(err, errWebUIAlreadyRunning) {
		cancel()
		t.Fatalf("second Server.Run() error = %v; want already running", err)
	}
	if !strings.Contains(err.Error(), firstListen) || !strings.Contains(err.Error(), fmt.Sprintf("pid=%d", os.Getpid())) {
		cancel()
		t.Fatalf("second Server.Run() error lacks diagnostics: %v", err)
	}
	if second.scheduler.isActive() {
		cancel()
		t.Fatal("second instance activated its scheduler")
	}

	cancel()
	select {
	case err := <-firstResult:
		if err != nil {
			t.Fatalf("first Server.Run() shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first Server.Run() did not stop")
	}
	if first.scheduler.isActive() {
		t.Fatal("scheduler remained active after shutdown")
	}

	restarted, err := acquireWebInstanceLock(home)
	if err != nil {
		t.Fatalf("instance lock was not released after shutdown: %v", err)
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
}

func newLifecycleTestServer(t *testing.T, ctx context.Context, home, listen string) *Server {
	t.Helper()
	configPath := filepath.Join(home, "config.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := os.WriteFile(configPath, configForTest(), 0600); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	server, err := New(ctx, Options{
		Listen: listen, Home: home,
		ConfigPath: configPath, WebConfig: filepath.Join(home, "webui.yaml"),
		Executable: os.Args[0], Version: "1.2.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func availableListen(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return listen
}

func waitForInstanceMetadata(t *testing.T, path, listen string) instanceMetadata {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		metadata, err := readInstanceMetadata(path)
		if err == nil && metadata.Listen == listen && metadata.InstanceID != "" {
			return metadata
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("instance metadata %q was not ready", path)
	return instanceMetadata{}
}

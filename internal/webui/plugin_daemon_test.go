package webui

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type daemonTestPlugins struct {
	PluginManager
	items []PluginInfo
}

func (p *daemonTestPlugins) DiscoverPlugins() []PluginInfo {
	return append([]PluginInfo(nil), p.items...)
}

func newTestDaemons(t *testing.T) (*pluginDaemonManager, *daemonTestPlugins) {
	t.Helper()
	home := t.TempDir()
	plugins := &daemonTestPlugins{items: []PluginInfo{{Name: "demo", Type: "daemon", Path: os.Args[0]}, {Name: "task", Type: "task", Path: os.Args[0]}}}
	supervisor := newProcessSupervisor()
	supervisor.command = func(ctx context.Context, command string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessSupervisorHelperProcess$", "--", processSupervisorHelperMarker, "wait", filepath.Join(home, "ready"))
	}
	m, err := newPluginDaemonManager(context.Background(), Options{Home: home}, plugins, supervisor)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.close(); supervisor.Close(time.Second) })
	return m, plugins
}

func TestPluginDaemonLifecycleAndPersistentOptIn(t *testing.T) {
	m, plugins := newTestDaemons(t)
	m.startAutomatic()
	state, err := m.state("demo")
	if err != nil || state.Running || state.AutoStart {
		t.Fatalf("new daemon ran automatically: %+v %v", state, err)
	}
	if err := m.configure("demo", pluginDaemonSettings{AutoStart: true, Args: []string{"--example"}}); err != nil {
		t.Fatal(err)
	}
	state, _ = m.state("demo")
	if state.Running {
		t.Fatal("settings must not start daemon")
	}
	m.startAutomatic()
	waitForFile(t, filepath.Join(m.home, "ready"))
	if err := m.start("demo"); err != nil {
		t.Fatal(err)
	}
	state, _ = m.state("demo")
	if !state.Running {
		t.Fatal("not running")
	}
	if err := m.stop("demo"); err != nil {
		t.Fatal(err)
	}
	state, _ = m.state("demo")
	if state.Running {
		t.Fatal("stop returned before process exit")
	}
	loaded, err := newPluginDaemonManager(context.Background(), Options{Home: m.home}, plugins, newProcessSupervisor())
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.close()
	state, _ = loaded.state("demo")
	if !state.AutoStart || len(state.Args) != 1 || state.Running {
		t.Fatalf("bad persisted settings: %+v", state)
	}
	if err := m.start("task"); err == nil {
		t.Fatal("task accepted as daemon")
	}
	m.close()
	if err := m.start("demo"); err == nil {
		t.Fatal("start after close accepted")
	}
}

func TestPluginDaemonMutationStopsAndRestoresWithoutBlockingStatus(t *testing.T) {
	m, _ := newTestDaemons(t)
	if err := m.start("demo"); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- m.mutate("demo", true, func() error { close(entered); <-release; return nil }) }()
	<-entered
	stateDone := make(chan pluginDaemonState, 1)
	go func() { state, _ := m.state("demo"); stateDone <- state }()
	select {
	case state := <-stateDone:
		if state.Running {
			t.Fatal("mutation before stop")
		}
	case <-time.After(time.Second):
		t.Fatal("status blocked by mutation")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	state, _ := m.state("demo")
	if !state.Running {
		t.Fatal("daemon not resumed")
	}
	if err := m.mutate("demo", false, func() error { return os.ErrPermission }); err == nil {
		t.Fatal("mutation error lost")
	}
	state, _ = m.state("demo")
	if !state.Running {
		t.Fatal("failed uninstall did not restore")
	}
}

func TestPluginDaemonSchedulesAndManualRunsAreExcluded(t *testing.T) {
	m, plugins := newTestDaemons(t)
	validator := pluginTaskValidator(plugins)
	job := Schedule{ID: "legacy", Name: "daemon", Command: "plugin", Args: []string{"run", "demo"}, Cron: "@hourly", OverlapPolicy: "skip", Enabled: true}
	if err := validator(job); err == nil {
		t.Fatal("daemon accepted as task")
	}
	runner := newControlledRunManager(t, 1)
	runner.validateJob = validator
	if _, err := runner.start(context.Background(), job); err == nil {
		t.Fatal("manual daemon run accepted")
	}
	store, err := newWebConfigStore(filepath.Join(m.home, "webui.yaml"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.upsert(job); err != nil {
		t.Fatal(err)
	}
	scheduler, err := newScheduler(context.Background(), store, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduler.close()
	if len(scheduler.list()) != 0 {
		t.Fatal("legacy daemon schedule loaded")
	}
	if _, err := scheduler.upsert(job); err == nil || !strings.Contains(err.Error(), "守护") {
		t.Fatalf("new daemon schedule accepted: %v", err)
	}
	if _, ok := scheduler.get(job.ID); ok {
		t.Fatal("legacy schedule exposed to runNow")
	}
}

func TestPluginDaemonAPISettingsAndTaskBoundary(t *testing.T) {
	m, plugins := newTestDaemons(t)
	server := &Server{daemons: m, plugins: plugins}
	for _, body := range []string{`{"autoStart":true,"args":["--once"]}`, `{"autoStart":false}`} {
		request := httptest.NewRequest("PUT", "/api/v1/plugins/demo/daemon", strings.NewReader(body))
		request.SetPathValue("name", "demo")
		response := httptest.NewRecorder()
		server.handlePluginDaemon(response, request)
		if response.Code != 200 {
			t.Fatalf("settings status=%d %s", response.Code, response.Body.String())
		}
		var state pluginDaemonState
		if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		if state.Running {
			t.Fatal("saving settings executed plugin")
		}
	}
	request := httptest.NewRequest("POST", "/api/v1/plugins/demo/run", strings.NewReader(`{}`))
	request.SetPathValue("name", "demo")
	response := httptest.NewRecorder()
	server.handlePluginRun(response, request)
	if response.Code != 400 {
		t.Fatalf("daemon task status=%d", response.Code)
	}
	for _, action := range []string{"start", "stop"} {
		request := httptest.NewRequest("POST", "/api/v1/plugins/demo/"+action, nil)
		request.SetPathValue("name", "demo")
		request.SetPathValue("action", action)
		response := httptest.NewRecorder()
		server.handlePluginDaemonAction(response, request)
		if response.Code != 200 {
			t.Fatalf("%s status=%d %s", action, response.Code, response.Body.String())
		}
	}
}

func TestPluginDaemonUnexpectedExitDoesNotRestart(t *testing.T) {
	m, _ := newTestDaemons(t)
	var starts atomic.Int32
	m.supervisor.command = func(ctx context.Context, command string, args ...string) *exec.Cmd {
		starts.Add(1)
		return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessSupervisorHelperProcess$", "--", processSupervisorHelperMarker, "output")
	}
	if err := m.start("demo"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, _ := m.state("demo")
		if !state.Running {
			if state.Error == "" {
				t.Fatal("unexpected exit not reported")
			}
			if starts.Load() != 1 {
				t.Fatal("daemon restarted without user request")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon exit was not observed")
}

func TestPluginDaemonCloseStopsDescendantsAndReleasesExecutable(t *testing.T) {
	m, plugins := newTestDaemons(t)
	data, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(m.home, "daemon.exe")
	if err := os.WriteFile(executable, data, 0700); err != nil {
		t.Fatal(err)
	}
	plugins.items[0].Path = executable
	ready, escaped := filepath.Join(m.home, "ready-tree"), filepath.Join(m.home, "escaped")
	m.supervisor.command = exec.CommandContext
	if err := m.configure("demo", pluginDaemonSettings{Args: []string{"-test.run=^TestProcessSupervisorHelperProcess$", "--", processSupervisorHelperMarker, "tree-parent", ready, escaped}}); err != nil {
		t.Fatal(err)
	}
	if err := m.start("demo"); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, ready)
	m.close()
	if err := os.Rename(executable, executable+".old"); err != nil {
		t.Fatalf("executable still locked after close: %v", err)
	}
	time.Sleep(800 * time.Millisecond)
	if _, err := os.Stat(escaped); !os.IsNotExist(err) {
		t.Fatal("daemon descendant survived close")
	}
}

func TestPluginDaemonLogRetainsBoundedOutputAfterExit(t *testing.T) {
	m, _ := newTestDaemons(t)
	m.supervisor.command = func(ctx context.Context, command string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessSupervisorHelperProcess$", "--", processSupervisorHelperMarker, "output")
	}
	if err := m.start("demo"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, _ := m.state("demo")
		if !state.Running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	output, err := m.logs("demo")
	if err != nil || len(output) != 4096 || output != strings.Repeat("x", 4096) {
		t.Fatalf("last output lost: size=%d err=%v", len(output), err)
	}
	server := &Server{daemons: m}
	request := httptest.NewRequest("GET", "/api/v1/plugins/demo/daemon/log", nil)
	request.SetPathValue("name", "demo")
	response := httptest.NewRecorder()
	server.handlePluginDaemonLog(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"output"`) {
		t.Fatalf("log status=%d %s", response.Code, response.Body.String())
	}
}

func TestPluginDaemonLogRequiresAuthentication(t *testing.T) {
	server := newAuthTestServer(t, t.TempDir())
	response := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/v1/plugins/demo/daemon/log", nil)
	request.Host = "127.0.0.1:3899"
	server.routes().ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatalf("unauthenticated log status=%d", response.Code)
	}
}

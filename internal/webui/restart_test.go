package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3899/ncmm/internal/selfupdate"
)

func newRestartTestManager(t *testing.T) *runManager {
	t.Helper()
	m := newControlledRunManager(t, 1)
	t.Cleanup(func() {
		m.mu.RLock()
		ids := make([]string, 0, len(m.running)+len(m.queued))
		for id := range m.running {
			ids = append(ids, id)
		}
		for id := range m.queued {
			ids = append(ids, id)
		}
		m.mu.RUnlock()
		for _, id := range ids {
			_ = m.stop(id)
		}
		for _, id := range ids {
			waitForRunStatus(t, m, id, "stopped", "success", "failed")
		}
		m.supervisor.Close(2 * time.Second)
	})
	return m
}

func TestRestartRequiresBusyConfirmationAndStopsNewAdmissions(t *testing.T) {
	m := newRestartTestManager(t)
	first, err := m.start(context.Background(), controlledJob("running", "allow", "one.json", filepath.Join(m.home, "never")))
	if err != nil {
		t.Fatal(err)
	}
	job := controlledJob("delayed", "allow", "two.json", filepath.Join(m.home, "later"))
	_, err = m.startWithDelay(context.Background(), job, 60)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{runner: m, restartCh: make(chan struct{}, 1), instance: instanceMetadata{InstanceID: "old-instance"}}
	response := httptest.NewRecorder()
	s.handleSystemRestart(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"force":false}`)))
	var result map[string]any
	_ = json.Unmarshal(response.Body.Bytes(), &result)
	if response.Code != 409 || result["running"] != float64(1) || result["queued"] != float64(1) || result["confirmationRequired"] != true {
		t.Fatalf("unsafe restart: %d %s", response.Code, response.Body.String())
	}
	if len(s.restartCh) != 0 {
		t.Fatal("busy restart was requested without confirmation")
	}
	response = httptest.NewRecorder()
	s.handleSystemRestart(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"force":true}`)))
	if response.Code != 202 || !strings.Contains(response.Body.String(), "old-instance") {
		t.Fatalf("forced restart: %d %s", response.Code, response.Body.String())
	}
	if _, err := m.start(context.Background(), job); err == nil {
		t.Fatal("new task admitted after restart decision")
	}
	if err := m.stop(first.ID); err != nil {
		t.Fatal(err)
	}
	if m.queuedCount() != 1 {
		t.Fatal("queued task launched during restart")
	}
}

func TestIdleRestartAndNewTaskRaceHasSingleWinner(t *testing.T) {
	for i := 0; i < 30; i++ {
		m := newRestartTestManager(t)
		var wg sync.WaitGroup
		var restarted bool
		var startErr error
		gate := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-gate; _, restarted = m.reserveRestart(false) }()
		go func() {
			defer wg.Done()
			<-gate
			_, startErr = m.startWithDelay(context.Background(), controlledJob("delayed", "allow", "one.json", "unused"), 60)
		}()
		close(gate)
		wg.Wait()
		if restarted == (startErr == nil) {
			t.Fatalf("restart=%v startErr=%v; both must not succeed", restarted, startErr)
		}
	}
}

func TestForceRestartPreventsDelayedTimerLaunch(t *testing.T) {
	m := newRestartTestManager(t)
	record, err := m.startWithDelay(context.Background(), controlledJob("delayed", "allow", "one.json", filepath.Join(m.home, "never")), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, accepted := m.reserveRestart(true); !accepted {
		t.Fatal("force restart was not accepted")
	}
	// 等待真实延迟计时器到期；它必须经 drainQueueLocked 的重启保护。
	time.Sleep(time.Until(*record.NotBefore) + 100*time.Millisecond)
	got, ok := m.get(record.ID)
	if !ok || got.Status != "queued" || m.runningCount() != 0 || m.queuedCount() != 1 {
		t.Fatalf("delayed task launched after force restart: %+v", got)
	}
}

func TestRestartRejectsInvalidForceAndInProgressUpdate(t *testing.T) {
	s := &Server{runner: newRestartTestManager(t), restartCh: make(chan struct{}, 1)}
	for _, body := range []string{`{"force":"yes"}`, `{"force":true} garbage`} {
		r := httptest.NewRecorder()
		s.handleSystemRestart(r, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		if r.Code != 400 {
			t.Fatalf("invalid force accepted: %d", r.Code)
		}
	}
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	r := httptest.NewRecorder()
	s.handleSystemRestart(r, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"force":true}`)))
	if r.Code != 409 || len(s.restartCh) != 0 {
		t.Fatalf("update interrupted: %d", r.Code)
	}
}

func TestAutoRestartOnlyVerifiedPendingUpdateWhenIdle(t *testing.T) {
	t.Setenv("NCMM_DOCKER_OFFICIAL", "")
	for _, scenario := range []string{"idle", "default", "disabled", "delayed", "running", "stale", "invalid-config", "grace", "corrupt-state", "latest"} {
		t.Run(scenario, func(t *testing.T) {
			m := newRestartTestManager(t)
			executable := filepath.Join(m.home, "installed")
			if err := os.WriteFile(executable, []byte("new binary fixture"), 0700); err != nil {
				t.Fatal(err)
			}
			cfg := "updater:\n  auto_restart: true\n"
			if scenario == "default" {
				cfg = "updater:\n  check: false\n"
			}
			if scenario == "disabled" {
				cfg = "updater:\n  auto_restart: false\n"
			}
			if scenario == "invalid-config" {
				cfg = "updater: ["
			}
			if err := os.WriteFile(m.configPath, []byte(cfg), 0600); err != nil {
				t.Fatal(err)
			}
			pending := updateState{LatestVersion: "1.2.2-grails.2", UpdatedVersion: "1.2.2-grails.2", InstalledSHA256: selfupdate.Digest([]byte("new binary fixture")), UpdateStatus: "success"}
			if scenario == "stale" {
				pending.InstalledSHA256 = selfupdate.Digest([]byte("other binary"))
			}
			data, _ := json.Marshal(pending)
			if scenario == "corrupt-state" {
				data = []byte("{")
			}
			if err := os.WriteFile(filepath.Join(m.home, "ncmm-update.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			s := &Server{opts: Options{Home: m.home, Executable: executable, Version: "1.2.2-grails.1", Output: func(string, ...any) {}}, runner: m, config: newConfigStore(m.configPath), restartCh: make(chan struct{}, 1)}
			if scenario == "latest" {
				s.opts.Version = "1.2.2-grails.2"
			}
			if scenario == "grace" {
				s.autoRestartAfter = time.Now().Add(time.Minute)
			}
			if scenario == "delayed" {
				_, _ = m.startWithDelay(context.Background(), controlledJob("waiting", "allow", "one.json", "unused"), 60)
			}
			if scenario == "running" {
				_, err := m.start(context.Background(), controlledJob("running", "allow", "one.json", filepath.Join(m.home, "never")))
				if err != nil {
					t.Fatal(err)
				}
			}
			got := s.tryAutoRestart()
			if got != (scenario == "idle" || scenario == "default") {
				t.Fatalf("auto restart=%v in %s", got, scenario)
			}
			if scenario == "delayed" {
				m.mu.RLock()
				ids := append([]string(nil), m.queue...)
				m.mu.RUnlock()
				for _, id := range ids {
					if err := m.stop(id); err != nil {
						t.Fatal(err)
					}
				}
				if !s.tryAutoRestart() {
					t.Fatal("did not restart after delayed queue became empty")
				}
			}
		})
	}
}

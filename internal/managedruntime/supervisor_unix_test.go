//go:build !windows

package managedruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSupervisorHelperProcess(t *testing.T) {
	if os.Getenv("NCMM_LAUNCHER_HELPER") != "1" {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		os.Exit(2)
	}
	slotName := filepath.Base(filepath.Dir(executable))
	version := slotName[:len(slotName)-17]
	if version == os.Getenv("NCMM_LAUNCHER_BAD_VERSION") {
		os.Exit(2)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	id := fmt.Sprintf("helper-%d", os.Getpid())
	metadata, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "instanceId": id, "listen": listener.Addr().String()})
	if os.WriteFile(filepath.Join(os.Getenv("NCMM_LAUNCHER_HOME"), "webui.instance.lock"), metadata, 0600) != nil {
		os.Exit(2)
	}
	http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/restart" {
			go func() { time.Sleep(20 * time.Millisecond); os.Exit(RestartExitCode) }()
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if r.URL.Path != "/api/v1/instance" {
			http.NotFound(w, r)
			return
		}
		if version == os.Getenv("NCMM_LAUNCHER_WRONG_HEALTH") {
			version = "0.0.0"
		}
		json.NewEncoder(w).Encode(map[string]string{"instanceId": id, "version": version})
	}))
}

func supervisorFixture(t *testing.T) (SupervisorOptions, []byte) {
	t.Helper()
	t.Setenv("NCMM_LAUNCHER_HELPER", "1")
	dir := t.TempDir()
	image := filepath.Join(dir, "image")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(image, data, 0755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NCMM_LAUNCHER_HOME", home)
	options := SupervisorOptions{Root: filepath.Join(dir, "runtime"), Image: image, ImageVersion: "1.2.2-grails.1", Home: home, Args: []string{"-test.run=^TestSupervisorHelperProcess$"}, Output: io.Discard, StartupTimeout: 3 * time.Second, StopTimeout: time.Second, PollInterval: 20 * time.Millisecond}
	if err := Initialize(options.Root, image, options.ImageVersion); err != nil {
		t.Fatal(err)
	}
	return options, data
}

func waitState(t *testing.T, root string, condition func(State) bool) State {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, err := Read(root)
		if err == nil && condition(state) {
			return state
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("runtime did not reach expected state")
	return State{}
}

func TestSupervisorPromotesHealthyCandidateAndStopsChildren(t *testing.T) {
	options, data := supervisorFixture(t)
	data = append(data, []byte("test-image-two")...)
	staged, err := Stage(options.Root, "1.2.2-grails.2", data)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, options) }()
	waitState(t, options.Root, func(state State) bool { return state.Current.Path == staged.Path && state.Pending.Path == "" })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("launcher did not reap child on stop")
	}
}

func TestSupervisorRollsBackCrashAndRejectsSecondLauncher(t *testing.T) {
	options, data := supervisorFixture(t)
	t.Setenv("NCMM_LAUNCHER_BAD_VERSION", "1.2.2-grails.2")
	staged, err := Stage(options.Root, "1.2.2-grails.2", append(data, []byte("bad-candidate")...))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, options) }()
	waitState(t, options.Root, func(state State) bool { return state.RejectedSHA256 == staged.SHA256 && state.Pending.Path == "" })
	if err := Serve(context.Background(), options); err == nil || !strings.Contains(err.Error(), "已有启动器") {
		t.Fatalf("second launcher error = %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	restored := false
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(filepath.Join(options.Home, "webui.instance.lock"))
		var meta struct {
			PID int `json:"pid"`
		}
		json.Unmarshal(data, &meta)
		if meta.PID != 0 && healthy(&http.Client{Timeout: time.Second}, options.Home, meta.PID, "1.2.2-grails.1") {
			restored = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !restored {
		t.Fatal("previous version did not become healthy after rollback")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("rollback child not stopped")
	}
}

func TestSupervisorRestartActivatesPendingAndVersionMismatchRollsBack(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(fmt.Sprintf("mismatch-%v", mismatch), func(t *testing.T) {
			options, data := supervisorFixture(t)
			options.StartupTimeout = 400 * time.Millisecond
			if mismatch {
				t.Setenv("NCMM_LAUNCHER_WRONG_HEALTH", "1.2.2-grails.2")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- Serve(ctx, options) }()
			var meta struct {
				PID    int    `json:"pid"`
				Listen string `json:"listen"`
			}
			deadline := time.Now().Add(3 * time.Second)
			ready := false
			for time.Now().Before(deadline) {
				bytes, _ := os.ReadFile(filepath.Join(options.Home, "webui.instance.lock"))
				json.Unmarshal(bytes, &meta)
				if meta.PID != 0 && healthy(&http.Client{Timeout: time.Second}, options.Home, meta.PID, options.ImageVersion) {
					ready = true
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if !ready {
				t.Fatal("initial child did not start")
			}
			// Wait for the second probe/confirmation before requesting restart.
			time.Sleep(60 * time.Millisecond)
			old, _ := CurrentExecutable(options.Root)
			candidate, err := Stage(options.Root, "1.2.2-grails.2", append(data, []byte("restart-candidate")...))
			if err != nil {
				t.Fatal(err)
			}
			if path, _ := CurrentExecutable(options.Root); path != old {
				t.Fatal("CLI switched before health confirmation")
			}
			response, err := http.Post("http://"+meta.Listen+"/restart", "application/json", nil)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			state := waitState(t, options.Root, func(state State) bool {
				if mismatch {
					return state.RejectedSHA256 == candidate.SHA256
				}
				return state.Current.Path == candidate.Path && state.Pending.Path == ""
			})
			if mismatch && (state.Current.Version != options.ImageVersion || state.LastError == "") {
				t.Fatalf("rollback = %+v", state)
			}
			if !mismatch {
				if path, _ := CurrentExecutable(options.Root); path == old {
					t.Fatal("CLI did not switch after confirmation")
				}
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("child did not stop")
			}
		})
	}
}

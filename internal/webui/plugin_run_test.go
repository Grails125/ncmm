package webui

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPluginRunOutlivesRequestAndStopsWithService(t *testing.T) {
	manager := newControlledRunManager(t, 1)
	t.Cleanup(func() { manager.supervisor.Close(time.Second) })
	serviceCtx, stopService := context.WithCancel(context.Background())
	defer stopService()
	server := &Server{runner: manager, scheduler: &scheduler{ctx: serviceCtx}}
	var processCtx context.Context
	var processArgs []string
	command := manager.command
	manager.command = func(ctx context.Context, executable string, args ...string) *exec.Cmd {
		processCtx = ctx
		processArgs = append([]string(nil), args...)
		return command(ctx, executable, args...)
	}
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	request := httptest.NewRequest("POST", "/api/v1/plugins/demo/run", strings.NewReader(`{"args":["--config","plugin.yaml"]}`)).WithContext(requestCtx)
	request.SetPathValue("name", "demo")
	response := httptest.NewRecorder()
	server.handlePluginRun(response, request)
	cancelRequest()
	if response.Code != 200 {
		t.Fatalf("run status=%d body=%s", response.Code, response.Body.String())
	}
	if processCtx == nil || processCtx.Err() != nil {
		t.Fatal("plugin process was canceled when its HTTP response completed")
	}
	wantSuffix := []string{"plugin", "run", "demo", "--", "--config", "plugin.yaml"}
	if len(processArgs) < len(wantSuffix) || !reflect.DeepEqual(processArgs[len(processArgs)-len(wantSuffix):], wantSuffix) {
		t.Fatalf("plugin-specific flags escaped their boundary: %q", processArgs)
	}
	var result struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	stopService()
	waitForRunStatus(t, manager, result.RunID, "stopped", "failed")
}

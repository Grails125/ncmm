package ncmm

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/pkg/notify"
	"github.com/spf13/cobra"
)

func TestTaskOrdinaryFailureContinuesAndReturnsSummary(t *testing.T) {
	for _, mode := range []string{"by-task-group", "by-account"} {
		t.Run(mode, func(t *testing.T) {
			// 目录不能当作 Cookie 文件读取；推歌任务缺少 token，两者均在任何 HTTP 请求前结束。
			main, secondary := t.TempDir(), t.TempDir()
			cfg := &config.Config{
				Network:  &api.Config{},
				Accounts: &config.AccountsConf{Main: main, Secondary: []string{secondary}},
				Task: &config.TaskConf{
					Mode: mode, Note: true, PlayIds: true, DailySongShare: true,
					FastTasks: []string{"note", "playids"}, SlowTasks: []string{"daily-song-share"},
				},
				Note:           &config.NoteConf{Type: 35},
				PlayIds:        &config.PlayIdsConfig{EnableMain: true, EnableSecondaries: true, IDs: "123"},
				DailySongShare: &config.DailySongShareConf{EnableMain: true, EnableSecondaries: true},
			}
			report := notify.NewReport("task")
			task := NewTask(&Root{Cfg: cfg, Report: report}, nil)
			task.wait = func(context.Context, time.Duration) {}
			var output bytes.Buffer
			task.cmd.SetOut(&output)
			task.cmd.SetErr(&output)
			err := task.execute(context.Background())
			if failures, skips := report.Counts(); failures != 3 || skips != 2 {
				t.Fatalf("failure interrupted another task/account: report=%+v; output=%s", report.Items(), output.String())
			}
			if err == nil || !strings.Contains(err.Error(), "3 项任务失败") {
				t.Fatalf("ordinary failures were reported as success: err=%v; output=%s", err, output.String())
			}
			for _, detail := range []string{"note", "playids", main, secondary} {
				if !strings.Contains(err.Error(), detail) {
					t.Fatalf("summary lost failed task/account %q: %v", detail, err)
				}
			}
			if strings.Contains(output.String(), "所有任务批量执行完毕") || !strings.Contains(output.String(), "其余可执行任务已继续处理") {
				t.Fatalf("failed batch did not explain that remaining tasks continued: %s", output.String())
			}
		})
	}
}

func TestTaskRuntimeFailureDoesNotPrintCobraUsage(t *testing.T) {
	cfg := &config.Config{
		Network: &api.Config{}, Accounts: &config.AccountsConf{Main: t.TempDir()},
		Task: &config.TaskConf{Note: true, FastTasks: []string{"note"}}, Note: &config.NoteConf{Type: 35},
	}
	task := NewTask(&Root{Cfg: cfg}, nil)
	task.wait = func(context.Context, time.Duration) {}
	var output bytes.Buffer
	task.cmd.SetOut(&output)
	task.cmd.SetErr(&output)
	err := task.cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("runtime failure was suppressed instead of being returned")
	}
	if strings.Contains(output.String(), "Usage:") || strings.Contains(output.String(), "Error:") {
		t.Fatalf("runtime failure still printed duplicate Cobra diagnostics: %s", output.String())
	}
}

func TestTaskCancellationStopsRemainingTasksAndAccounts(t *testing.T) {
	for _, mode := range []string{"by-task-group", "by-account"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &config.Config{
				Network: &api.Config{}, Accounts: &config.AccountsConf{Main: t.TempDir(), Secondary: []string{t.TempDir()}},
				Task: &config.TaskConf{
					Mode: mode, Note: true, PlayIds: true, DailySongShare: true,
					FastTasks: []string{"note", "playids"}, SlowTasks: []string{"daily-song-share"},
				},
				Note:           &config.NoteConf{Type: 35},
				PlayIds:        &config.PlayIdsConfig{EnableMain: true, EnableSecondaries: true, IDs: "123"},
				DailySongShare: &config.DailySongShareConf{EnableMain: true, EnableSecondaries: true},
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			report := notify.NewReport("task")
			task := NewTask(&Root{Cfg: cfg, Report: report}, nil)
			task.wait = func(context.Context, time.Duration) { cancel() }
			var output bytes.Buffer
			task.cmd.SetOut(&output)
			err := task.execute(ctx)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost behind task failure: %v", err)
			}
			if failures, skips := report.Counts(); failures != 1 || skips != 0 {
				t.Fatalf("tasks ran after cancellation: %+v", report.Items())
			}
			for _, text := range []string{"[播放指定歌曲]", "[慢任务组]", "执行完毕", "批量任务执行结束"} {
				if strings.Contains(output.String(), text) {
					t.Fatalf("canceled batch kept running or printed completion: %s", output.String())
				}
			}
		})
	}
}

func TestTaskPrerequisiteFailureDoesNotStartBatch(t *testing.T) {
	task := NewTask(&Root{}, nil)
	errPrerequisite := errors.New("强制升级拦截")
	task.cmd.PreRunE = func(*cobra.Command, []string) error { return errPrerequisite }
	var output bytes.Buffer
	task.cmd.SetOut(&output)
	task.cmd.SetErr(&output)
	if err := task.cmd.ExecuteContext(context.Background()); !errors.Is(err, errPrerequisite) {
		t.Fatalf("global prerequisite failure was swallowed: %v", err)
	}
	if strings.Contains(output.String(), "开始执行") {
		t.Fatalf("batch ran after a global prerequisite failed: %s", output.String())
	}
}

func TestTaskBlockedAccountReportsSkippedActionsAndContinuesOtherAccount(t *testing.T) {
	main, secondary := t.TempDir(), t.TempDir()
	cfg := &config.Config{
		Network:        &api.Config{},
		PlayIds:        &config.PlayIdsConfig{EnableMain: true, EnableSecondaries: true, IDs: "123"},
		DailySongShare: &config.DailySongShareConf{EnableMain: true, EnableSecondaries: true},
		MusicPartner:   &config.MusicPartnerConf{EnableMain: true},
	}
	report := notify.NewReport("task")
	task := NewTask(&Root{Cfg: cfg, Report: report}, nil)
	task.wait = func(context.Context, time.Duration) {}
	var output bytes.Buffer
	task.cmd.SetOut(&output)
	task.runQueue(context.Background(), []string{"playids", "daily-song-share", "music-partner"},
		[]Account{{Filepath: main, IsMain: true}, {Filepath: secondary}},
		map[string]bool{"playids": true, "daily-song-share": true, "music-partner": true},
		map[string]bool{main: true}, nil, false)
	if failures, skips := report.Counts(); failures != 1 || skips != 4 {
		t.Fatalf("blocked account skips were lost or next account did not run: %+v; output=%s", report.Items(), output.String())
	}
	blockedScoringSkipped := false
	for _, item := range report.Items() {
		if item.Account == main && item.Task == "music-partner" && item.Kind == notify.KindSkip {
			blockedScoringSkipped = true
		}
	}
	if !blockedScoringSkipped {
		t.Fatalf("blocked scoring was not recorded as skipped: %+v", report.Items())
	}
	if len(task.failures) != 1 || !strings.Contains(task.failures[0].Error(), secondary) {
		t.Fatalf("next account failure missing from aggregate: %v", task.failures)
	}
}

func TestTaskSkipOnlyDoesNotReusePreviousRunFailures(t *testing.T) {
	cfg := &config.Config{
		Network: &api.Config{}, Accounts: &config.AccountsConf{Main: t.TempDir()},
		Task: &config.TaskConf{Note: true, DailySongShare: true, FastTasks: []string{"note", "daily-song-share"}},
		Note: &config.NoteConf{Type: 35}, DailySongShare: &config.DailySongShareConf{EnableMain: true},
	}
	task := NewTask(&Root{Cfg: cfg}, nil)
	task.wait = func(context.Context, time.Duration) {}
	var output bytes.Buffer
	task.cmd.SetOut(&output)
	if err := task.execute(context.Background()); err == nil {
		t.Fatal("first run did not retain its task error")
	}
	cfg.Task.Note = false
	output.Reset()
	if err := task.execute(context.Background()); err != nil {
		t.Fatalf("operational skip inherited the previous run failure: %v", err)
	}
	if !strings.Contains(output.String(), "0 项任务失败，1 项跳过") {
		t.Fatalf("skip-only completion summary was inaccurate: %s", output.String())
	}
}

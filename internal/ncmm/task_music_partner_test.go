package ncmm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/pkg/notify"
)

func TestTaskMusicPartnerLegacyQueueAndFailure(t *testing.T) {
	for _, mode := range []string{"by-account", "by-task-group"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &config.Config{
				Network:  &api.Config{},
				Accounts: &config.AccountsConf{Main: filepath.Join(t.TempDir(), "missing-cookie.json")},
				Task:     &config.TaskConf{MusicPartner: true, Mode: mode, FastTasks: []string{"note"}, SlowTasks: []string{"playids"}},
			}
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			task := NewTask(&Root{Cfg: cfg}, nil)
			var output bytes.Buffer
			task.cmd.SetOut(&output)
			task.cmd.SetErr(&output)
			err := task.execute(context.Background())
			if err == nil || !strings.Contains(err.Error(), "音乐合伙人") {
				t.Fatalf("missing cookie silently succeeded: %v; output=%s", err, output.String())
			}
			if strings.Contains(output.String(), "所有任务批量执行完毕") {
				t.Fatal("failure printed success")
			}
			if len(cfg.Task.SlowTasks) != 1 {
				t.Fatal("legacy queue mutated")
			}
		})
	}
}

func TestTaskMusicPartnerAccountStopReasons(t *testing.T) {
	joined := errors.Join(musicPartnerResponseError("评分", 405, "资源状态异常"), musicPartnerResponseError("评分", 429, ""))
	if !isSessionInvalidError(fmt.Errorf("评分失败: %w", joined)) {
		t.Fatal("later account stop inside joined failures was lost")
	}
	for _, code := range []int64{301, 401, 403, 419, 429, 460} {
		wrapped := fmt.Errorf("评分失败: %w", musicPartnerResponseError("评分", code, ""))
		if !isSessionInvalidError(wrapped) {
			t.Fatalf("account stop lost for code %d", code)
		}
	}
	if isSessionInvalidError(musicPartnerResponseError("评分", 703, "")) {
		t.Fatal("partner qualification must not block unrelated tasks")
	}
	if isSessionInvalidError(musicPartnerResponseError("评分", 405, "资源状态异常")) {
		t.Fatal("single resource failure must not block unrelated tasks")
	}
}

func TestTaskMusicPartnerBlockedByPreviousTaskIsSkipped(t *testing.T) {
	cfg := &config.Config{Task: &config.TaskConf{MusicPartner: true}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	task := NewTask(&Root{Cfg: cfg, Report: notify.NewReport("task")}, nil)
	task.runQueue(context.Background(), []string{"music-partner"}, []Account{{Filepath: "account.json", IsMain: true}}, map[string]bool{"music-partner": true}, map[string]bool{"account.json": true}, nil, false)
	if err := task.finishRun(); err != nil {
		t.Fatalf("unexecuted scoring should be a non-failing skip: %v", err)
	}
	if failed, skipped, unknown := task.root.Report.OutcomeCounts(); failed != 0 || skipped != 1 || unknown != 0 {
		t.Fatalf("unexecuted scoring must remain a skip: %+v", task.root.Report.Items())
	}
}

func TestTaskMusicPartnerNoSelectedAccount(t *testing.T) {
	cfg := &config.Config{Accounts: &config.AccountsConf{Main: "main.json"}, Task: &config.TaskConf{MusicPartner: true}, MusicPartner: &config.MusicPartnerConf{EnableSecondaries: true}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	task := NewTask(&Root{Cfg: cfg}, nil)
	if err := task.execute(context.Background()); err != nil {
		t.Fatalf("disabled account selection must be a non-failing skip: %v", err)
	}
	if failed, skipped, unknown := task.root.Report.OutcomeCounts(); failed != 0 || skipped != 1 || unknown != 0 {
		t.Fatalf("disabled account selection must remain a skip: %+v", task.root.Report.Items())
	}
	task.opts.OnlyFast = true
	if err := task.execute(context.Background()); err != nil {
		t.Fatal("only-fast should exclude the slow scoring task", err)
	}
}

func TestTaskMusicPartnerAccountFlagsAndCancellation(t *testing.T) {
	cfg := &config.Config{Task: &config.TaskConf{MusicPartner: true}, MusicPartner: &config.MusicPartnerConf{EnableSecondaries: true}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	task := NewTask(&Root{Cfg: cfg}, nil)
	if task.isActionEnabledForAccount("music-partner", true) || !task.isActionEnabledForAccount("music-partner", false) {
		t.Fatal("account flags ignored")
	}
	if task.standardizeActionKey("Music-Partner") != "music-partner" {
		t.Fatal("task name not recognized")
	}
	if err := task.execute(context.Background()); err == nil {
		t.Fatal("missing accounts should fail")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := task.execute(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
	if err := task.cmd.Flags().Set("music-partner", "false"); err != nil {
		t.Fatal(err)
	}
	if err := task.execute(context.Background()); err != nil {
		t.Fatal("explicit task disable ignored", err)
	}
}

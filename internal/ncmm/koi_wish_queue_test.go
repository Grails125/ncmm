package ncmm

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/config"
)

func TestKoiWishUsesSignGatesWithoutVIPDependency(t *testing.T) {
	vipDisabled := false
	cfg := &config.Config{Sign: &config.SignConf{EnableMain: true, Automatic: true, EnableKoiWish: true, EnableVipTask: &vipDisabled}}
	task := NewTask(&Root{Cfg: cfg}, nil)
	if task.standardizeActionKey("koiwish") != "KoiWish" || !task.isActionActive("KoiWish", map[string]bool{"sign": true}) {
		t.Fatal("免费许愿应独立于 VIP 开关并沿用签到总开关")
	}
	if task.isActionActive("KoiWish", map[string]bool{"KoiWish": true}) || !task.isActionEnabledForAccount("KoiWish", true) || task.isActionEnabledForAccount("KoiWish", false) {
		t.Fatal("免费许愿绕过签到或账号开关")
	}
	if !task.queueHasActiveSignTasks([]string{"KoiWish"}, map[string]bool{"sign": true}) {
		t.Fatal("仅免费许愿的队列未识别为签到任务组")
	}
	for _, automatic := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			cfg.Sign.Automatic, cfg.Sign.EnableKoiWish = automatic, enabled
			want := automatic && enabled
			if task.isActionActive("KoiWish", map[string]bool{"sign": true}) != want || task.queueHasActiveSignTasks([]string{"KoiWish"}, map[string]bool{"sign": true}) != want {
				t.Fatalf("免费许愿未遵守自动与专属开关: automatic=%t enabled=%t", automatic, enabled)
			}
		}
	}
}

func TestKoiWishDirectSignGateHonorsAutomaticFlag(t *testing.T) {
	command := NewSign(&Root{Cfg: &config.Config{Sign: &config.SignConf{EnableKoiWish: true}}}, nil)
	allowed := map[string]bool{"KoiWish": true}
	if command.isKoiWishEnabled(allowed) {
		t.Fatal("未开启自动任务时不得许愿")
	}
	command.opts.Automatic = true
	if !command.isKoiWishEnabled(allowed) || command.isKoiWishEnabled(map[string]bool{"VipTask": true}) {
		t.Fatal("直接签到自动标志或当前队列未生效")
	}
	command.root.Cfg.Sign.EnableKoiWish = false
	if command.isKoiWishEnabled(allowed) {
		t.Fatal("自动标志不得覆盖许愿专属关闭开关")
	}
}

func TestKoiWishDirectSignCancellationReturnsCanceledWithoutRunningAccounts(t *testing.T) {
	command := NewSign(&Root{Cfg: &config.Config{Sign: &config.SignConf{EnableMain: true, EnableSecondaries: true, Automatic: true, EnableKoiWish: true}, Accounts: &config.AccountsConf{Main: "not-opened.json", Secondary: []string{"not-opened-either.json"}}}}, nil)
	var output bytes.Buffer
	command.cmd.SetOut(&output)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := command.execute(ctx); err != context.Canceled {
		t.Fatalf("取消签到应返回取消错误: %v", err)
	}
	if strings.Contains(output.String(), "开始主账号签到") || strings.Contains(output.String(), "开始辅助账号签到") || strings.Contains(output.String(), "所有") {
		t.Fatalf("取消后仍执行账号或误报完成: %s", output.String())
	}
}

func TestKoiWishOnlyQueueDispatchesInSelectedTaskGroup(t *testing.T) {
	for _, mode := range []string{"by-account", "by-task-group"} {
		for _, onlyFast := range []bool{false, true} {
			cfg := &config.Config{
				Network: &api.Config{}, Accounts: &config.AccountsConf{Main: t.TempDir()},
				Task: &config.TaskConf{Mode: mode, Sign: true, FastTasks: []string{"KoiWish"}, SlowTasks: []string{"playids"}},
				Sign: &config.SignConf{EnableMain: true, Automatic: true, EnableKoiWish: true},
			}
			task := NewTask(&Root{Cfg: cfg}, nil)
			task.opts.OnlyFast, task.opts.OnlySlow = onlyFast, !onlyFast
			task.wait = func(context.Context, time.Duration) {}
			var output bytes.Buffer
			task.cmd.SetOut(&output)
			// 目录无法作为 Cookie 读取，确定分派后即失败，不产生 HTTP 请求。
			err := task.execute(context.Background())
			if (err != nil) != onlyFast || strings.Contains(output.String(), "[日常签到] 执行失败") != onlyFast {
				t.Fatalf("许愿队列未遵守任务组选择: mode=%s onlyFast=%t err=%v output=%s", mode, onlyFast, err, output.String())
			}
		}
	}
}

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

func TestYunbeiActivitiesUseSignAutomaticAndAccountGates(t *testing.T) {
	activity := &config.YunbeiTaskConf{EnableRedPacket: true, EnableLottery: true, EnableLotteryWatchVideo: true, EnableWatchVideoYunbei: true}
	cfg := &config.Config{Sign: &config.SignConf{EnableMain: true, Automatic: true, YunbeiTask: activity}}
	task := NewTask(&Root{Cfg: cfg}, nil)
	for _, key := range []string{"RedPacket", "Lottery", "LotteryWatchVideo", "WatchVideoYunbei"} {
		if got := task.standardizeActionKey(strings.ToLower(key)); got != key {
			t.Fatalf("活动未被识别：%s -> %s", key, got)
		}
		if !task.isActionActive(key, map[string]bool{"sign": true}) || task.isActionActive(key, map[string]bool{key: true}) {
			t.Fatalf("活动绕过签到总开关：%s", key)
		}
		if !task.isActionEnabledForAccount(key, true) || task.isActionEnabledForAccount(key, false) {
			t.Fatalf("活动绕过签到账号选择：%s", key)
		}
		if !task.queueHasActiveSignTasks([]string{key}, map[string]bool{"sign": true}) {
			t.Fatalf("活动独立队列未被识别为签到队列：%s", key)
		}
		cfg.Sign.Automatic = false
		if task.isActionActive(key, map[string]bool{"sign": true}) {
			t.Fatalf("活动绕过自动任务开关：%s", key)
		}
		cfg.Sign.Automatic = true
	}
	cfg.Sign.YunbeiTask = &config.YunbeiTaskConf{}
	for _, key := range []string{"RedPacket", "Lottery", "LotteryWatchVideo", "WatchVideoYunbei"} {
		if task.isActionActive(key, map[string]bool{"sign": true}) || task.queueHasActiveSignTasks([]string{key}, map[string]bool{"sign": true}) {
			t.Fatalf("关闭的活动仍进入调度：%s", key)
		}
	}
}

func TestYunbeiActivityOnlyQueuesDispatchInBothTaskModes(t *testing.T) {
	for _, mode := range []string{"by-account", "by-task-group"} {
		for _, key := range []string{"RedPacket", "Lottery", "LotteryWatchVideo", "WatchVideoYunbei"} {
			t.Run(mode+"/"+key, func(t *testing.T) {
				cfg := &config.Config{
					Network: &api.Config{}, Accounts: &config.AccountsConf{Main: t.TempDir()},
					Task: &config.TaskConf{Mode: mode, Sign: true, FastTasks: []string{key}},
					Sign: &config.SignConf{EnableMain: true, Automatic: true, YunbeiTask: &config.YunbeiTaskConf{EnableRedPacket: true, EnableLottery: true, EnableLotteryWatchVideo: true, EnableWatchVideoYunbei: true}},
				}
				task := NewTask(&Root{Cfg: cfg}, nil)
				task.opts.OnlyFast = true
				task.wait = func(context.Context, time.Duration) {}
				var output bytes.Buffer
				task.cmd.SetOut(&output)
				task.cmd.SetErr(&output)
				// 目录不能作为 Cookie 读取，分派后在任何网络请求前退出。
				if err := task.execute(t.Context()); err == nil || !strings.Contains(output.String(), "[日常签到] 执行失败") {
					t.Fatalf("活动独立队列没有分派到签到：err=%v output=%s", err, output.String())
				}
			})
		}
	}
}

func TestYunbeiActivitySwitchesAreIndependent(t *testing.T) {
	keys := []string{"RedPacket", "Lottery", "LotteryWatchVideo", "WatchVideoYunbei"}
	for _, enabledKey := range keys {
		cfg := &config.Config{Sign: &config.SignConf{Automatic: true, EnableSecondaries: true, YunbeiTask: &config.YunbeiTaskConf{
			EnableRedPacket: enabledKey == "RedPacket", EnableLottery: enabledKey == "Lottery",
			EnableLotteryWatchVideo: enabledKey == "LotteryWatchVideo", EnableWatchVideoYunbei: enabledKey == "WatchVideoYunbei",
		}}}
		task := NewTask(&Root{Cfg: cfg}, nil)
		for _, key := range keys {
			if task.isActionActive(key, map[string]bool{"sign": true}) != (key == enabledKey) {
				t.Fatalf("活动开关串用：enabled=%s action=%s", enabledKey, key)
			}
			if task.isActionEnabledForAccount(key, true) || !task.isActionEnabledForAccount(key, false) {
				t.Fatalf("仅辅助账号的活动配置未生效：%s", key)
			}
		}
	}
	for _, root := range []*Root{nil, {}, {Cfg: &config.Config{}}, {Cfg: &config.Config{Sign: &config.SignConf{Automatic: true}}}} {
		task := NewTask(root, nil)
		for _, key := range keys {
			if task.isActionActive(key, map[string]bool{"sign": true}) {
				t.Fatalf("缺失配置时不得启用活动：%s", key)
			}
		}
	}
}

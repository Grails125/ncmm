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

func TestShareSongUsesSignTaskAndAccountGates(t *testing.T) {
	task := NewTask(&Root{Cfg: &config.Config{Sign: &config.SignConf{EnableMain: true}}}, nil)
	if got := task.standardizeActionKey("sharesong"); got != "ShareSong" {
		t.Errorf("分享任务未被识别: %q", got)
	}
	if !task.isActionActive("ShareSong", map[string]bool{"sign": true}) || task.isActionActive("ShareSong", map[string]bool{"ShareSong": true}) {
		t.Error("分享任务没有沿用日常签到总开关")
	}
	if !task.isActionEnabledForAccount("ShareSong", true) || task.isActionEnabledForAccount("ShareSong", false) {
		t.Error("分享任务没有沿用签到账号开关")
	}
	if !task.queueHasActiveSignTasks([]string{"ShareSong"}, map[string]bool{"sign": true}) || task.queueHasActiveSignTasks([]string{"ShareSong"}, map[string]bool{"sign": false}) {
		t.Error("仅分享歌曲的队列没有被识别为签到队列")
	}
}

func TestShareSongOnlyQueueReachesSignExecution(t *testing.T) {
	for _, mode := range []string{"by-account", "by-task-group"} {
		t.Run(mode, func(t *testing.T) {
			// 使用目录作为 Cookie 路径，在任何联网前确定签到分派已发生。
			cfg := &config.Config{
				Network: &api.Config{}, Accounts: &config.AccountsConf{Main: t.TempDir()},
				Task: &config.TaskConf{Mode: mode, Sign: true, FastTasks: []string{"ShareSong"}},
				Sign: &config.SignConf{EnableMain: true},
			}
			task := NewTask(&Root{Cfg: cfg}, nil)
			task.wait = func(context.Context, time.Duration) {}
			var output bytes.Buffer
			task.cmd.SetOut(&output)
			task.cmd.SetErr(&output)
			if err := task.execute(context.Background()); err == nil || !strings.Contains(output.String(), "[日常签到] 执行失败") {
				t.Fatalf("分享队列未进入签到执行: err=%v output=%s", err, output.String())
			}
		})
	}
}

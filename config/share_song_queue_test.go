package config

import (
	"slices"
	"testing"
)

func TestShareSongIncludedInDefaultQueueWithoutChangingCustomQueue(t *testing.T) {
	if !slices.Contains(GetDefault().Task.FastTasks, "ShareSong") {
		t.Error("配置模板默认快任务缺少分享歌曲")
	}
	defaults := &Config{Task: &TaskConf{}}
	if err := defaults.Validate(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(defaults.Task.FastTasks, "ShareSong") {
		t.Error("运行时默认快任务缺少分享歌曲")
	}
	custom := &Config{Task: &TaskConf{FastTasks: []string{"VipTask"}, SlowTasks: []string{"ListenIndie"}}}
	if err := custom.Validate(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(custom.Task.FastTasks, []string{"VipTask"}) || !slices.Equal(custom.Task.SlowTasks, []string{"ListenIndie"}) {
		t.Fatal("用户自定义队列被自动增加任务")
	}
}

func TestEnabledShareSongRepairsMissingQueueWithoutReordering(t *testing.T) {
	for _, mode := range []string{"by-account", "by-task-group"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &Config{
				Task: &TaskConf{Mode: mode, FastTasks: []string{"VipTask", "custom-task", "note"}, SlowTasks: []string{"music-partner", "PlayDailyRecommend"}},
				Sign: &SignConf{YunbeiTask: &YunbeiTaskConf{EnableShareSong: true}},
			}
			for range 2 {
				if err := cfg.Validate(); err != nil {
					t.Fatal(err)
				}
			}
			if !slices.Equal(cfg.Task.FastTasks, []string{"VipTask", "custom-task", "note", "ShareSong"}) {
				t.Fatalf("缺少分享歌曲或改变原队列顺序：%v", cfg.Task.FastTasks)
			}
			if !slices.Equal(cfg.Task.SlowTasks, []string{"music-partner", "PlayDailyRecommend"}) {
				t.Fatalf("慢任务队列被修改：%v", cfg.Task.SlowTasks)
			}
		})
	}
}

func TestShareSongQueueRepairRespectsDisabledAndExistingPlacement(t *testing.T) {
	for _, tc := range []struct {
		name       string
		enabled    bool
		fast, slow []string
	}{
		{"关闭分享", false, []string{"VipTask"}, []string{"music-partner"}},
		{"已有快队列", true, []string{"sharesong", "VipTask"}, []string{"music-partner"}},
		{"已有慢队列", true, []string{"VipTask"}, []string{"music-partner", "SHARESONG"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Task: &TaskConf{FastTasks: slices.Clone(tc.fast), SlowTasks: slices.Clone(tc.slow)}, Sign: &SignConf{YunbeiTask: &YunbeiTaskConf{EnableShareSong: tc.enabled}}}
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(cfg.Task.FastTasks, tc.fast) || !slices.Equal(cfg.Task.SlowTasks, tc.slow) {
				t.Fatalf("现有队列不应改变：fast=%v slow=%v", cfg.Task.FastTasks, cfg.Task.SlowTasks)
			}
		})
	}
}

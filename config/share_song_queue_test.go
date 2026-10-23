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

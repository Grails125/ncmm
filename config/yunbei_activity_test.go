package config

import (
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func activityConfig() *YunbeiTaskConf {
	return &YunbeiTaskConf{EnableRedPacket: true, EnableLottery: true, EnableLotteryWatchVideo: true, EnableWatchVideoYunbei: true}
}

func TestYunbeiActivitiesDefaultToDisabled(t *testing.T) {
	cfg := GetDefault()
	if cfg.Sign.YunbeiTask.EnableRedPacket || cfg.Sign.YunbeiTask.EnableLottery || cfg.Sign.YunbeiTask.EnableLotteryWatchVideo || cfg.Sign.YunbeiTask.EnableWatchVideoYunbei {
		t.Fatal("新增活动不得默认消耗次数或启用广告任务")
	}
	custom := &Config{Sign: &SignConf{}, Task: &TaskConf{FastTasks: []string{"VipTask"}, SlowTasks: []string{"music-partner"}}}
	if err := custom.Validate(); err != nil {
		t.Fatal(err)
	}
	if custom.Sign.YunbeiTask.EnableRedPacket || custom.Sign.YunbeiTask.EnableLottery || custom.Sign.YunbeiTask.EnableLotteryWatchVideo || custom.Sign.YunbeiTask.EnableWatchVideoYunbei {
		t.Fatal("旧配置缺少活动开关时不得自动启用")
	}
	if !slices.Equal(custom.Task.FastTasks, []string{"VipTask", "ShareSong"}) || !slices.Equal(custom.Task.SlowTasks, []string{"music-partner"}) {
		t.Fatalf("关闭的新增任务改变了原队列：%+v", custom.Task)
	}
}

func TestYunbeiActivitiesRepairQueuesWithoutReordering(t *testing.T) {
	for _, mode := range []string{"by-account", "by-task-group"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &Config{Sign: &SignConf{YunbeiTask: activityConfig()}, Task: &TaskConf{Mode: mode, FastTasks: []string{"custom-task", "VipTask"}, SlowTasks: []string{"music-partner"}}}
			for range 2 {
				if err := cfg.Validate(); err != nil {
					t.Fatal(err)
				}
			}
			if !slices.Equal(cfg.Task.FastTasks, []string{"custom-task", "VipTask", "RedPacket", "Lottery"}) || !slices.Equal(cfg.Task.SlowTasks, []string{"music-partner", "LotteryWatchVideo", "WatchVideoYunbei"}) {
				t.Fatalf("活动队列缺失、重复或改变原顺序：%+v", cfg.Task)
			}
		})
	}
}

func TestYunbeiActivitiesKeepCustomPlacementAndDisabledKeys(t *testing.T) {
	fast := []string{"VipTask", "WATCHVIDEOYUNBEI", "lotterywatchvideo"}
	slow := []string{"music-partner", "lottery", "REDPACKET"}
	cfg := &Config{Sign: &SignConf{YunbeiTask: activityConfig()}, Task: &TaskConf{FastTasks: slices.Clone(fast), SlowTasks: slices.Clone(slow)}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Task.FastTasks, fast) || !slices.Equal(cfg.Task.SlowTasks, slow) {
		t.Fatalf("已有任务位置不得被改动：%+v", cfg.Task)
	}
	cfg.Sign.YunbeiTask = &YunbeiTaskConf{}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Task.FastTasks, fast) || !slices.Equal(cfg.Task.SlowTasks, slow) {
		t.Fatal("关闭活动应保留用户队列，执行期由开关控制")
	}
}

func TestYunbeiActivityConfigUpgradePreservesSwitchesAndPartner(t *testing.T) {
	text := string(DefaultYAML())
	for _, key := range []string{"enableRedPacket", "enableLottery", "enableLotteryWatchVideo", "enableWatchVideoYunbei"} {
		if !strings.Contains(text, key+": false") {
			t.Fatalf("模板缺少默认关闭字段 %s", key)
		}
		text = strings.Replace(text, key+": false", key+": true", 1)
	}
	text = strings.Replace(text, "scoreMin: 3", "scoreMin: 2", 1)
	path := writeCompatibilityConfig(t, text)
	if err := AutoUpgradeConfig(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Sign.YunbeiTask.EnableRedPacket || !cfg.Sign.YunbeiTask.EnableLottery || !cfg.Sign.YunbeiTask.EnableLotteryWatchVideo || !cfg.Sign.YunbeiTask.EnableWatchVideoYunbei || cfg.MusicPartner.ScoreMin != 2 {
		t.Fatal("升级丢失活动开关或音乐合伙人配置")
	}
}

func TestYunbeiActivityLegacyUpgradeAddsDisabledSwitches(t *testing.T) {
	text := string(DefaultYAML())
	for _, key := range []string{"enableRedPacket", "enableLottery", "enableLotteryWatchVideo", "enableWatchVideoYunbei"} {
		text = strings.Replace(text, "    "+key+": false\n", "", 1)
	}
	path := writeCompatibilityConfig(t, text)
	if err := AutoUpgradeConfig(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sign.YunbeiTask.EnableRedPacket || cfg.Sign.YunbeiTask.EnableLottery || cfg.Sign.YunbeiTask.EnableLotteryWatchVideo || cfg.Sign.YunbeiTask.EnableWatchVideoYunbei {
		t.Fatal("迁移旧配置不得自动启用新活动")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"enableRedPacket", "enableLottery", "enableLotteryWatchVideo", "enableWatchVideoYunbei"} {
		if !strings.Contains(string(data), key+": false") {
			t.Fatalf("升级未补齐关闭字段：%s", key)
		}
	}
}

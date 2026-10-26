package config

import (
	"slices"
	"testing"
)

func TestKoiWishDefaultsToDisabled(t *testing.T) {
	if GetDefault().Sign.EnableKoiWish {
		t.Fatal("锦鲤免费许愿不得默认开启")
	}
	cfg := &Config{Sign: &SignConf{}, Task: &TaskConf{}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Sign.EnableKoiWish || slices.Contains(cfg.Task.FastTasks, "KoiWish") {
		t.Fatal("默认配置不得自动启用或补入锦鲤免费许愿")
	}
}

func TestKoiWishQueueRepairPreservesCustomPlacement(t *testing.T) {
	for _, tc := range []struct {
		name       string
		enabled    bool
		fast, slow []string
		wantFast   []string
	}{
		{"关闭", false, []string{"VipTask", "custom-task"}, []string{"ListenIndie"}, []string{"VipTask", "custom-task"}},
		{"启用补齐", true, []string{"VipTask", "custom-task"}, []string{"ListenIndie"}, []string{"VipTask", "custom-task", "KoiWish"}},
		{"保留快队列位置", true, []string{"koiwish", "VipTask"}, []string{"ListenIndie"}, []string{"koiwish", "VipTask"}},
		{"保留慢队列位置", true, []string{"VipTask"}, []string{"KOIWISH", "ListenIndie"}, []string{"VipTask"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Sign: &SignConf{EnableKoiWish: tc.enabled, YunbeiTask: &YunbeiTaskConf{}}, Task: &TaskConf{FastTasks: slices.Clone(tc.fast), SlowTasks: slices.Clone(tc.slow)}}
			for range 2 {
				if err := cfg.Validate(); err != nil {
					t.Fatal(err)
				}
			}
			if !slices.Equal(cfg.Task.FastTasks, tc.wantFast) || !slices.Equal(cfg.Task.SlowTasks, tc.slow) {
				t.Fatalf("队列补齐改变已有顺序、位置或重复追加: fast=%v slow=%v", cfg.Task.FastTasks, cfg.Task.SlowTasks)
			}
		})
	}
}

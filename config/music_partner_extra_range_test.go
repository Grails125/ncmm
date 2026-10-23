package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMusicPartnerExtraTargetDefaultsAndZeroRange(t *testing.T) {
	defaults := GetDefault().MusicPartner
	if defaults.ExtraLimitMin != 5 || defaults.ExtraLimitMax != 15 {
		t.Fatalf("默认额外目标应为5至15，得到%d至%d", defaults.ExtraLimitMin, defaults.ExtraLimitMax)
	}
	for _, tc := range []struct {
		name, fields string
		min, max     int
	}{
		{"缺省", "", 5, 15},
		{"旧固定目标", "  extraLimit: 9\n", 9, 9},
		{"零至零", "  extraLimitMin: 0\n  extraLimitMax: 0\n", 0, 0},
		{"零至十五", "  extraLimitMin: 0\n  extraLimitMax: 15\n", 0, 15},
		{"新零范围优先", "  extraLimit: 9\n  extraLimitMin: 0\n  extraLimitMax: 0\n", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			text := fmt.Sprintf("version: %s\nmusicPartner:\n  enableMain: true\n%s", GetDefault().Version, tc.fields)
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			for _, upgrade := range []bool{false, true} {
				if upgrade {
					if err := AutoUpgradeConfig(path); err != nil {
						t.Fatal(err)
					}
				}
				cfg, err := New(path)
				if err != nil {
					t.Fatal(err)
				}
				if cfg.MusicPartner.ExtraLimitMin != tc.min || cfg.MusicPartner.ExtraLimitMax != tc.max {
					t.Fatalf("upgrade=%t: 额外范围变为%d至%d", upgrade, cfg.MusicPartner.ExtraLimitMin, cfg.MusicPartner.ExtraLimitMax)
				}
				data, err := yaml.Marshal(cfg.MusicPartner)
				if err != nil {
					t.Fatal(err)
				}
				var roundTrip MusicPartnerConf
				if err := yaml.Unmarshal(data, &roundTrip); err != nil {
					t.Fatal(err)
				}
				if err := roundTrip.Validate(); err != nil || roundTrip.ExtraLimitMin != tc.min || roundTrip.ExtraLimitMax != tc.max {
					t.Fatalf("范围序列化后变化: %v", err)
				}
			}
		})
	}
}

func TestMusicPartnerExtraTargetRejectsOutsideDailyBounds(t *testing.T) {
	for _, fields := range []string{
		"  extraLimitMin: -1\n  extraLimitMax: 15\n",
		"  extraLimitMin: 0\n  extraLimitMax: 16\n",
		"  extraLimitMin: 5\n  extraLimitMax: 4\n",
		"  extraLimitMin: 0\n",
		"  extraLimitMin: null\n  extraLimitMax: 15\n",
		"  extraLimit: 16\n",
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("version: "+GetDefault().Version+"\nmusicPartner:\n"+fields), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := New(path); err == nil {
			t.Fatalf("接受了超出每日0至15或不完整的范围: %s", fields)
		}
	}
}

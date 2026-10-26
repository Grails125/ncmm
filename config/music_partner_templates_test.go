package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMusicPartnerTemplateDefaultsRemainOptIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, DefaultYAML(), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.MusicPartner
	if cfg.Task.MusicPartner || p.RecommendCountMin != 0 || p.RecommendCountMax != 0 || p.SyncYunCircle || p.ScoreMin != 3 || p.ScoreMax != 4 {
		t.Fatal("默认模板不得开启任务、公开推荐或修改评分范围")
	}
	for _, score := range []string{"1", "2", "3", "4", "5"} {
		if len(p.Comments[score]) == 0 {
			t.Fatalf("%s 分缺少默认乐评", score)
		}
	}
	if len(p.RecommendNotes) == 0 || len(p.RecommendComments) == 0 {
		t.Fatal("推荐笔记和推荐乐评需要独立默认模板")
	}
}

func TestMusicPartnerTemplateUpgradePreservesExplicitPools(t *testing.T) {
	for _, tc := range []struct {
		name, fields           string
		comments               map[string][]string
		notes, recommendations []string
	}{
		{"显式留空", "  comments: {}\n  recommendNotes: []\n  recommendComments: []\n", map[string][]string{}, []string{}, []string{}},
		{"空评分档位", "  comments: {'1': [], '3': []}\n  recommendNotes: []\n  recommendComments: []\n", map[string][]string{"1": {}, "3": {}}, []string{}, []string{}},
		{"自定义原文", "  comments: {'4': ['  我的评分乐评  ']}\n  recommendNotes: ['  {name} — {artist}  ']\n  recommendComments: ['  我的推荐乐评  ']\n", map[string][]string{"4": {"  我的评分乐评  "}}, []string{"  {name} — {artist}  "}, []string{"  我的推荐乐评  "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			original := "version: " + GetDefault().Version + "\nmusicPartner:\n  recommendCountMin: 0\n  recommendCountMax: 0\n" + tc.fields
			if err := os.WriteFile(path, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			if err := AutoUpgradeConfigIfNeeded(path); err != nil {
				t.Fatal(err)
			}
			cfg, err := New(path)
			if err != nil {
				t.Fatal(err)
			}
			p := cfg.MusicPartner
			if !reflect.DeepEqual(p.Comments, tc.comments) || !reflect.DeepEqual(p.RecommendNotes, tc.notes) || !reflect.DeepEqual(p.RecommendComments, tc.recommendations) {
				t.Fatalf("升级覆盖了已有模板: %+v", p)
			}
			if cfg.Task.MusicPartner || p.RecommendCountMin != 0 || p.RecommendCountMax != 0 {
				t.Fatal("模板升级开启了任务")
			}
			before, _ := os.ReadFile(path)
			if err := AutoUpgradeConfigIfNeeded(path); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("重复升级不应改写模板")
			}
		})
	}
}

func TestMusicPartnerTemplateUpgradeFillsMissingPools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("version: "+GetDefault().Version+"\nmusicPartner:\n  enableMain: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := AutoUpgradeConfigIfNeeded(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	p, defaults := cfg.MusicPartner, GetDefault().MusicPartner
	if !reflect.DeepEqual(p.Comments, defaults.Comments) || !reflect.DeepEqual(p.RecommendNotes, defaults.RecommendNotes) || !reflect.DeepEqual(p.RecommendComments, defaults.RecommendComments) {
		t.Fatal("缺失的模板池没有使用统一默认值")
	}
	if p.EnableMain || p.RecommendCountMin != 0 || p.RecommendCountMax != 0 {
		t.Fatal("补默认模板改变了账号开关或推荐目标")
	}
}

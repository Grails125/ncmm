package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMusicPartnerDefaultsAndValidation(t *testing.T) {
	c := &Config{Task: &TaskConf{}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	p := c.MusicPartner
	if c.Task.MusicPartner || !p.EnableMain || p.EnableSecondaries || p.ScoreMin != 3 || p.ScoreMax != 4 || !p.ExtraTasks || p.FullExtraTasks || p.ExtraLimitMin != 7 || p.ExtraLimitMax != 7 || p.WaitMin != 15 || p.WaitMax != 20 || p.SyncYunCircle || len(p.Comments) != 0 {
		t.Fatalf("unexpected defaults: %+v", p)
	}
	for _, tc := range []MusicPartnerConf{{Score: -1}, {Score: 5}, {ExtraLimit: -1}, {ExtraLimit: 501}, {WaitMin: -1}, {WaitMin: 21, WaitMax: 20}, {WaitMax: 301}} {
		c := &Config{MusicPartner: &tc}
		if err := c.Validate(); err == nil {
			t.Fatalf("accepted invalid config: %+v", tc)
		}
	}
}

func TestMusicPartnerConfigMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	old := `version: 1.2.2
accounts:
  main: ./mine.json
  secondary: []
task:
  music-partner: false
  slow_tasks: [playids]
musicPartner:
  enableMain: false
  extraTasks: false
  score: 4
`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	if err := AutoUpgradeConfigIfNeeded(path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Accounts.Main != "./mine.json" || len(c.Accounts.Secondary) != 0 || c.Task.MusicPartner || c.MusicPartner.EnableMain || c.MusicPartner.ExtraTasks || c.MusicPartner.ScoreMin != 4 || c.MusicPartner.ScoreMax != 4 || c.MusicPartner.WaitMin != 15 || len(c.Task.SlowTasks) != 1 {
		t.Fatalf("migration lost explicit user values: %s", b)
	}
}

func TestMusicPartnerLegacyRangesThroughLoadAndUpgrade(t *testing.T) {
	for _, strategy := range []string{"1", "2", "3", "4"} {
		for _, upgrade := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/upgrade=%t", strategy, upgrade), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config.yaml")
				// 即使版本字符串相同，也必须在模板补默认前保留旧版自定义策略。
				text := "version: " + GetDefault().Version + "\nmusicPartner:\n  enableMain: false\n  score: " + strategy + "\n  extraLimit: 17\n  waitMin: 21\n  waitMax: 25\n  comments:\n    '4': ['  自定义乐评  ', '另一条']\n"
				if err := os.WriteFile(path, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
				if upgrade {
					if err := AutoUpgradeConfigIfNeeded(path); err != nil {
						t.Fatal(err)
					}
				}
				c, err := New(path)
				if err != nil {
					t.Fatal(err)
				}
				wantMin := int(strategy[0] - '0')
				wantMax := min(wantMin+1, 4)
				p := c.MusicPartner
				if p.ScoreMin != wantMin || p.ScoreMax != wantMax || p.ExtraLimitMin != 17 || p.ExtraLimitMax != 17 || p.WaitMin != 21 || p.WaitMax != 25 || p.EnableMain || len(p.Comments["4"]) != 2 || p.Comments["4"][0] != "  自定义乐评  " {
					t.Fatalf("legacy values changed: %+v", p)
				}
				if upgrade {
					before, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if err := AutoUpgradeConfigIfNeeded(path); err != nil {
						t.Fatal(err)
					}
					after, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(before, after) {
						t.Fatal("migration not idempotent")
					}
				}
			})
		}
	}
}

func TestMusicPartnerFileCommentValidation(t *testing.T) {
	for _, comments := range []map[string][]string{
		{"0": {"无效分值"}}, {"five": {"无效分值"}}, {"3": {"\n\t "}},
		{"3": {strings.Repeat("字", 501)}}, {"3": make([]string, 51)},
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		original, err := yaml.Marshal(map[string]any{"version": GetDefault().Version, "musicPartner": map[string]any{"scoreMin": 1, "scoreMax": 5, "comments": comments}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Fatal(err)
		}
		// WebUI 保存接口也通过 New 校验临时配置文件。
		if _, err := New(path); err == nil {
			t.Fatal("file/API validation accepted invalid comments")
		}
		if err := AutoUpgradeConfig(path); err == nil {
			t.Fatal("upgrade accepted invalid comments")
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(original, after) {
			t.Fatal("invalid comments were rewritten")
		}
	}
}

func TestMusicPartnerExplicitRangesTakePrecedence(t *testing.T) {
	p := &MusicPartnerConf{Score: 4, ScoreMin: 1, ScoreMax: 5, ExtraLimit: 17, ExtraLimitMin: 5, ExtraLimitMax: 9, Comments: map[string][]string{"5": {strings.Repeat("好", 500)}}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.ScoreMin != 1 || p.ScoreMax != 5 || p.ExtraLimitMin != 5 || p.ExtraLimitMax != 9 || len(p.Comments["5"]) != 1 {
		t.Fatalf("explicit ranges changed: %+v", p)
	}
}

func TestMusicPartnerRangeAndCommentValidation(t *testing.T) {
	for _, p := range []MusicPartnerConf{
		{ScoreMin: 1}, {ScoreMax: 4}, {ScoreMin: 4, ScoreMax: 3}, {ScoreMin: -1, ScoreMax: 4}, {ScoreMin: 1, ScoreMax: 6},
		{ExtraLimitMin: 1}, {ExtraLimitMax: 7}, {ExtraLimitMin: 8, ExtraLimitMax: 7}, {ExtraLimitMin: 1, ExtraLimitMax: 501},
		{WaitMin: 15}, {WaitMax: 20},
		{Comments: map[string][]string{"6": {"不可用"}}}, {Comments: map[string][]string{"01": {"不可用"}}},
		{Comments: map[string][]string{"1": {"   "}}}, {Comments: map[string][]string{"1": {strings.Repeat("字", 501)}}},
		{Comments: map[string][]string{"1": make([]string, 51)}},
	} {
		if err := p.Validate(); err == nil {
			t.Fatalf("accepted invalid partner config: %+v", p)
		}
	}
}

func TestMusicPartnerFileRangesCannotBeFilledOverInvalidInput(t *testing.T) {
	for _, fields := range []string{
		"  scoreMin: 2\n", "  scoreMin: 0\n  scoreMax: 0\n",
		"  scoreMin: 1.5\n  scoreMax: 4\n",
		"  scoreMin: null\n", "  scoreMax: null\n", "  scoreMin: null\n  scoreMax: null\n",
		"  scoreMin: null\n  scoreMax: 4\n", "  scoreMin: 3\n  scoreMax: null\n",
		"  extraLimitMin: 3\n", "  extraLimitMin: 0\n  extraLimitMax: 0\n",
		"  extraLimitMin: 1.5\n  extraLimitMax: 7\n",
		"  extraLimitMin: null\n", "  extraLimitMax: null\n", "  extraLimitMin: null\n  extraLimitMax: null\n",
		"  extraLimitMin: null\n  extraLimitMax: 7\n", "  extraLimitMin: 7\n  extraLimitMax: null\n",
		"  waitMin: 15\n", "  waitMin: 0\n  waitMax: 0\n",
		"  waitMin: 1.5\n  waitMax: 20\n",
		"  waitMin: null\n", "  waitMax: null\n", "  waitMin: null\n  waitMax: null\n",
		"  waitMin: null\n  waitMax: 20\n", "  waitMin: 15\n  waitMax: null\n",
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		original := []byte("version: " + GetDefault().Version + "\nmusicPartner:\n" + fields)
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := New(path); err == nil {
			t.Fatalf("Load accepted invalid range: %s", fields)
		}
		for _, upgrade := range []func(string) error{AutoUpgradeConfigIfNeeded, AutoUpgradeConfig} {
			if err := upgrade(path); err == nil {
				t.Fatalf("upgrade hid invalid range: %s", fields)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(original, after) {
				t.Fatal("invalid source was rewritten")
			}
		}
	}
}

func TestNewerSourceConfigCannotBeDowngraded(t *testing.T) {
	for _, version := range []string{"1.2.9", "1.3.0", "2.0.0"} {
		t.Run(version, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			original := []byte("version: " + version + "\nfutureField: keep-me\n")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			for _, upgrade := range []func(string) error{AutoUpgradeConfigIfNeeded, AutoUpgradeConfig} {
				if err := upgrade(path); err == nil {
					t.Fatal("newer config accepted")
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(original, after) {
					t.Fatal("newer config modified")
				}
			}
			if err := (&Config{Version: version}).Validate(); err == nil {
				t.Fatal("newer config loaded")
			}
		})
	}
}

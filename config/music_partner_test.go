package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMusicPartnerRecommendDefaultsAndExplicitSettings(t *testing.T) {
	for _, fields := range []string{"", "  recommendCount: 0\n", "  recommendCount: 0\n  recommendNotes: []\n  recommendComments: []\n"} {
		for _, upgrade := range []bool{false, true} {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("version: 1.2.2\nmusicPartner:\n  enableMain: true\n"+fields), 0600); err != nil {
				t.Fatal(err)
			}
			if upgrade {
				if err := AutoUpgradeConfigIfNeeded(path); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := New(path)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(cfg.MusicPartner)
			var values map[string]any
			_ = json.Unmarshal(data, &values)
			if values["recommendCountMin"] != float64(0) || values["recommendCountMax"] != float64(0) || values["recommendOrder"] != "sequential" || values["recommendWaitMin"] != float64(16) || values["recommendWaitMax"] != float64(20) {
				t.Fatalf("recommendation defaults missing or enabled: %s", data)
			}
		}
	}
}

func TestMusicPartnerRecommendTemplatesAndBounds(t *testing.T) {
	valid := MusicPartnerConf{RecommendCount: 3, RecommendWaitMin: 16, RecommendWaitMax: 300, RecommendNotes: []string{"  分享《{name}》—{artist}  ", strings.Repeat("字", 500)}, RecommendComments: []string{"{artist} 的《{name}》"}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if valid.RecommendNotes[0] != "  分享《{name}》—{artist}  " || valid.RecommendWaitMin != 16 || valid.RecommendWaitMax != 300 {
		t.Fatal("validation rewrote explicit templates or bounds")
	}
	for _, invalid := range []MusicPartnerConf{
		{RecommendCount: -1}, {RecommendCountMin: -1}, {RecommendCountMin: 5, RecommendCountMax: 4}, {RecommendCountMax: -1},
		{RecommendOrder: "invalid"}, {RecommendOrder: "Random"},
		{RecommendCountMax: 5}, {RecommendCountMax: 10, RecommendNotes: []string{"笔记"}},
		{RecommendCount: 1}, {RecommendCount: 1, RecommendNotes: []string{"笔记"}}, {RecommendCount: 1, RecommendComments: []string{"乐评"}},
		{RecommendWaitMin: 15, RecommendWaitMax: 20}, {RecommendWaitMin: 16}, {RecommendWaitMax: 20},
		{RecommendWaitMin: 20, RecommendWaitMax: 19}, {RecommendWaitMin: 16, RecommendWaitMax: 301},
		{RecommendNotes: []string{""}}, {RecommendComments: []string{" \n\t"}},
		{RecommendNotes: []string{strings.Repeat("字", 501)}}, {RecommendComments: []string{strings.Repeat("字", 501)}},
		{RecommendNotes: make([]string, 51)}, {RecommendComments: make([]string, 51)},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("accepted invalid recommendation settings: %+v", invalid)
		}
	}
}

func TestMusicPartnerRecommendInvalidFileIsNeverRewritten(t *testing.T) {
	for _, fields := range []string{
		"  recommendCount: -1\n", "  recommendCount: 1.5\n", "  recommendCount: null\n",
		"  recommendCountMin: 0\n", "  recommendCountMax: 0\n", "  recommendCountMin: -1\n  recommendCountMax: 3\n",
		"  recommendCountMin: 5\n  recommendCountMax: 4\n", "  recommendCountMin: 0\n  recommendCountMax: -1\n",
		"  recommendCountMin: null\n  recommendCountMax: 0\n", "  recommendCountMin: 0\n  recommendCountMax: null\n",
		"  recommendCountMin: 1.5\n  recommendCountMax: 5\n", "  recommendOrder: invalid\n", "  recommendOrder: null\n", "  recommendOrder: ''\n",
		"  recommendCount: 1\n", "  recommendCount: 1\n  recommendNotes: ['公开笔记']\n", "  recommendCount: 1\n  recommendComments: ['公开乐评']\n",
		"  recommendWaitMin: 16\n", "  recommendWaitMax: 20\n", "  recommendWaitMin: 0\n  recommendWaitMax: 0\n",
		"  recommendWaitMin: 15\n  recommendWaitMax: 20\n", "  recommendWaitMin: 16\n  recommendWaitMax: 301\n",
		"  recommendWaitMin: 16.5\n  recommendWaitMax: 20\n", "  recommendWaitMin: 20\n  recommendWaitMax: 19\n",
		"  recommendWaitMin: null\n  recommendWaitMax: 20\n", "  recommendWaitMin: 16\n  recommendWaitMax: null\n",
		"  recommendWaitMin: null\n  recommendWaitMax: null\n", "  recommendNotes: ['   ']\n", "  recommendComments: ['']\n",
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		original := []byte("version: " + GetDefault().Version + "\nmusicPartner:\n" + fields)
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := New(path); err == nil {
			t.Fatalf("load accepted invalid recommendation settings: %s", fields)
		}
		for _, upgrade := range []func(string) error{AutoUpgradeConfigIfNeeded, AutoUpgradeConfig} {
			if err := upgrade(path); err == nil {
				t.Fatalf("upgrade masked invalid recommendation settings: %s", fields)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(original, after) {
				t.Fatal("invalid recommendation configuration overwritten")
			}
		}
	}
}

func TestMusicPartnerRecommendUpgradePreservesEnabledSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	text := "version: 1.2.2\nmusicPartner:\n  recommendCount: 2\n  recommendWaitMin: 18\n  recommendWaitMax: 25\n  recommendNotes: ['  {name} — {artist}  ']\n  recommendComments: ['喜欢《{name}》']\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
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
	if p.RecommendCountMin != 2 || p.RecommendCountMax != 2 || p.RecommendOrder != "sequential" || p.RecommendWaitMin != 18 || p.RecommendWaitMax != 25 || len(p.RecommendNotes) != 1 || p.RecommendNotes[0] != "  {name} — {artist}  " || len(p.RecommendComments) != 1 || p.RecommendComments[0] != "喜欢《{name}》" {
		t.Fatalf("upgrade changed explicit recommendation settings: %+v", p)
	}
	before, _ := os.ReadFile(path)
	if err := AutoUpgradeConfigIfNeeded(path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("recommendation configuration migration not idempotent")
	}
}

func TestMusicPartnerRecommendRangeMigrationAndRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, fields, order string
		min, max            int
	}{
		{"旧关闭", "  recommendCount: 0\n", "sequential", 0, 0},
		{"旧固定目标", "  recommendCount: 3\n", "sequential", 3, 3},
		{"旧目标不限制三首", "  recommendCount: 5\n", "sequential", 5, 5},
		{"新范围优先", "  recommendCount: 3\n  recommendCountMin: 5\n  recommendCountMax: 10\n", "sequential", 5, 10},
		{"新关闭优先", "  recommendCount: 3\n  recommendCountMin: 0\n  recommendCountMax: 0\n", "sequential", 0, 0},
		{"新范围忽略旧无效数", "  recommendCount: -1\n  recommendCountMin: 0\n  recommendCountMax: 0\n", "sequential", 0, 0},
		{"随机抽取", "  recommendCountMin: 0\n  recommendCountMax: 10\n  recommendOrder: random\n", "random", 0, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			data := "version: 1.2.2\nmusicPartner:\n" + tc.fields
			if tc.max > 0 {
				data += "  recommendNotes: ['  {name} — {artist}  ']\n  recommendComments: ['喜欢《{name}》']\n"
			}
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
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
				p := cfg.MusicPartner
				if p.RecommendCountMin != tc.min || p.RecommendCountMax != tc.max || p.RecommendOrder != tc.order {
					t.Fatalf("upgrade=%t: 推荐范围或顺序变化: %+v", upgrade, p)
				}
				for _, format := range []struct {
					marshal   func(any) ([]byte, error)
					unmarshal func([]byte, any) error
				}{{yaml.Marshal, yaml.Unmarshal}, {json.Marshal, json.Unmarshal}} {
					encoded, err := format.marshal(p)
					if err != nil {
						t.Fatal(err)
					}
					var restored MusicPartnerConf
					if err := format.unmarshal(encoded, &restored); err != nil {
						t.Fatal(err)
					}
					if err := restored.Validate(); err != nil || restored.RecommendCountMin != tc.min || restored.RecommendCountMax != tc.max || restored.RecommendOrder != tc.order {
						t.Fatalf("推荐范围序列化后变化: %+v, error=%v", restored, err)
					}
				}
			}
			before, _ := os.ReadFile(path)
			if err := AutoUpgradeConfig(path); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("推荐范围迁移不幂等")
			}
		})
	}
}

func TestMusicPartnerDefaultsAndValidation(t *testing.T) {
	c := &Config{Task: &TaskConf{}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	p := c.MusicPartner
	if c.Task.MusicPartner || !p.EnableMain || p.EnableSecondaries || p.ScoreMin != 3 || p.ScoreMax != 4 || !p.ExtraTasks || p.FullExtraTasks || p.ExtraLimitMin != 5 || p.ExtraLimitMax != 15 || p.WaitMin != 15 || p.WaitMax != 20 || p.SyncYunCircle || len(p.Comments) != 0 {
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
				text := "version: " + GetDefault().Version + "\nmusicPartner:\n  enableMain: false\n  score: " + strategy + "\n  extraLimit: 12\n  waitMin: 21\n  waitMax: 25\n  comments:\n    '4': ['  自定义乐评  ', '另一条']\n"
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
				if p.ScoreMin != wantMin || p.ScoreMax != wantMax || p.ExtraLimitMin != 12 || p.ExtraLimitMax != 12 || p.WaitMin != 21 || p.WaitMax != 25 || p.EnableMain || len(p.Comments["4"]) != 2 || p.Comments["4"][0] != "  自定义乐评  " {
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
		{ExtraLimitMin: 1}, {ExtraLimitMin: -1, ExtraLimitMax: 7}, {ExtraLimitMin: 8, ExtraLimitMax: 7}, {ExtraLimitMin: 1, ExtraLimitMax: 501},
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
		"  extraLimitMin: 3\n",
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
	for _, version := range []string{"1.2.10", "1.3.0", "2.0.0"} {
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

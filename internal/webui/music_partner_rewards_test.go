package webui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMusicPartnerAccountPathNormalization(t *testing.T) {
	for _, tt := range []struct {
		name, first, second string
		count               int
	}{
		{"unix case distinct", "/data/A/cookie.json", "/data/a/cookie.json", 2},
		{"relative case distinct", "A/cookie.json", "a/cookie.json", 2},
		{"relative aliases", "./cookie.json", "cookie.json", 1},
		{"relative clean", "sub/../cookie.json", "cookie.json", 1},
		{"windows drive aliases", `C:\Data\A\cookie.json`, "c:/data/a/cookie.json", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var log strings.Builder
			for i, account := range []string{tt.first, tt.second} {
				encoded, _ := json.Marshal(account)
				fmt.Fprintf(&log, "[NCMM_REWARD] {\"version\":1,\"account\":%s,\"domain\":\"musicPartner\",\"musicPartner\":{\"delta\":%d,\"known\":true}}\n", encoded, i+2)
			}
			parsed := parseRunRewardsReader(strings.NewReader(log.String()))
			if len(parsed) != tt.count {
				t.Fatalf("parser merged/separated incorrect accounts: %+v", parsed)
			}
			if tt.count == 1 && parsed[0].MusicPartnerDelta != 5 {
				t.Fatalf("alias delta lost: %+v", parsed)
			}
			if tt.count == 2 && (parsed[0].MusicPartnerDelta != 2 || parsed[1].MusicPartnerDelta != 3) {
				t.Fatalf("case-sensitive deltas mixed: %+v", parsed)
			}
			merged := mergeRunRewards([]RunReward{{Account: tt.first, Nickname: "cached"}}, []RunReward{{Account: tt.second, MusicPartnerDelta: 9, MusicPartnerKnown: true, musicPartnerObserved: true}})
			if len(merged) != tt.count || merged[len(merged)-1].MusicPartnerDelta != 9 {
				t.Fatalf("cache merge identity failed: %+v", merged)
			}
			if tt.count == 1 && merged[0].Nickname != "cached" {
				t.Fatal("alias cache metadata lost")
			}
		})
	}
}

func TestParseMusicPartnerRewards(t *testing.T) {
	for _, tt := range []struct {
		name, log string
		delta     int64
		known     bool
	}{
		{"task legacy", "[task] >>> 账号 (cookie.json) 开始执行 [音乐合伙人评分] <<<\n[2026-10-23 01:02:03] [music-partner] 本期积分：100 -> 105，变化 +5（平台账面变化）\n[music-partner] 本周积分：20 -> 25，变化 +5（平台账面变化）", 5, true},
		{"standalone legacy", "[2026-10-23 01:02:03] [music-partner] 开始处理账号 fan.json\n[music-partner] 本期积分：100 -> 95，变化 -5（平台账面变化）", -5, true},
		{"multiple stages", "[task] >>> 账号 (cookie.json) 开始执行 [音乐合伙人评分] <<<\n[music-partner] 本期积分：100 -> 105，变化 +5（平台账面变化）\n[music-partner] 本期积分：105 -> 108，变化 +3（平台账面变化）", 8, true},
		{"cross period", "[task] >>> 账号 (cookie.json) 开始执行 [音乐合伙人评分] <<<\n[music-partner] 本期积分：100 -> 5，差值未计算（期号变化）", 0, false},
		{"structured deduplicates text", "[task] >>> 账号 (cookie.json) 开始执行 [音乐合伙人评分] <<<\n[music-partner] 本期积分：100 -> 105，变化 +5（平台账面变化）\n[NCMM_REWARD] {\"version\":1,\"account\":\"cookie.json\",\"domain\":\"musicPartner\",\"musicPartner\":{\"delta\":5,\"known\":true}}\n[NCMM_REWARD] {\"version\":1,\"account\":\"cookie.json\",\"domain\":\"musicPartner\",\"musicPartner\":{\"delta\":-2,\"known\":true}}\n[music-partner] 本期积分：105 -> 103，变化 -2（平台账面变化）", 3, true},
		{"unknown stage", "[NCMM_REWARD] {\"version\":1,\"account\":\"cookie.json\",\"domain\":\"musicPartner\",\"musicPartner\":{\"delta\":0,\"known\":false}}\n[NCMM_REWARD] {\"version\":1,\"account\":\"cookie.json\",\"domain\":\"musicPartner\",\"musicPartner\":{\"delta\":3,\"known\":true}}", 3, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rewards := parseRunRewardsReader(strings.NewReader(tt.log))
			if len(rewards) != 1 {
				t.Fatalf("rewards = %+v", rewards)
			}
			data, _ := json.Marshal(rewards[0])
			var decoded map[string]any
			_ = json.Unmarshal(data, &decoded)
			if decoded["musicPartnerDelta"] != float64(tt.delta) || decoded["musicPartnerKnown"] != tt.known {
				t.Fatalf("rewards = %s", data)
			}
		})
	}
}

func TestMusicPartnerRewardsKeepAccountsSeparate(t *testing.T) {
	log := `[task] >>> 账号 (cookie.json) 开始执行 [音乐合伙人评分] <<<
[2026-10-23 01:02:03] [music-partner] >>> 账号 (cookie.json) 开始评分 <<<
[music-partner] 本期积分：100 -> 109，变化 +9（平台账面变化）
[NCMM_REWARD] {"version":1,"account":"cookie.json","domain":"musicPartner","musicPartner":{"delta":9,"known":true}}
[music-partner] >>> 账号 (sub/cookie.json) 开始评分 <<<
[music-partner] 本期积分：100 -> 98，变化 -2（平台账面变化）
[NCMM_REWARD] {"version":1,"account":"sub/cookie.json","domain":"musicPartner","musicPartner":{"delta":-2,"known":true}}
`
	rewards := parseRunRewardsReader(strings.NewReader(log))
	if len(rewards) != 2 || rewards[0].Account != "cookie.json" || rewards[0].MusicPartnerDelta != 9 || rewards[1].Account != "sub/cookie.json" || rewards[1].MusicPartnerDelta != -2 {
		t.Fatalf("account identity/dedup failed: %+v", rewards)
	}
}

func TestMusicPartnerHistoryBackfillsCachedRewards(t *testing.T) {
	dir := t.TempDir()
	cached := RunRecord{ID: "history", Status: "success", Rewards: []RunReward{{Account: `C:\ncmm\cookie.json`, Nickname: "保留账号", GrowthKnown: true, GrowthTotal: 90, YunbeiKnown: true, Yunbei: 7}}}
	meta, _ := json.Marshal(cached)
	metaPath := filepath.Join(dir, "history.json")
	if err := os.WriteFile(metaPath, meta, 0600); err != nil {
		t.Fatal(err)
	}
	log := "[task] >>> 账号 (C:/ncmm/cookie.json) 开始执行 [音乐合伙人评分] <<<\n[music-partner] 本期积分：100 -> 109，变化 +9（平台账面变化）\n"
	if err := os.WriteFile(filepath.Join(dir, "history.log"), []byte(log), 0600); err != nil {
		t.Fatal(err)
	}
	m := &runManager{logDir: dir, runs: make(map[string]RunRecord)}
	m.loadHistory()
	reward := m.runs["history"].Rewards[0]
	if !reward.MusicPartnerKnown || reward.MusicPartnerDelta != 9 || reward.Nickname != "保留账号" || !reward.GrowthKnown || reward.GrowthTotal != 90 || !reward.YunbeiKnown || reward.Yunbei != 7 {
		t.Fatalf("backfill lost cached fields: %+v", reward)
	}
	updated, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted RunRecord
	if err := json.Unmarshal(updated, &persisted); err != nil {
		t.Fatal(err)
	}
	if !persisted.Rewards[0].MusicPartnerKnown || persisted.Rewards[0].MusicPartnerDelta != 9 {
		t.Fatal("reward backfill not persisted")
	}
}

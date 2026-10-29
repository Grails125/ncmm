package ncmm

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/api/types"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/pkg/notify"
)

func TestMusicPartnerScoreRangeAndPerScoreComments(t *testing.T) {
	for fixed := 1; fixed <= 5; fixed++ {
		cfg := &config.MusicPartnerConf{ScoreMin: fixed, ScoreMax: fixed, Comments: map[string][]string{strconv.Itoa(fixed): {"甲", "乙"}}}
		for i := 0; i < 20; i++ {
			score, tag, comment := musicPartnerScore(cfg)
			if score != strconv.Itoa(fixed) || string(tag) != score+"-A-1" || (comment != "甲" && comment != "乙") {
				t.Fatalf("invalid fixed score or comment: %s %s %s", score, tag, comment)
			}
		}
	}
	cfg := &config.MusicPartnerConf{ScoreMin: 2, ScoreMax: 5}
	for i := 0; i < 200; i++ {
		score, tag, comment := musicPartnerScore(cfg)
		n, _ := strconv.Atoi(score)
		if n < 2 || n > 5 || string(tag) != score+"-A-1" || comment != "" {
			t.Fatal("score outside configured range")
		}
	}
}

func TestMusicPartnerDailyTargetStableAndWithinRange(t *testing.T) {
	for id := int64(1); id < 100; id++ {
		cfg := &config.MusicPartnerConf{ExtraLimitMin: 7, ExtraLimitMax: 12}
		target := musicPartnerDailyTarget(id, cfg)
		if target < 7 || target > 12 || musicPartnerDailyTarget(id, cfg) != target {
			t.Fatal("unstable daily target or outside range")
		}
		cfg.ExtraLimitMax = 7
		if musicPartnerDailyTarget(id, cfg) != 7 {
			t.Fatal("equal bounds must be fixed")
		}
	}
}

func TestMusicPartnerRangeRunOnlyFillsRemainingDailyTarget(t *testing.T) {
	command, cfg := partnerTestCommand(t)
	cfg.ExtraTasks = true
	cfg.ExtraLimitMin, cfg.ExtraLimitMax = 7, 12
	cfg.WaitMin, cfg.WaitMax = 21, 25
	completed := 2
	waits := 0
	command.wait = func(_ context.Context, duration time.Duration) error {
		if duration < 21*time.Second || duration > 25*time.Second {
			t.Fatalf("wait outside configured range: %v", duration)
		}
		waits++
		return nil
	}
	fake := &partnerFake{
		daily: func(context.Context) (*weapi.PartnerTaskResp, error) { return partnerTestDaily(true), nil },
		extra: func(context.Context) (*weapi.PartnerExtraTaskResp, error) {
			return partnerTestExtra(15, completed), nil
		},
		evaluate: func(context.Context, *weapi.PartnerEvaluateReq) (*weapi.PartnerEvaluateResp, error) {
			completed++
			return &weapi.PartnerEvaluateResp{RespCommon: types.RespCommon[weapi.PartnerEvaluateRespData]{Code: 200, Data: weapi.PartnerEvaluateRespData{EvaluateRes: true}}}, nil
		},
	}
	for i := 0; i < 2; i++ {
		if err := command.run(context.Background(), fake, cfg, "account.json"); err != nil {
			t.Fatal(err)
		}
	}
	target := musicPartnerDailyTarget(10, cfg)
	if completed != target || len(fake.evaluations) != target-2 || waits != target-2 {
		t.Fatalf("repeat run exceeded daily target: completed=%d target=%d writes=%d waits=%d", completed, target, len(fake.evaluations), waits)
	}
}

func TestMusicPartnerInsufficientCandidatesDoNotCompleteTarget(t *testing.T) {
	for _, available := range []int{0, 3} {
		command, cfg := partnerTestCommand(t)
		command.root.Report = notify.NewReport("music-partner")
		cfg.ExtraTasks = true
		fake := &partnerFake{
			daily: func(context.Context) (*weapi.PartnerTaskResp, error) { return partnerTestDaily(true), nil },
			extra: func(context.Context) (*weapi.PartnerExtraTaskResp, error) { return partnerTestExtra(available, 0), nil },
		}
		err := command.run(context.Background(), fake, cfg, "account.json")
		if err != nil {
			t.Fatalf("insufficient candidates should skip the remaining target: available=%d error=%v", available, err)
		}
		if failed, skipped, unknown := command.root.Report.OutcomeCounts(); failed != 0 || skipped != 1 || unknown != 0 {
			t.Fatalf("insufficient candidates must remain a skip: %+v", command.root.Report.Items())
		}
		if len(fake.evaluations) != available {
			t.Fatal("must score only the available candidates")
		}
		cfg.FullExtraTasks = true
		if err := command.run(context.Background(), fake, cfg, "account.json"); err != nil {
			t.Fatalf("full-list mode should use available candidates as target: %v", err)
		}
	}
}

func TestMusicPartnerDailyQuotaIsNotAccountBlock(t *testing.T) {
	command, cfg := partnerTestCommand(t)
	command.root.Report = notify.NewReport("music-partner")
	var output bytes.Buffer
	command.Command().SetOut(&output)
	cfg.Comments = map[string][]string{"3": {"三分乐评"}}
	cfg.ExtraTasks = true
	fake := &partnerFake{daily: func(context.Context) (*weapi.PartnerTaskResp, error) { return partnerTestDaily(true), nil }, extra: func(context.Context) (*weapi.PartnerExtraTaskResp, error) {
		list := partnerTestExtra(3, 0)
		list.Data[0].Work.Name = "灯火阑珊处"
		return list, nil
	}, evaluate: func(_ context.Context, req *weapi.PartnerEvaluateReq) (*weapi.PartnerEvaluateResp, error) {
		if req.Comment != "三分乐评" {
			t.Fatal("configured comment was not submitted")
		}
		var response weapi.PartnerEvaluateResp
		if err := json.Unmarshal([]byte(`{"code":405,"message":"今日没有更多可评定歌曲啦","debugInfo":null,"data":null,"failData":null}`), &response); err != nil {
			t.Fatal(err)
		}
		return &response, nil
	}}
	err := command.run(context.Background(), fake, cfg, "account.json")
	if err != nil {
		t.Fatalf("daily quota should stop normally: %v", err)
	}
	if failed, skipped, unknown := command.root.Report.OutcomeCounts(); failed != 0 || skipped != 1 || unknown != 0 {
		t.Fatalf("daily quota must be reported as a skip: %+v", command.root.Report.Items())
	}
	if len(fake.evaluations) != 1 || len(fake.reports) != 1 {
		t.Fatal("daily quota triggered more submissions")
	}
	if musicPartnerResponseClass(405, "今日没有更多可评定歌曲啦") != "当日可评歌曲已用尽" {
		t.Fatal("quota misclassified")
	}
	failure := musicPartnerResponseError("作品评分", 405, "今日没有更多可评定歌曲啦").(*musicPartnerFailure)
	if failure.accountBlocked || !failure.stop {
		t.Fatal("quota must stop this task without blocking unrelated account tasks")
	}
	for _, want := range []string{"灯火阑珊处", "作者", "今日没有更多可评定歌曲啦", "评分请求数据", "评分响应数据"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q in diagnostic log", want)
		}
	}
}

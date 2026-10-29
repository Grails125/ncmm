package ncmm

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/3899/ncmm/api/types"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/pkg/notify"
)

func TestMusicPartnerQuotaSkipsEveryStageWithoutRetry(t *testing.T) {
	for _, phase := range []string{"daily-list", "daily-score", "extra-list", "extra-report", "extra-score"} {
		t.Run(phase, func(t *testing.T) {
			command, cfg := partnerTestCommand(t)
			command.root.Report = notify.NewReport("task")
			cfg.ExtraTasks = true
			message := "今日没有更多可评定歌曲啦"
			fake := &partnerFake{
				daily: func(context.Context) (*weapi.PartnerTaskResp, error) {
					if phase == "daily-list" {
						return &weapi.PartnerTaskResp{RespCommon: types.RespCommon[weapi.PartnerTaskRespData]{Code: 405, Message: message}}, nil
					}
					if phase == "daily-score" {
						return partnerTestDaily(false, false), nil
					}
					return partnerTestDaily(true), nil
				},
				extra: func(context.Context) (*weapi.PartnerExtraTaskResp, error) {
					if phase == "extra-list" {
						return &weapi.PartnerExtraTaskResp{RespCommon: types.RespCommon[[]weapi.PartnerExtraTaskRespData]{Code: 405, Message: message}}, nil
					}
					return partnerTestExtra(3, 0), nil
				},
				report: func(context.Context, *weapi.PartnerExtraReportReq) (*weapi.PartnerExtraReportResp, error) {
					return &weapi.PartnerExtraReportResp{RespCommon: types.RespCommon[weapi.PartnerExtraReportRespData]{Code: 405, Message: message}}, nil
				},
				evaluate: func(context.Context, *weapi.PartnerEvaluateReq) (*weapi.PartnerEvaluateResp, error) {
					return &weapi.PartnerEvaluateResp{RespCommon: types.RespCommon[weapi.PartnerEvaluateRespData]{Code: 405, Message: message}}, nil
				},
			}
			if phase != "extra-report" {
				fake.report = nil
			}
			if err := command.run(context.Background(), fake, cfg, "account.json"); err != nil {
				t.Fatalf("quota must not fail the command: %v", err)
			}
			failed, skipped, unknown := command.root.Report.OutcomeCounts()
			if failed != 0 || skipped != 1 || unknown != 0 {
				t.Fatalf("quota outcome mismatch: %+v", command.root.Report.Items())
			}
			if item := command.root.Report.Items()[0]; item.Account != "account.json" || item.Kind != notify.KindSkip {
				t.Fatalf("quota skip lost its account: %+v", item)
			}
			wantScores, wantReports := 0, 0
			switch phase {
			case "daily-score":
				wantScores = 1
			case "extra-report":
				wantReports = 1
			case "extra-score":
				wantScores, wantReports = 1, 1
			}
			if len(fake.evaluations) != wantScores || len(fake.reports) != wantReports {
				t.Fatalf("continued submitting after quota: %d scores, %d reports", len(fake.evaluations), len(fake.reports))
			}
			task := NewTask(command.root, nil)
			var output bytes.Buffer
			task.cmd.SetOut(&output)
			if err := task.finishRun(); err != nil || !strings.Contains(output.String(), "0 项任务失败，1 项跳过，0 项未确认") {
				t.Fatalf("quota failed batch summary: err=%v output=%s", err, output.String())
			}
		})
	}
}

func TestMusicPartnerQuotaPreservesEarlierFailure(t *testing.T) {
	command, cfg := partnerTestCommand(t)
	command.root.Report = notify.NewReport("task")
	fake := &partnerFake{
		daily: func(context.Context) (*weapi.PartnerTaskResp, error) {
			return partnerTestDaily(false, false, false), nil
		},
		evaluate: func(_ context.Context, req *weapi.PartnerEvaluateReq) (*weapi.PartnerEvaluateResp, error) {
			code, message := int64(500), "服务暂不可用"
			if req.WorkId == "2" {
				code, message = 405, "今日没有更多可评定歌曲啦"
			}
			return &weapi.PartnerEvaluateResp{RespCommon: types.RespCommon[weapi.PartnerEvaluateRespData]{Code: code, Message: message}}, nil
		},
	}
	err := command.run(context.Background(), fake, cfg, "account.json")
	if err == nil || !strings.Contains(err.Error(), "服务码 500") || strings.Contains(err.Error(), "服务码 405") {
		t.Fatalf("quota should preserve only the earlier actual failure: %v", err)
	}
	if len(fake.evaluations) != 2 {
		t.Fatal("scoring continued after quota")
	}
	task := NewTask(command.root, nil)
	task.reportFailure("account.json", "music-partner", err)
	if err := task.finishRun(); err == nil {
		t.Fatal("earlier failure was hidden by a later quota")
	}
	if failed, skipped, unknown := command.root.Report.OutcomeCounts(); failed != 1 || skipped != 1 || unknown != 0 {
		t.Fatalf("mixed failure/quota summary mismatch: %+v", command.root.Report.Items())
	}
}

func TestMusicPartnerQuotaDoesNotHideCancellation(t *testing.T) {
	for _, phase := range []string{"daily-list", "extra-list", "extra-report"} {
		t.Run(phase, func(t *testing.T) {
			command, cfg := partnerTestCommand(t)
			cfg.ExtraTasks = true
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fake := &partnerFake{
				daily: func(context.Context) (*weapi.PartnerTaskResp, error) {
					if phase == "daily-list" {
						cancel()
						return &weapi.PartnerTaskResp{RespCommon: types.RespCommon[weapi.PartnerTaskRespData]{Code: 405, Message: "今日没有更多可评定歌曲啦"}}, nil
					}
					return partnerTestDaily(true), nil
				},
				extra: func(context.Context) (*weapi.PartnerExtraTaskResp, error) {
					if phase == "extra-list" {
						cancel()
						return &weapi.PartnerExtraTaskResp{RespCommon: types.RespCommon[[]weapi.PartnerExtraTaskRespData]{Code: 405, Message: "今日没有更多可评定歌曲啦"}}, nil
					}
					return partnerTestExtra(2, 0), nil
				},
				report: func(context.Context, *weapi.PartnerExtraReportReq) (*weapi.PartnerExtraReportResp, error) {
					cancel()
					return &weapi.PartnerExtraReportResp{RespCommon: types.RespCommon[weapi.PartnerExtraReportRespData]{Code: 405, Message: "今日没有更多可评定歌曲啦"}}, nil
				},
			}
			if err := command.run(ctx, fake, cfg, "account.json"); !errors.Is(err, context.Canceled) {
				t.Fatalf("quota swallowed cancellation: %v", err)
			}
			if len(fake.evaluations) != 0 {
				t.Fatal("scoring continued after cancellation")
			}
		})
	}
}

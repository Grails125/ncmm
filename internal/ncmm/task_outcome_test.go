package ncmm

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/types"
	"github.com/3899/ncmm/pkg/notify"
)

func TestTaskSummaryIncludesInternalOutcomes(t *testing.T) {
	root := &Root{Report: notify.NewReport("task")}
	c := NewTask(root, nil)
	var output bytes.Buffer
	c.cmd.SetOut(&output)
	root.ReportSkip("account", "fansgroup/播放", "未开启网页播放")
	root.ReportUnconfirmed("account", "vip/乐签", "官方状态未完成")
	root.ReportUnconfirmed("account", "vip/乐签", "官方状态未完成")
	err := c.finishRun()
	if err == nil || !strings.Contains(output.String(), "1 项跳过，1 项未确认") {
		t.Fatalf("内部任务未确认被计为成功：err=%v output=%s", err, output.String())
	}
	if !strings.Contains(output.String(), "fansgroup/播放") || !strings.Contains(output.String(), "官方状态未完成") {
		t.Fatalf("汇总缺少任务及原因：%s", output.String())
	}
}

func TestMusicianVipInvalidPlayProgressReportsInternalFailure(t *testing.T) {
	f := newMusicianVipClaimFixture(t)
	f.command.root.Report = notify.NewReport("task")
	f.ctx.resp = &eapi.MusicianVipTasksResp{RespCommon: types.RespCommon[eapi.MusicianVipTasksData]{Code: 200, Data: eapi.MusicianVipTasksData{FurtherTask: &eapi.MusicianVipFurtherTask{Children: []eapi.MusicianVipSubTask{{MissionCode: musicianPlayPercentCode, Name: "有效播放", TotalCompleteNum: 0}}}}}}
	f.command.doVipPhase(context.Background(), f.ctx, "account-a.json")
	if failures, _, _ := f.command.root.Report.OutcomeCounts(); failures != 1 {
		t.Fatalf("内部播放失败不能仅打印日志：%+v", f.command.root.Report.Items())
	}
}

func TestMusicianVipMissingDetailsDoesNotInventFailedRequirements(t *testing.T) {
	f := newMusicianVipClaimFixture(t)
	f.command.root.Report = notify.NewReport("task")
	f.info.Data.TaskStatus = false
	f.info.Data.FurtherTask = nil
	f.info.Data.HasFurtherTask = true
	f.run("account-a.json")
	if strings.Contains(f.output.String(), "笔记/播放量") || !strings.Contains(f.output.String(), "未返回进阶任务详情") {
		t.Fatalf("缺少详情时不应断言未达标：%s", f.output.String())
	}
	_, _, unconfirmed := f.command.root.Report.OutcomeCounts()
	if unconfirmed != 1 || f.claimCalls != 0 {
		t.Fatalf("缺少详情必须保留未确认且不能提交领取：unknown=%d calls=%d", unconfirmed, f.claimCalls)
	}
}

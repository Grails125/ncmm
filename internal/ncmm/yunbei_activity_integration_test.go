package ncmm

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/pkg/notify"
)

func TestYunbeiActivityClientDefaultsStayScoped(t *testing.T) {
	original := &api.Config{Debug: true, Retry: 3}
	configured, err := yunbeiActivityNetworkConfig(original, filepath.Join(t.TempDir(), "account.json"))
	if err != nil {
		t.Fatal(err)
	}
	if configured.UserAgent.XEAPI != api.DefaultXEAPIUserAgent || configured.Debug || configured.Retry != 0 || !configured.Cookie.ReadOnly {
		t.Fatalf("活动专用客户端不符合约束：%+v", configured)
	}
	if original.UserAgent.XEAPI != "" || !original.Debug || original.Retry != 3 || original.Cookie.ReadOnly || original.Cookie.Filepath != "" {
		t.Fatal("活动默认值污染了原账号客户端配置")
	}
	original.UserAgent.XEAPI = "user-configured-Android-UA"
	configured, err = yunbeiActivityNetworkConfig(original, "account.json")
	if err != nil || configured.UserAgent.XEAPI != original.UserAgent.XEAPI || !filepath.IsAbs(configured.Cookie.Filepath) {
		t.Fatalf("手工UA或账号路径未保留：%+v %v", configured, err)
	}
}

func TestYunbeiVideoMissingSessionSkipsBeforeCreatingAnyClient(t *testing.T) {
	c, _ := activityTestSign()
	c.root.Cfg = &config.Config{Sign: &config.SignConf{Automatic: true, YunbeiTask: &config.YunbeiTaskConf{EnableLotteryWatchVideo: true, EnableWatchVideoYunbei: true}}}
	report := &accountEarningsReport{Account: "account-a"}
	allowed := map[string]bool{"LotteryWatchVideo": true, "WatchVideoYunbei": true}
	for range 2 {
		if err := c.handleYunbeiActivities(t.Context(), nil, "account-a", allowed, report); err != nil {
			t.Fatal(err)
		}
	}
	failed, skipped, unknown := c.root.Report.OutcomeCounts()
	if failed != 0 || skipped != 2 || unknown != 0 {
		t.Fatalf("缺移动会话不应先发活动请求或记失败：%+v", c.root.Report.Items())
	}
}

func TestYunbeiActivitiesDoNotReuseAnotherAccountsBatchState(t *testing.T) {
	c, _ := activityTestSign()
	c.root.Cfg = &config.Config{Sign: &config.SignConf{Automatic: true, YunbeiTask: &config.YunbeiTaskConf{EnableLotteryWatchVideo: true}}}
	report := &accountEarningsReport{Account: "account-a", yunbeiActivitiesHandled: map[string]bool{"LotteryWatchVideo": true}}
	if err := c.handleYunbeiActivities(t.Context(), nil, "account-b", map[string]bool{"LotteryWatchVideo": true}, report); err != nil {
		t.Fatal(err)
	}
	items := c.root.Report.Items()
	if len(items) != 1 || items[0].Account != "account-b" {
		t.Fatalf("另一账号被前账号处理状态阻断：%+v", items)
	}
}

func TestYunbeiActivityDisabledAndCancellation(t *testing.T) {
	c, _ := activityTestSign()
	c.root.Cfg = &config.Config{Sign: &config.SignConf{Automatic: true, YunbeiTask: &config.YunbeiTaskConf{}}}
	if err := c.handleYunbeiActivities(t.Context(), nil, "account-a", map[string]bool{"RedPacket": true}, nil); err != nil {
		t.Fatal(err)
	}
	if len(c.root.Report.Items()) != 0 {
		t.Fatal("默认关闭的新活动不应产生请求或额外跳过项")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.handleYunbeiActivities(ctx, nil, "account-a", nil, nil); err != context.Canceled {
		t.Fatalf("取消信号丢失：%v", err)
	}
}

func TestYunbeiVideoChancesRequireOptInAndNoUncertainWrite(t *testing.T) {
	c, _ := activityTestSign()
	c.root.Cfg = &config.Config{Sign: &config.SignConf{YunbeiTask: &config.YunbeiTaskConf{EnableLottery: true}}}
	report := &accountEarningsReport{Account: "account-a", yunbeiActivitiesHandled: map[string]bool{"Lottery": true}}
	if !c.canDrawYunbeiVideoChances("account-a", report) {
		t.Fatal("已启用且本批次已处理的抽奖应允许核对新增机会")
	}
	c.root.Report.AddUnconfirmed("account-a", "云贝/刮刮乐", "写入结果未知")
	if c.canDrawYunbeiVideoChances("account-a", report) {
		t.Fatal("不得在同批次重开已有未知写回执的活动")
	}
	c.root.Report = notify.NewReport("test")
	c.root.Report.AddFailureMsg("account-b", "云贝/刮刮乐", "其它账号失败")
	if !c.canDrawYunbeiVideoChances("account-a", report) {
		t.Fatal("不能跨账号共享失败状态")
	}
	report.yunbeiActivitiesHandled["Lottery"] = false
	if c.canDrawYunbeiVideoChances("account-a", report) {
		t.Fatal("仅慢任务不应额外执行未选中的快任务抽奖")
	}
}

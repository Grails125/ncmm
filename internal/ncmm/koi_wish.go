package ncmm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/weapi"
)

const koiWishTask = "vip-koi-wish"

type koiWishAPI interface {
	VipLotteryModuleList(context.Context) (*weapi.VipLotteryModuleListResp, error)
	VipLotteryFreeClick(context.Context, *eapi.VipLotteryFreeClickReq) (*eapi.VipLotteryFreeClickResp, error)
}

type koiWishClient struct {
	reader *weapi.Api
	write  func(context.Context, *eapi.VipLotteryFreeClickReq) (*eapi.VipLotteryFreeClickResp, error)
	mode   api.CryptoMode
}

func (c *koiWishClient) VipLotteryModuleList(ctx context.Context) (*weapi.VipLotteryModuleListResp, error) {
	return c.reader.VipLotteryModuleList(ctx)
}

func (c *koiWishClient) VipLotteryFreeClick(ctx context.Context, request *eapi.VipLotteryFreeClickReq) (*eapi.VipLotteryFreeClickResp, error) {
	requestCopy := *request
	requestCopy.CryptoMode = c.mode
	return c.write(ctx, &requestCopy)
}

func (c *SignIn) handleKoiWish(ctx context.Context, cli *api.Client, cookieFile string, earningsReport *accountEarningsReport) error {
	client := &koiWishClient{reader: weapi.New(cli), write: eapi.New(cli).VipLotteryFreeClick}
	return c.runKoiWish(ctx, client, cookieFile, earningsReport, func() string {
		// 先核对官方免费次数；只有需要写入时才要求移动端身份与令牌。
		client.mode = koiWishProtocol(cli)
		if client.mode == "" || c.root == nil || c.root.Cfg == nil || c.root.Cfg.Accounts == nil {
			return ""
		}
		return c.root.Cfg.Accounts.AntiCheatTokenFor(cookieFile)
	})
}

func koiWishProtocol(cli *api.Client) api.CryptoMode {
	// 未知或网页 Cookie 不从配置 UA 推测为移动端，也不改写 Cookie 身份。
	platform := vipMemberGiftPlatformFromText(vipMemberGiftCookieValue(cli, "os"))
	switch platform {
	case "android":
		if vipMemberGiftPlatformFromText(cli.UserAgent(api.CryptoModeXEAPI)) == "android" {
			return api.CryptoModeXEAPI
		}
	case "ios":
		if vipMemberGiftPlatformFromText(cli.UserAgent(api.CryptoModeEAPI)) == "ios" {
			return api.CryptoModeEAPI
		}
	}
	return ""
}

type koiWishState struct {
	prizeTableID, freeCount, participated     int64
	serverTime, startTime, endTime, awardTime int64
	vip, used                                 bool
	status                                    int
}

func koiWishReadState(response *weapi.VipLotteryModuleListResp) (koiWishState, error) {
	var state koiWishState
	if response == nil {
		return state, fmt.Errorf("官方未返回锦鲤状态")
	}
	if response.Code != 200 {
		return state, fmt.Errorf("查询锦鲤状态失败（code=%d）", response.Code)
	}
	d := response.Data
	if d == nil || d.VIP == nil || d.ThisDayCanUseFreeCount == nil || d.UseEveryDayFreeCount == nil || d.ActivityBaseInfo == nil || d.ActivityProcessInfo == nil || d.ActivityProcessInfo.Status == nil || d.ActivityProcessInfo.VisitorParticipateNum == nil {
		return state, fmt.Errorf("官方锦鲤状态缺少必要字段")
	}
	a, p := d.ActivityBaseInfo, d.ActivityProcessInfo
	if p.PrizeTableID <= 0 || *d.ThisDayCanUseFreeCount < 0 || *p.VisitorParticipateNum < 0 || *p.Status < -1 || a.ServiceTimestamp <= 0 || a.StartTime <= 0 || a.EndTime <= a.StartTime || a.AwardTimestamp <= a.StartTime {
		return state, fmt.Errorf("官方锦鲤活动或次数数据无效")
	}
	return koiWishState{prizeTableID: p.PrizeTableID, freeCount: *d.ThisDayCanUseFreeCount, participated: *p.VisitorParticipateNum, serverTime: a.ServiceTimestamp, startTime: a.StartTime, endTime: a.EndTime, awardTime: a.AwardTimestamp, vip: *d.VIP, used: *d.UseEveryDayFreeCount, status: *p.Status}, nil
}

func (s koiWishState) samePeriodAndDay(before koiWishState) bool {
	return s.prizeTableID == before.prizeTableID && s.serverTime >= before.serverTime && time.UnixMilli(s.serverTime).In(neteaseLocation).Format("2006-01-02") == time.UnixMilli(before.serverTime).In(neteaseLocation).Format("2006-01-02")
}

func (s koiWishState) freeConsumptionSince(before koiWishState) int64 {
	if !s.samePeriodAndDay(before) || !s.used || s.freeCount >= before.freeCount || s.participated <= before.participated {
		return 0
	}
	consumed := before.freeCount - s.freeCount
	if s.participated-before.participated != consumed {
		return 0
	}
	return consumed
}

func koiWishReceiptCount(response *eapi.VipLotteryFreeClickResp, available int64) int64 {
	if response == nil || response.Code != 200 || response.Data == nil {
		return 0
	}
	d := response.Data
	if d.Success == nil || !*d.Success || d.Privilege == nil || !*d.Privilege || d.Count == nil || *d.Count <= 0 || *d.Count > available {
		return 0
	}
	return *d.Count
}

func (c *SignIn) reportKoiWish(account, outcome, message string) {
	if c.cmd != nil {
		c.cmd.Printf("    [锦鲤免费许愿] %s\n", message)
	}
	switch outcome {
	case "failure":
		c.root.ReportFailureMsg(account, koiWishTask, message)
	case "skip":
		c.root.ReportSkip(account, koiWishTask, message)
	case "unconfirmed":
		c.root.ReportUnconfirmed(account, koiWishTask, message)
	}
}

func koiWishCountText(value *int64) string {
	if value == nil {
		return "缺失"
	}
	return fmt.Sprint(*value)
}

func koiWishBoolText(value *bool) string {
	if value == nil {
		return "缺失"
	}
	return fmt.Sprint(*value)
}

func (c *SignIn) logKoiWishState(phase string, response *weapi.VipLotteryModuleListResp, requestErr error) {
	if c.cmd == nil {
		return
	}
	var code int
	var activityID int64
	var name string
	var free, participated *int64
	var used *bool
	if response != nil {
		code = response.Code
		if d := response.Data; d != nil {
			free, used = d.ThisDayCanUseFreeCount, d.UseEveryDayFreeCount
			if d.ActivityBaseInfo != nil {
				name = truncateRunes(d.ActivityBaseInfo.PrizeName, 120)
			}
			if d.ActivityProcessInfo != nil {
				activityID, participated = d.ActivityProcessInfo.PrizeTableID, d.ActivityProcessInfo.VisitorParticipateNum
			}
		}
	}
	// 只打印必要诊断字段和错误类型，不输出请求、令牌或原始错误正文。
	c.cmd.Printf("    [锦鲤免费许愿] %s：code=%d，活动名=%q，活动ID=%d，免费次数=%s，参与次数=%s，已用=%s，错误类型=%T\n", phase, code, name, activityID, koiWishCountText(free), koiWishCountText(participated), koiWishBoolText(used), requestErr)
}

func (c *SignIn) logKoiWishReceipt(response *eapi.VipLotteryFreeClickResp, requestErr error) {
	if c.cmd == nil {
		return
	}
	var code int
	var success, privilege *bool
	var count *int64
	if response != nil {
		code = response.Code
		if response.Data != nil {
			success, privilege, count = response.Data.Success, response.Data.Privilege, response.Data.Count
		}
	}
	c.cmd.Printf("    [锦鲤免费许愿] 提交回执：code=%d，success=%s，privilege=%s，count=%s，错误类型=%T\n", code, koiWishBoolText(success), koiWishBoolText(privilege), koiWishCountText(count), requestErr)
}

func (c *SignIn) runKoiWish(ctx context.Context, client koiWishAPI, account string, earningsReport *accountEarningsReport, tokenForAccount func() string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if earningsReport == nil {
		earningsReport = &accountEarningsReport{}
	}
	if earningsReport.koiWishHandled {
		c.reportKoiWish(account, "skip", "同批次已处理锦鲤免费许愿，后续任务组不再查询或提交")
		return nil
	}
	// 快慢队列共享账号报告；包括读取失败、取消和结果不确定都不在本批次重跑。
	earningsReport.koiWishHandled = true
	response, err := client.VipLotteryModuleList(ctx)
	c.logKoiWishState("提交前查询", response, err)
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	if err != nil {
		c.reportKoiWish(account, "failure", "查询官方锦鲤状态失败，本轮未提交")
		return nil
	}
	before, err := koiWishReadState(response)
	if err != nil {
		c.reportKoiWish(account, "failure", err.Error()+"，本轮未提交")
		return nil
	}
	if before.used || before.freeCount == 0 {
		c.reportKoiWish(account, "skip", "今日免费许愿已使用或次数已用完，已跳过")
		return nil
	}
	if !before.vip {
		c.reportKoiWish(account, "skip", "当前账号不具备 VIP 免费许愿资格，已跳过")
		return nil
	}
	if before.status != 0 || before.serverTime < before.startTime || before.serverTime >= before.endTime || before.serverTime >= before.awardTime {
		c.reportKoiWish(account, "skip", "当前活动未开放免费许愿或已截止，已跳过")
		return nil
	}
	token := strings.TrimSpace(tokenForAccount())
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	if token == "" {
		c.reportKoiWish(account, "skip", "需要当前账号配套的移动端 Cookie、UA 和 Token，已跳过")
		return nil
	}
	if c.cmd != nil {
		c.cmd.Printf("    [锦鲤免费许愿] 活动 ID=%d，免费次数=%d，参与次数=%d，准备提交一次\n", before.prizeTableID, before.freeCount, before.participated)
	}
	// 写入至多一次。无论回执是否丢失，后续只查询，不据此自动补交。
	receipt, submitErr := client.VipLotteryFreeClick(ctx, &eapi.VipLotteryFreeClickReq{PrizeTableID: before.prizeTableID, Token: token})
	c.logKoiWishReceipt(receipt, submitErr)
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	postResponse, postErr := client.VipLotteryModuleList(ctx)
	c.logKoiWishState("提交后查询", postResponse, postErr)
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	if postErr != nil {
		c.reportKoiWish(account, "unconfirmed", "提交后状态查询失败，结果未确认，请在 App 核对；本轮不重试")
		return nil
	}
	after, err := koiWishReadState(postResponse)
	if err != nil {
		c.reportKoiWish(account, "unconfirmed", "提交后官方状态不完整，结果未确认，请在 App 核对；本轮不重试")
		return nil
	}
	if !after.samePeriodAndDay(before) {
		c.reportKoiWish(account, "unconfirmed", "提交后活动或服务端日期已变化，结果未确认，请在 App 核对；本轮不重试")
		return nil
	}
	consumed := after.freeConsumptionSince(before)
	if submitErr != nil || receipt == nil || receipt.Code != 200 {
		if consumed > 0 {
			c.reportKoiWish(account, "", fmt.Sprintf("平台状态已完成免费许愿（活动 ID=%d，消耗次数=%d），不能确认归因于本次请求；本轮不重试", before.prizeTableID, consumed))
		} else if submitErr == nil && receipt != nil && receipt.Code != 200 {
			c.reportKoiWish(account, "failure", fmt.Sprintf("官方拒绝免费许愿（code=%d），未核对到免费次数消耗；本轮不重试", receipt.Code))
		} else {
			c.reportKoiWish(account, "unconfirmed", "提交回执缺失且未核对到免费次数消耗，结果未确认，请在 App 核对；本轮不重试")
		}
		return nil
	}
	count := koiWishReceiptCount(receipt, before.freeCount)
	if count == 0 || count != consumed {
		c.reportKoiWish(account, "unconfirmed", "提交回执与官方免费次数、参与次数未一致，结果未确认，请在 App 核对；本轮不重试")
		return nil
	}
	c.reportKoiWish(account, "", fmt.Sprintf("已确认本次免费许愿（活动 ID=%d，次数=%d，当前参与次数=%d）", before.prizeTableID, count, after.participated))
	return nil
}

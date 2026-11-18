package ncmm

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/3899/ncmm/api/eapi"
)

type yunbeiRedPacketAPI interface {
	YunbeiRedPacketInfo(context.Context, *eapi.YunbeiRedPacketInfoReq) (*eapi.YunbeiRedPacketInfoResp, error)
	YunbeiRedPacketPlay(context.Context, *eapi.YunbeiRedPacketPlayReq) (*eapi.YunbeiRedPacketPlayResp, error)
	YunbeiAdRightDirectGain(context.Context, *eapi.YunbeiAdRightDirectGainReq) (*eapi.YunbeiAdRightDirectGainResp, error)
}

func waitYunbeiActivity(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func yunbeiRedPacketCounts(response *eapi.YunbeiRedPacketInfoResp) (int, int, bool, error) {
	if response == nil || response.Code != 200 || response.Data == nil {
		return 0, 0, false, fmt.Errorf("红包雨场次状态缺失")
	}
	d := response.Data
	if d.CurrentActivity == nil || d.HasChance == nil || d.RemainingChance == nil || *d.CurrentActivity < 0 || *d.RemainingChance < 0 {
		return 0, 0, false, fmt.Errorf("红包雨场次或次数字段缺失、无效")
	}
	return *d.CurrentActivity, *d.RemainingChance, *d.HasChance, nil
}

func (c *SignIn) runYunbeiRedPacket(ctx context.Context, client yunbeiRedPacketAPI, account string, wait func(context.Context, time.Duration) error) error {
	const task = "红包雨"
	if err := ctx.Err(); err != nil {
		return err
	}
	response, err := client.YunbeiRedPacketInfo(ctx, &eapi.YunbeiRedPacketInfoReq{UserId: -1})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	code := 0
	if response != nil {
		code = response.Code
	}
	if issue := yunbeiActivityResponse("查询红包雨", code, err, false); issue != nil {
		return c.reportYunbeiActivityError(account, task, issue)
	}
	activity, remaining, available, err := yunbeiRedPacketCounts(response)
	if err != nil {
		return c.reportYunbeiActivityError(account, task, err)
	}
	if !available || remaining == 0 {
		c.reportYunbeiActivity(account, task, "skip", "当前场次没有可用机会，等待下一场")
		return nil
	}
	budget := min(remaining, 6)
	for range budget {
		if err := ctx.Err(); err != nil {
			return err
		}
		play, playErr := client.YunbeiRedPacketPlay(ctx, &eapi.YunbeiRedPacketPlayReq{UserId: -1, ActivityId: activity})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		code = 0
		if play != nil {
			code = play.Code
		}
		if issue := yunbeiActivityResponse("开始本次红包雨", code, playErr, true); issue != nil {
			return c.reportYunbeiActivityError(account, task, issue)
		}
		if strings.TrimSpace(play.Data) == "" {
			return c.reportYunbeiActivityError(account, task, &yunbeiActivityIssue{operation: "红包雨请求凭证缺失", unconfirmed: true})
		}
		duration := time.Duration(8+rand.IntN(3)) * time.Second
		c.reportYunbeiActivity(account, task, "", fmt.Sprintf("场次 %d，剩余 %d 次；等待 %d 秒后提交本次领取", activity, remaining, int(duration.Seconds())))
		if err := wait(ctx, duration); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		receipt, submitErr := client.YunbeiAdRightDirectGain(ctx, &eapi.YunbeiAdRightDirectGainReq{ReqUid: play.Data, UserId: -1, Source: 93, RightType: 6, BizParams: eapi.YunbeiRedPacketBizParams{Rank: 1 + rand.IntN(3)}})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		code = 0
		if receipt != nil {
			code = receipt.Code
		}
		if issue := yunbeiActivityResponse("领取红包雨权益", code, submitErr, true); issue != nil {
			return c.reportYunbeiActivityError(account, task, issue)
		}
		after, queryErr := client.YunbeiRedPacketInfo(ctx, &eapi.YunbeiRedPacketInfoReq{UserId: -1})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		code = 0
		if after != nil {
			code = after.Code
		}
		if issue := yunbeiActivityResponse("核对红包雨次数", code, queryErr, false); issue != nil {
			c.reportYunbeiActivity(account, task, "unconfirmed", "红包雨已提交但官方次数未能核对，本轮不再提交")
			return c.reportYunbeiActivityError(account, task, issue)
		}
		afterActivity, afterRemaining, afterAvailable, stateErr := yunbeiRedPacketCounts(after)
		if queryErr != nil || stateErr != nil || afterActivity != activity || afterRemaining != remaining-1 || receipt.Success == nil || !*receipt.Success {
			c.reportYunbeiActivity(account, task, "unconfirmed", "领取回执与官方场次、次数尚未一致，本轮不再提交，请在 App 核对")
			return nil
		}
		remaining = afterRemaining
		c.reportYunbeiActivity(account, task, "", fmt.Sprintf("已确认一次领取，当前场次剩余 %d 次；实际云贝以账户流水为准", remaining))
		if remaining == 0 || !afterAvailable {
			return nil
		}
	}
	if remaining > 0 {
		c.reportYunbeiActivity(account, task, "skip", "已达到本轮六次执行上限，保留剩余机会")
	}
	return ctx.Err()
}

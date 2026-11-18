package ncmm

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/3899/ncmm/api/eapi"
)

type yunbeiVideoAPI interface {
	YunbeiAdGet(context.Context, *eapi.YunbeiAdGetReq) (*eapi.YunbeiAdGetResp, error)
	YunbeiAdRightsGain(context.Context, *eapi.YunbeiAdRightsGainReq) (*eapi.YunbeiAdRightsGainResp, error)
}

type yunbeiLotteryVideoAPI interface {
	yunbeiVideoAPI
	YunbeiLotteryChanceGet(context.Context, *eapi.YunbeiLotteryChanceGetReq) (*eapi.YunbeiLotteryChanceGetResp, error)
	YunbeiSecKillRecommend(context.Context, *eapi.YunbeiSecKillRecommendReq) (*eapi.YunbeiSecKillRecommendResp, error)
}

type yunbeiVideoStageAPI interface {
	yunbeiVideoAPI
	YunbeiStageInfo(context.Context, *eapi.YunbeiStageInfoReq) (*eapi.YunbeiStageInfoResp, error)
}

func yunbeiVideoParams(source int, general bool) map[string]any {
	params := map[string]any{"source": source, "creativeType": 2, "rightsGainMethod": 4, "extraRightsGainMethod": 0}
	if general {
		params["generalRightsInfo"] = map[string]any{"stayTime": 6, "clickStayTime": 6, "source": source}
	}
	return params
}

// 等待不代表实际观看或获得奖励，仍需核对 gainFlag 和各活动状态。
func (c *SignIn) prepareYunbeiVideo(ctx context.Context, client yunbeiVideoAPI, account, task string, source int, wait func(context.Context, time.Duration) error) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	ad, requestErr := client.YunbeiAdGet(ctx, &eapi.YunbeiAdGetReq{AdExtJson: map[string]any{"source": source}})
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	code := 0
	if ad != nil {
		code = ad.Code
	}
	if issue := yunbeiActivityResponse("获取视频素材", code, requestErr, false); issue != nil {
		return false, issue
	}
	if ad.Ads == nil {
		return false, fmt.Errorf("视频素材状态缺失，未提交权益")
	}
	if len(ad.Ads) == 0 {
		c.reportYunbeiActivity(account, task, "skip", "当前没有可用视频素材，未提交权益")
		return false, nil
	}
	duration := time.Duration(8+rand.IntN(3)) * time.Second
	c.reportYunbeiActivity(account, task, "", fmt.Sprintf("场景 %d，等待 %d 秒后提交本次权益，最终结果以官方回执为准", source, int(duration.Seconds())))
	if wait == nil {
		wait = waitYunbeiActivity
	}
	if err := wait(ctx, duration); err != nil {
		return false, err
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	return true, nil
}

// 每次只提交一次权益；阶段 genTime 由调用方在素材等待完成后生成。
func submitYunbeiVideoRights(ctx context.Context, client yunbeiVideoAPI, token string, params map[string]any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	receipt, submitErr := client.YunbeiAdRightsGain(ctx, &eapi.YunbeiAdRightsGainReq{ReqParam: params, CheckToken: token})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	code := 0
	if receipt != nil {
		code = receipt.Code
	}
	if issue := yunbeiActivityResponse("提交视频权益", code, submitErr, true); issue != nil {
		return issue
	}
	if receipt.Data == nil || receipt.Data.GainFlag == nil || !*receipt.Data.GainFlag {
		return &yunbeiActivityIssue{operation: "视频权益未获得明确发放回执", unconfirmed: true}
	}
	return nil
}

func (c *SignIn) runYunbeiLotteryVideos(ctx context.Context, client yunbeiLotteryVideoAPI, account, token string, wait func(context.Context, time.Duration) error) error {
	const task = "广告增加抽奖次数"
	if err := ctx.Err(); err != nil {
		return err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		c.reportYunbeiActivity(account, task, "skip", "未配置当前账号移动端 Token，未请求活动")
		return nil
	}
	for _, lottery := range yunbeiLotteries {
		if err := ctx.Err(); err != nil {
			return err
		}
		before, queryErr := client.YunbeiLotteryChanceGet(ctx, &eapi.YunbeiLotteryChanceGetReq{PlayId: lottery.id})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		code := 0
		if before != nil {
			code = before.Code
		}
		if issue := yunbeiActivityResponse("查询视频增次前的抽奖机会", code, queryErr, false); issue != nil {
			return c.reportYunbeiActivityError(account, task, issue)
		}
		total, used, remaining, err := yunbeiLotteryChances(before)
		if err != nil {
			return c.reportYunbeiActivityError(account, task, err)
		}
		ready, err := c.prepareYunbeiVideo(ctx, client, account, task, lottery.source, wait)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return c.reportYunbeiActivityError(account, task, err)
		}
		if !ready {
			continue
		}
		if err := submitYunbeiVideoRights(ctx, client, token, yunbeiVideoParams(lottery.source, true)); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return c.reportYunbeiActivityError(account, task, err)
		}
		after, queryErr := client.YunbeiLotteryChanceGet(ctx, &eapi.YunbeiLotteryChanceGetReq{PlayId: lottery.id})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		code = 0
		if after != nil {
			code = after.Code
		}
		if issue := yunbeiActivityResponse("核对视频增次结果", code, queryErr, false); issue != nil {
			return c.reportYunbeiActivityError(account, task, issue)
		}
		afterTotal, afterUsed, afterRemaining, stateErr := yunbeiLotteryChances(after)
		if stateErr != nil || afterTotal <= total || afterRemaining <= remaining || afterUsed != used || afterTotal-total != afterRemaining-remaining {
			c.reportYunbeiActivity(account, task, "unconfirmed", lottery.name+"权益回执与官方次数增加尚未一致，本轮不再提交，请在 App 核对")
			return nil
		}
		c.reportYunbeiActivity(account, task, "", fmt.Sprintf("%s已核对新增 %d 次机会，剩余 %d 次；抽奖由独立开关和当前任务队列控制", lottery.name, afterRemaining-remaining, afterRemaining))
	}
	return c.runYunbeiHomeVideo(ctx, client, account, token, wait)
}

func (c *SignIn) runYunbeiHomeVideo(ctx context.Context, client yunbeiLotteryVideoAPI, account, token string, wait func(context.Context, time.Duration) error) error {
	const task = "首页额外云贝"
	if err := ctx.Err(); err != nil {
		return err
	}
	response, queryErr := client.YunbeiSecKillRecommend(ctx, &eapi.YunbeiSecKillRecommendReq{FromPage: "home"})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	code := 0
	if response != nil {
		code = response.Code
	}
	if issue := yunbeiActivityResponse("查询首页额外权益", code, queryErr, false); issue != nil {
		return c.reportYunbeiActivityError(account, task, issue)
	}
	if response.Data == nil || response.Data.SecKillAdInfo == nil || response.Data.SecKillAdInfo.Limit == nil || response.Data.SecKillAdInfo.Amount == nil || *response.Data.SecKillAdInfo.Amount < 0 {
		return c.reportYunbeiActivityError(account, task, fmt.Errorf("首页额外权益状态缺失或无效"))
	}
	info := response.Data.SecKillAdInfo
	if *info.Limit || *info.Amount == 0 {
		c.reportYunbeiActivity(account, task, "skip", "官方当前没有可领取的首页额外权益")
		return nil
	}
	if info.BaseSource == nil || *info.BaseSource <= 0 {
		return c.reportYunbeiActivityError(account, task, fmt.Errorf("首页额外权益来源缺失，未提交权益"))
	}
	source := *info.BaseSource
	c.reportYunbeiActivity(account, task, "", fmt.Sprintf("官方当前展示额外权益 %d 云贝；不将预告金额记为到账", *info.Amount))
	ready, err := c.prepareYunbeiVideo(ctx, client, account, task, source, wait)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return c.reportYunbeiActivityError(account, task, err)
	}
	if !ready {
		return nil
	}
	if err := submitYunbeiVideoRights(ctx, client, token, yunbeiVideoParams(source, false)); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return c.reportYunbeiActivityError(account, task, err)
	}
	after, queryErr := client.YunbeiSecKillRecommend(ctx, &eapi.YunbeiSecKillRecommendReq{FromPage: "home"})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	code = 0
	if after != nil {
		code = after.Code
	}
	if issue := yunbeiActivityResponse("核对首页额外权益", code, queryErr, false); issue != nil {
		return c.reportYunbeiActivityError(account, task, issue)
	}
	if after.Data == nil || after.Data.SecKillAdInfo == nil || after.Data.SecKillAdInfo.Limit == nil || after.Data.SecKillAdInfo.Amount == nil || *after.Data.SecKillAdInfo.Amount < 0 || after.Data.SecKillAdInfo.BaseSource == nil || *after.Data.SecKillAdInfo.BaseSource != source || (!*after.Data.SecKillAdInfo.Limit && *after.Data.SecKillAdInfo.Amount != 0) {
		c.reportYunbeiActivity(account, task, "unconfirmed", "首页额外权益回执与官方可领状态尚未一致，不再提交，请在 App 核对")
		return nil
	}
	c.reportYunbeiActivity(account, task, "", "已核对本次首页权益领取状态，实际入账金额以账户流水为准")
	return ctx.Err()
}

func yunbeiVideoStageState(response *eapi.YunbeiStageInfoResp) (int, int, int64, error) {
	if response == nil || response.Code != 200 || response.Data == nil {
		return 0, 0, 0, fmt.Errorf("视频阶段状态缺失")
	}
	d := response.Data
	if d.CurrentIndex == nil || d.TotalStage == nil || d.CurrentAmount == nil || *d.CurrentIndex < 0 || *d.TotalStage <= 0 || *d.CurrentIndex > *d.TotalStage || *d.CurrentAmount < 0 {
		return 0, 0, 0, fmt.Errorf("视频阶段字段缺失或无效")
	}
	return *d.CurrentIndex, *d.TotalStage, *d.CurrentAmount, nil
}

func (c *SignIn) runYunbeiVideoStages(ctx context.Context, client yunbeiVideoStageAPI, account, token string, wait func(context.Context, time.Duration) error) error {
	const task = "看视频赚云贝"
	if err := ctx.Err(); err != nil {
		return err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		c.reportYunbeiActivity(account, task, "skip", "未配置当前账号移动端 Token，未请求活动")
		return nil
	}
	read := func() (*eapi.YunbeiStageInfoResp, error) {
		response, err := client.YunbeiStageInfo(ctx, &eapi.YunbeiStageInfoReq{EntranceType: "YUNBEI_CENTER"})
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		code := 0
		if response != nil {
			code = response.Code
		}
		return response, yunbeiActivityResponse("查询视频阶段", code, err, false)
	}
	response, err := read()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return c.reportYunbeiActivityError(account, task, err)
	}
	for range 15 {
		if err := ctx.Err(); err != nil {
			return err
		}
		index, total, amount, err := yunbeiVideoStageState(response)
		if err != nil {
			return c.reportYunbeiActivityError(account, task, err)
		}
		if index >= total || amount == 0 {
			c.reportYunbeiActivity(account, task, "skip", "官方当前没有可执行的视频阶段，保留实际完成进度")
			return nil
		}
		c.reportYunbeiActivity(account, task, "", fmt.Sprintf("准备阶段 %d/%d，官方当前展示权益 %d 云贝", index+1, total, amount))
		ready, err := c.prepareYunbeiVideo(ctx, client, account, task, 58, wait)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return c.reportYunbeiActivityError(account, task, err)
		}
		if !ready {
			return nil
		}
		params := yunbeiVideoParams(58, true)
		params["generalRightsInfo"].(map[string]any)["rightType"] = 6
		ext, err := json.Marshal(map[string]any{"preRights": map[string]any{"genTime": time.Now().UnixMilli(), "popupNum": 1, "resource": map[string]any{"time": 0, "worth": amount}, "rightType": 6, "source": 58}})
		if err != nil {
			return c.reportYunbeiActivityError(account, task, fmt.Errorf("无法构造当前视频阶段参数"))
		}
		params["rightsExtJson"] = string(ext)
		if err := submitYunbeiVideoRights(ctx, client, token, params); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return c.reportYunbeiActivityError(account, task, err)
		}
		response, err = read()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return c.reportYunbeiActivityError(account, task, err)
		}
		afterIndex, afterTotal, _, stateErr := yunbeiVideoStageState(response)
		if stateErr != nil || afterTotal != total || afterIndex <= index {
			c.reportYunbeiActivity(account, task, "unconfirmed", "视频权益回执与官方阶段推进尚未一致，本轮停止，不重复提交")
			return nil
		}
		c.reportYunbeiActivity(account, task, "", fmt.Sprintf("官方阶段已从 %d 推进至 %d；实际收益以账户流水为准", index, afterIndex))
	}
	if index, total, amount, err := yunbeiVideoStageState(response); err == nil && (index >= total || amount == 0) {
		c.reportYunbeiActivity(account, task, "", "本轮已核对当前可执行视频阶段，不将预告金额记为实际入账")
		return ctx.Err()
	}
	c.reportYunbeiActivity(account, task, "skip", "达到本轮十五阶段执行上限，保留剩余阶段供后续调度")
	return ctx.Err()
}

package weapi

import (
	"context"
	"fmt"

	"github.com/3899/ncmm/api"
)

type VipLotteryModuleListResp struct {
	Code int               `json:"code"`
	Data *VipLotteryModule `json:"data"`
}

// VipLotteryModule 只保留当前账号参与免费许愿所需的状态，不收集往期中奖用户资料。
type VipLotteryModule struct {
	VIP                    *bool               `json:"vip"`
	ThisDayCanUseFreeCount *int64              `json:"thisDayCanUseFreeCount"`
	UseEveryDayFreeCount   *bool               `json:"useEveryDayFreeCount"`
	ActivityBaseInfo       *VipLotteryActivity `json:"activityBaseInfo"`
	ActivityProcessInfo    *VipLotteryProcess  `json:"activityProcessInfo"`
}

type VipLotteryActivity struct {
	PrizeName        string `json:"prizeName"`
	ServiceTimestamp int64  `json:"serviceTimestamp"`
	StartTime        int64  `json:"startTime"`
	EndTime          int64  `json:"endTime"`
	AwardTimestamp   int64  `json:"awardTimestamp"`
}

type VipLotteryProcess struct {
	PrizeTableID          int64  `json:"prizeTableId"`
	Status                *int   `json:"status"`
	VisitorParticipateNum *int64 `json:"visitorParticipateNum"`
}

// VipLotteryModuleList 查询锦鲤活动、今日免费次数和本期参与次数，不领取或消耗次数。
func (a *Api) VipLotteryModuleList(ctx context.Context) (*VipLotteryModuleListResp, error) {
	opts := api.NewOptions()
	opts.Sensitive = true
	opts.NoRetry = true
	var response VipLotteryModuleListResp
	_, err := a.client.Request(ctx, "https://music.163.com/weapi/vipnewcenter/app/lottery/module/list", struct{}{}, &response, opts)
	if err != nil {
		return nil, fmt.Errorf("查询锦鲤免费许愿状态失败: %w", err)
	}
	return &response, nil
}

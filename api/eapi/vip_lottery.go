package eapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/3899/ncmm/api"
)

type VipLotteryFreeClickReq struct {
	PrizeTableID int64          `json:"prizeTableId"`
	Token        string         `json:"token"`
	CryptoMode   api.CryptoMode `json:"-"`
}

type VipLotteryFreeClickResp struct {
	Code int                      `json:"code"`
	Data *VipLotteryFreeClickData `json:"data"`
}

type VipLotteryFreeClickData struct {
	Success   *bool  `json:"success"`
	Privilege *bool  `json:"privilege"`
	Count     *int64 `json:"count"`
}

// VipLotteryFreeClick 仅使用今日免费许愿次数。活动 ID 必须来自本次查询，移动端 Token 不写入日志。
func (a *Api) VipLotteryFreeClick(ctx context.Context, req *VipLotteryFreeClickReq) (*VipLotteryFreeClickResp, error) {
	if req == nil || req.PrizeTableID <= 0 || strings.TrimSpace(req.Token) == "" {
		return nil, fmt.Errorf("免费许愿缺少有效活动或移动端 Token")
	}
	if req.CryptoMode != api.CryptoModeEAPI && req.CryptoMode != api.CryptoModeXEAPI {
		return nil, fmt.Errorf("免费许愿需要匹配的移动端协议")
	}
	payload := struct {
		DailySongShareBaseReq
		PrizeTableID int64  `json:"prizeTableId"`
		Token        string `json:"token"`
	}{DailySongShareBaseReq: DailySongShareBaseReq{CryptoMode: req.CryptoMode}, PrizeTableID: req.PrizeTableID, Token: strings.TrimSpace(req.Token)}
	mode := payload.fill(a.client)
	opts := api.NewOptions()
	opts.CryptoMode = mode
	opts.Sensitive = true
	opts.NoRetry = true
	a.applyDailySongShareHeaders(opts, mode)
	opts.SetHeader("X-antiCheatToken", payload.Token)
	var response VipLotteryFreeClickResp
	_, err := a.client.Request(ctx, dailySongShareURL(mode, "vipnewcenter/app/lottery/free/click"), &payload, &response, opts)
	if err != nil {
		return nil, fmt.Errorf("提交锦鲤免费许愿失败: %w", err)
	}
	return &response, nil
}

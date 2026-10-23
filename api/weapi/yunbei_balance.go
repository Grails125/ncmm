package weapi

import (
	"context"
	"fmt"

	"github.com/3899/ncmm/api"
)

// YunBeiAvailableBalance 从云贝用户信息读取可用余额；旧商城余额与当前云贝账户不一致，不能作为回退来源。
func (a *Api) YunBeiAvailableBalance(ctx context.Context) (int64, error) {
	var response struct {
		Code      int64 `json:"code"`
		UserPoint struct {
			Balance *int64 `json:"balance"`
		} `json:"userPoint"`
	}
	opts := api.NewOptions()
	opts.Sensitive = true
	if _, err := a.client.Request(ctx, "https://music.163.com/weapi/v1/user/info", &YunBeiUserInfoReq{}, &response, opts); err != nil {
		return 0, fmt.Errorf("云贝余额读取失败: %w", err)
	}
	if response.Code != 200 {
		return 0, fmt.Errorf("云贝余额读取失败（服务码 %d）", response.Code)
	}
	if response.UserPoint.Balance == nil {
		return 0, fmt.Errorf("云贝余额数据缺失")
	}
	return *response.UserPoint.Balance, nil
}

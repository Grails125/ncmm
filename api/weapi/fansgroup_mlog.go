package weapi

import (
	"context"
	"github.com/3899/ncmm/api"
)

// 协议来源：api-enhanced 19c7dad6/module/mlog_to_video.js。
func (a *Api) FansGroupMlogVideo(ctx context.Context, mlogID string) (string, int, error) {
	var reply struct {
		Code int    `json:"code"`
		Data string `json:"data"`
	}
	opts := api.NewOptions()
	opts.Sensitive = true
	_, err := a.client.Request(ctx, "https://music.163.com/weapi/mlog/video/convert/id", struct {
		MlogID string `json:"mlogId"`
	}{mlogID}, &reply, opts)
	return reply.Data, reply.Code, err
}

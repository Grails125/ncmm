package weapi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/types"
)

// PartnerRecommendResource 同时用于每日任务和推荐列表，保留服务端的独立完成标记。
type PartnerRecommendResource struct {
	Work           PartnerWork             `json:"work"`
	SpecialTag     []string                `json:"specialTag"`
	SongCommonTags interface{}             `json:"songCommonTags"`
	ReceivedScore  int64                   `json:"receivedScore"`
	QualityScore   int64                   `json:"qualityScore"`
	RedHeartSong   bool                    `json:"redHeartSong"`
	Listened       bool                    `json:"listened"`
	CanInteract    bool                    `json:"canInteract"`
	PublishComment bool                    `json:"publishComment"`
	PublishEvent   bool                    `json:"publishEvent"`
	CollectList    bool                    `json:"collectList"`
	TotalTaskNum   int64                   `json:"totalTaskNum"`
	FinishTaskNum  int64                   `json:"finishTaskNum"`
	TaskSource     int64                   `json:"taskSource"`
	RelatedTopics  []PartnerRecommendTopic `json:"relatedTopics"`
}

type PartnerRecommendTopic struct {
	TopicID   json.Number `json:"topicId"`
	TopicName string      `json:"topicName"`
}

type PartnerRecommendResourcesResp struct {
	types.RespCommon[PartnerRecommendResourcesData]
}

type PartnerRecommendResourcesData struct {
	Resources []PartnerRecommendResource `json:"resources"`
	Cursor    string                     `json:"cursor"`
	HasMore   bool                       `json:"hasMore"`
	RcmdScore int64                      `json:"rcmdScore"`
}

// PartnerRecommendResources 按官方任务页的首屏分页参数获取最多 100 首推荐歌曲。
func (a *Api) PartnerRecommendResources(ctx context.Context) (*PartnerRecommendResourcesResp, error) {
	endpoint := "https://interface.music.163.com/weapi/music/partner/rcmd/resource/list"
	csrf, _ := a.client.GetCSRF(endpoint)
	req := struct {
		types.ReqCommon
		Page string `json:"page"`
	}{ReqCommon: types.ReqCommon{CSRFToken: csrf}, Page: `{"size":100,"cursor":""}`}
	opts := api.NewOptions()
	opts.Sensitive = true
	opts.NoRetry = true
	opts.SetHeader("Referer", "https://mp.music.163.com/")
	var reply PartnerRecommendResourcesResp
	if _, err := a.client.Request(ctx, endpoint, &req, &reply, opts); err != nil {
		return nil, fmt.Errorf("获取音乐合伙人推荐列表失败: %w", err)
	}
	return &reply, nil
}

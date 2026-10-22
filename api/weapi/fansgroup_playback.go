package weapi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/3899/ncmm/api"
)

// 字段来自网易云公开网页 core_c7686f506f6f2607bb7f6cea1951f039.js 的 eI0u/Rg8m。
// 专用方法避免改变普通刷歌与音乐合伙人的上报协议。
type FansGroupPlaybackLog struct {
	Action string                `json:"action"`
	JSON   FansGroupPlaybackData `json:"json"`
}

type FansGroupPlaybackData struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"`
	Mainsite    string `json:"mainsite"`
	MainsiteWeb string `json:"mainsiteWeb"`
	Content     string `json:"content"`
	Wifi        *int   `json:"wifi,omitempty"`
	Download    *int   `json:"download,omitempty"`
	Time        *int   `json:"time,omitempty"`
	End         string `json:"end,omitempty"`
	Source      string `json:"source,omitempty"`
	SourceID    string `json:"sourceId,omitempty"`
}

func FansGroupPlaybackLogs(songID int64, source, sourceID string, seconds int) []FansGroupPlaybackLog {
	start := FansGroupPlaybackData{ID: songID, Type: "song", Mainsite: "1", MainsiteWeb: "1", Content: fmt.Sprintf("id=%d", songID)}
	play := start
	zero := 0
	play.Wifi, play.Download, play.Time = &zero, &zero, &seconds
	play.End, play.Source, play.SourceID = "interrupt", source, sourceID
	return []FansGroupPlaybackLog{{Action: "startplay", JSON: start}, {Action: "play", JSON: play}}
}

func (a *Api) FansGroupPlayerURL(ctx context.Context, req *SongPlayerV1Req) (*SongPlayerV1Resp, error) {
	if req == nil {
		return nil, fmt.Errorf("播放地址请求不能为空")
	}
	const endpoint = "https://music.163.com/weapi/song/enhance/player/url/v1"
	request := *req
	request.CSRFToken, _ = a.client.GetCSRF(endpoint)
	var reply SongPlayerV1Resp
	opts := api.NewOptions()
	opts.FansGroupWebSession = true
	opts.Sensitive = true
	opts.NoRetry = true
	_, err := a.client.Request(ctx, endpoint, &request, &reply, opts)
	return &reply, err
}

func (a *Api) FansGroupWebLog(ctx context.Context, entry FansGroupPlaybackLog) (*WebLogResp, error) {
	const endpoint = "https://clientlogusf.music.163.com/weapi/feedback/weblog"
	logs, err := json.Marshal([]FansGroupPlaybackLog{entry})
	if err != nil {
		return nil, err
	}
	csrf, _ := a.client.GetCSRF(endpoint)
	var reply WebLogResp
	opts := api.NewOptions()
	opts.FansGroupWebSession = true
	opts.Sensitive = true
	opts.NoRetry = true
	_, err = a.client.Request(ctx, endpoint, &webLogReq{CsrfToken: csrf, Logs: string(logs)}, &reply, opts)
	return &reply, err
}

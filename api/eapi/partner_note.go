package eapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/3899/ncmm/api"
)

// PartnerNoteTopic 是推荐歌曲关联的话题；未提供时使用合伙人乐迷团话题。
type PartnerNoteTopic struct {
	ID      string
	Name    string
	Type    int
	SubType int
}

// PartnerNotePublishReq 使用当前账号上传的歌曲封面，不复用抓包图片或模板渲染信息。
type PartnerNotePublishReq struct {
	SongID     int64
	UserID     int64
	Title      string
	Content    string
	Pics       string
	Topics     []PartnerNoteTopic
	CheckToken string
	CryptoMode api.CryptoMode
}

type PartnerNotePublishResp struct {
	Code        int             `json:"code"`
	Message     string          `json:"message"`
	Msg         string          `json:"msg"`
	ErrDetail   json.RawMessage `json:"errDetail"`
	ID          int64           `json:"id"`
	NeedPolling bool            `json:"needPolling"`
	Event       struct {
		ID int64 `json:"id"`
	} `json:"event"`
}

// ConfirmedEventID 只返回已完成发布的稳定 ID；轮询中或响应矛盾时不可再次发布。
func (r *PartnerNotePublishResp) ConfirmedEventID() (int64, error) {
	failure := &PartnerPublishError{Phase: "发布回执"}
	if r == nil {
		return 0, failure
	}
	failure.ServiceCode = int64(r.Code)
	if r.Code != 200 {
		return 0, failure
	}
	if r.NeedPolling {
		return 0, failure
	}
	if r.ID < 0 || r.Event.ID < 0 || (r.ID > 0 && r.Event.ID > 0 && r.ID != r.Event.ID) {
		return 0, failure
	}
	id := r.ID
	if id == 0 {
		id = r.Event.ID
	}
	if id == 0 {
		return 0, failure
	}
	return id, nil
}

type partnerNoteActivity struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      int    `json:"type"`
	SubType   int    `json:"subType"`
	Selected  bool   `json:"selected"`
	CanChange bool   `json:"canChange"`
}

func partnerNotePayload(req *PartnerNotePublishReq) (*DailySongSharePublishReq, error) {
	if req == nil || req.SongID <= 0 || req.UserID <= 0 {
		return nil, fmt.Errorf("笔记发布缺少有效的歌曲或账号编号")
	}
	title, content := strings.TrimSpace(req.Title), strings.TrimSpace(req.Content)
	if !utf8.ValidString(title) || title == "" || utf8.RuneCountInString(title) > 100 {
		return nil, fmt.Errorf("笔记标题需要填写 1 至 100 个字符")
	}
	if !utf8.ValidString(content) || content == "" || utf8.RuneCountInString(content) > 500 {
		return nil, fmt.Errorf("笔记内容需要填写 1 至 500 个字符")
	}
	var pictures []eventImgPicInfo
	if len(req.Pics) > 32768 || json.Unmarshal([]byte(req.Pics), &pictures) != nil || len(pictures) != 1 {
		return nil, fmt.Errorf("推荐笔记需要一张已上传的歌曲封面")
	}
	originID, err := strconv.ParseInt(pictures[0].OriginId, 10, 64)
	if err != nil || originID <= 0 || pictures[0].Width <= 0 || pictures[0].Height <= 0 {
		return nil, fmt.Errorf("推荐笔记的封面信息不完整")
	}
	mode := req.CryptoMode
	if mode == "" {
		mode = api.CryptoModeXEAPI
	}
	if mode != api.CryptoModeXEAPI && mode != api.CryptoModeEAPI {
		return nil, fmt.Errorf("推荐笔记的请求协议无效")
	}
	topics := req.Topics
	if len(topics) == 0 {
		topics = []PartnerNoteTopic{{ID: "13827903", Name: "音乐合伙人的乐迷团", Type: 3, SubType: 11}}
	}
	if len(topics) > 10 {
		return nil, fmt.Errorf("推荐笔记的话题数量超过限制")
	}
	activities := make([]partnerNoteActivity, 0, len(topics))
	for _, topic := range topics {
		id, err := strconv.ParseInt(topic.ID, 10, 64)
		if err != nil || id <= 0 || strings.TrimSpace(topic.Name) == "" || (topic.Type != 2 && topic.Type != 3) {
			return nil, fmt.Errorf("推荐笔记的话题信息不完整")
		}
		activities = append(activities, partnerNoteActivity{ID: topic.ID, Name: topic.Name, Type: topic.Type, SubType: topic.SubType, Selected: true})
	}
	activityInfo, err := json.Marshal(activities)
	if err != nil {
		return nil, fmt.Errorf("推荐笔记的话题信息无法编码")
	}
	return &DailySongSharePublishReq{
		Id:                 strconv.FormatInt(req.SongID, 10),
		UID:                strconv.FormatInt(req.UserID, 10),
		Title:              title,
		Msg:                content,
		Type:               "song",
		Pics:               req.Pics,
		ActivityInfoList:   string(activityInfo),
		PubSource:          `{"bizCode":"Music_Partner","entranceCode":"Publisher_Redirect","needReachNotice":true,"tagDto":null}`,
		PrivacySetting:     "0",
		SocialSpaceVisible: 1,
		CheckToken:         strings.TrimSpace(req.CheckToken),
		CryptoMode:         mode,
	}, nil
}

// PartnerNotePublish 使用已确认的合伙人笔记端点和来源，复用现有原生发布模型。
// 请求不自动重发；不执行普通推歌注册、抽奖或自动删除。
func (a *Api) PartnerNotePublish(ctx context.Context, req *PartnerNotePublishReq) (*PartnerNotePublishResp, error) {
	mode := api.CryptoModeXEAPI
	if req != nil && req.CryptoMode != "" {
		mode = req.CryptoMode
	}
	payload, err := partnerNotePayload(req)
	if err != nil {
		return nil, partnerPublishFailure(ctx, mode, "参数校验", true, err)
	}
	if err := partnerPublishPreflight(ctx, a.client, mode); err != nil {
		return nil, err
	}
	mode, err = fillDailySongSharePublishDefaults(a.client, payload)
	if err != nil {
		return nil, partnerPublishFailure(ctx, mode, "请求准备", true, err)
	}
	opts := api.NewOptions()
	opts.CryptoMode = mode
	opts.Sensitive = true
	opts.NoRetry = true
	if !a.partnerPublishHeaders(opts, mode, &payload.Header) {
		return nil, partnerPublishFailure(ctx, mode, "协议配置", true, nil)
	}
	a.fillDailySongShareAntiCheatToken(payload, opts)
	var reply PartnerNotePublishResp
	if _, err := a.client.Request(ctx, dailySongShareURL(mode, "note/share/friends/resource"), payload, &reply, opts); err != nil {
		return nil, partnerPublishFailure(ctx, mode, "请求或响应处理", false, err)
	}
	if reply.Code == 400 {
		if reply.ID == 0 && reply.Event.ID == 0 && !reply.NeedPolling {
			return &reply, partnerPublishRejection(ctx, mode)
		}
		failure := partnerPublishFailure(ctx, mode, "发布回执", false, nil)
		failure.HTTPStatus, failure.ServiceCode = 200, 400
		return &reply, failure
	}
	if reply.Code == 200 {
		if _, err := reply.ConfirmedEventID(); err != nil {
			failure := partnerPublishFailure(ctx, mode, "发布回执", false, err)
			failure.HTTPStatus = 200
			failure.ServiceCode = int64(reply.Code)
			return &reply, failure
		}
	}
	return &reply, nil
}

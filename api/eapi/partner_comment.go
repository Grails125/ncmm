package eapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/3899/ncmm/api"
)

type PartnerCommentReq struct {
	ResourceID int64
	Content    string
	CheckToken string
	CryptoMode api.CryptoMode
}

type PartnerCommentResp struct {
	Code        int64           `json:"code"`
	Message     string          `json:"message"`
	Msg         string          `json:"msg"`
	ErrDetail   json.RawMessage `json:"errDetail"`
	ID          int64           `json:"id"`
	CommentID   int64           `json:"commentId"`
	NeedPolling bool            `json:"needPolling"`
	Comment     struct {
		CommentID int64  `json:"commentId"`
		ThreadID  string `json:"threadId"`
	} `json:"comment"`
	Event struct {
		ID int64 `json:"id"`
	} `json:"event"`
}

type partnerCommentPayload struct {
	ThreadID   string      `json:"threadId"`
	Content    string      `json:"content"`
	Source     string      `json:"source"`
	CheckToken string      `json:"checkToken,omitempty"`
	Header     interface{} `json:"header"`
	DeviceID   string      `json:"deviceId,omitempty"`
	OS         string      `json:"os,omitempty"`
	VerifyID   int         `json:"verifyId,omitempty"`
	ER         bool        `json:"e_r"`
}

func newPartnerCommentPayload(req *PartnerCommentReq) (*partnerCommentPayload, error) {
	if req == nil || req.ResourceID <= 0 {
		return nil, fmt.Errorf("推荐歌曲 ID 无效")
	}
	content := strings.TrimSpace(req.Content)
	if content == "" || utf8.RuneCountInString(content) > 500 {
		return nil, fmt.Errorf("推荐乐评内容须为 1～500 个字符")
	}
	return &partnerCommentPayload{
		ThreadID: "R_SO_4_" + strconv.FormatInt(req.ResourceID, 10),
		Content:  content, Source: `{"type":16}`, CheckToken: strings.TrimSpace(req.CheckToken),
		Header: "{}", ER: true,
	}, nil
}

// PartnerCommentAdd 使用成功抓包确认的歌曲乐评接口，不改变视频笔记评论契约。
// source 来自官方 H5 原生入口；请求体字段在原生加密抓包中尚未逐项恢复。
func (a *Api) PartnerCommentAdd(ctx context.Context, req *PartnerCommentReq) (*PartnerCommentResp, error) {
	mode := api.CryptoModeXEAPI
	if req != nil && req.CryptoMode != "" {
		mode = req.CryptoMode
	}
	payload, err := newPartnerCommentPayload(req)
	if err != nil {
		return nil, partnerPublishFailure(ctx, mode, "参数校验", true, err)
	}
	if err := partnerPublishPreflight(ctx, a.client, mode); err != nil {
		return nil, err
	}
	if mode == api.CryptoModeEAPI {
		fillDailySongShareIOSBase(a.client, &payload.Header, &payload.DeviceID, &payload.OS, &payload.VerifyID)
	}
	opts := api.NewOptions()
	opts.CryptoMode = mode
	opts.Sensitive = true
	opts.NoRetry = true
	if !a.partnerPublishHeaders(opts, mode, &payload.Header) {
		return nil, partnerPublishFailure(ctx, mode, "协议配置", true, nil)
	}
	opts.SetHeader("Referer", "https://mp.music.163.com/")
	if payload.CheckToken != "" {
		opts.SetHeader("X-antiCheatToken", payload.CheckToken)
	}
	if musicU := a.client.MusicU(); musicU != "" {
		opts.SetHeader("x-music-u", musicU)
	}
	var reply PartnerCommentResp
	if _, err := a.client.Request(ctx, dailySongShareURL(mode, "v1/resource/comments/add"), payload, &reply, opts); err != nil {
		var statusErr *api.HTTPStatusError
		if errors.As(err, &statusErr) {
			reply.Code = statusErr.ServiceCode
		}
		return &reply, partnerPublishFailure(ctx, mode, "请求或响应处理", false, err)
	}
	if reply.Code != 200 {
		if reply.Code == 400 {
			if reply.ID == 0 && reply.CommentID == 0 && reply.Comment.CommentID == 0 && reply.Event.ID == 0 && !reply.NeedPolling {
				return &reply, partnerPublishRejection(ctx, mode)
			}
			failure := partnerPublishFailure(ctx, mode, "发布回执", false, nil)
			failure.HTTPStatus, failure.ServiceCode = 200, 400
			return &reply, failure
		}
		// 保留业务响应给调用方识别登录失效、账号受限等状态。
		return &reply, nil
	}
	if reply.Comment.CommentID <= 0 {
		failure := partnerPublishFailure(ctx, mode, "发布回执", false, nil)
		failure.HTTPStatus = 200
		failure.ServiceCode = reply.Code
		return &reply, failure
	}
	return &reply, nil
}

type PartnerCommentDeleteReq struct {
	ResourceID int64
	CommentID  int64
}

type PartnerCommentDeleteResp struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

type partnerCommentDeletePayload struct {
	ThreadID  string `json:"threadId"`
	CommentID int64  `json:"commentId"`
	CSRFToken string `json:"csrf_token"`
}

func newPartnerCommentDeletePayload(req *PartnerCommentDeleteReq) (*partnerCommentDeletePayload, error) {
	if req == nil || req.ResourceID <= 0 || req.CommentID <= 0 {
		return nil, fmt.Errorf("删除乐评需要有效的歌曲和评论 ID")
	}
	return &partnerCommentDeletePayload{ThreadID: "R_SO_4_" + strconv.FormatInt(req.ResourceID, 10), CommentID: req.CommentID}, nil
}

// PartnerCommentDelete 使用官方网页的本人评论删除接口；归属权限由当前账号和服务端校验。
// 调用方必须先核对本地发布回执及保留期限，不删除任意历史评论。
func (a *Api) PartnerCommentDelete(ctx context.Context, req *PartnerCommentDeleteReq) (*PartnerCommentDeleteResp, error) {
	payload, err := newPartnerCommentDeletePayload(req)
	if err != nil {
		return nil, err
	}
	endpoint := "https://music.163.com/weapi/resource/comments/delete"
	payload.CSRFToken, _ = a.client.GetCSRF(endpoint)
	opts := api.NewOptions()
	opts.Sensitive = true
	opts.NoRetry = true
	var reply PartnerCommentDeleteResp
	if _, err := a.client.Request(ctx, endpoint, payload, &reply, opts); err != nil {
		var statusErr *api.HTTPStatusError
		if errors.As(err, &statusErr) {
			reply.Code = statusErr.ServiceCode
		}
		return &reply, fmt.Errorf("删除音乐合伙人乐评失败: %w", err)
	}
	return &reply, nil
}

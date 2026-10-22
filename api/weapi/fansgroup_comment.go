package weapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/3899/ncmm/api"
)

// 协议来自网易云公开 pt_song_index_d0a152c42c4ce7550652d601df8db69c.js。
// checkToken 是网页 WM.getToken 的结果；不同于移动端 X-antiCheatToken。
type ResourceCommentReq struct {
	ThreadID   string `json:"threadId"`
	Content    string `json:"content,omitempty"`
	CommentID  int64  `json:"commentId,omitempty"`
	CheckToken string `json:"checkToken,omitempty"`
	CSRFToken  string `json:"csrf_token"`
}
type ResourceComment struct {
	CommentID int64 `json:"commentId"`
}
type ResourceCommentResp struct {
	Code    int             `json:"code"`
	Comment ResourceComment `json:"comment"`
}

func (a *Api) ResourceCommentAdd(ctx context.Context, req *ResourceCommentReq) (*ResourceCommentResp, error) {
	if req == nil || strings.TrimSpace(req.Content) == "" {
		return nil, fmt.Errorf("评论内容不能为空")
	}
	return a.resourceComment(ctx, "add", &ResourceCommentReq{ThreadID: req.ThreadID, Content: req.Content, CheckToken: req.CheckToken})
}
func (a *Api) ResourceCommentDelete(ctx context.Context, req *ResourceCommentReq) (*ResourceCommentResp, error) {
	if req == nil || req.CommentID <= 0 {
		return nil, fmt.Errorf("评论 ID 无效")
	}
	// 官方网页删除本人评论不请求 Watchman token，也不携带新建评论的短效凭据。
	return a.resourceComment(ctx, "delete", &ResourceCommentReq{ThreadID: req.ThreadID, CommentID: req.CommentID})
}
func (a *Api) resourceComment(ctx context.Context, action string, req *ResourceCommentReq) (*ResourceCommentResp, error) {
	if !strings.HasPrefix(req.ThreadID, "R_VI_62_") || len(req.ThreadID) <= 8 {
		return nil, fmt.Errorf("仅支持明确的视频笔记评论目标")
	}
	endpoint := "https://music.163.com/weapi/resource/comments/" + action
	req.CSRFToken, _ = a.client.GetCSRF(endpoint)
	var reply ResourceCommentResp
	opts := api.NewOptions()
	opts.Sensitive = true
	opts.NoRetry = true
	_, err := a.client.Request(ctx, endpoint, req, &reply, opts)
	return &reply, err
}

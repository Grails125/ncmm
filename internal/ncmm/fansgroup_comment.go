package ncmm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/internal/atomicfile"
	"github.com/3899/ncmm/internal/filelock"
)

type fansGroupCommentAPI interface {
	ResourceCommentAdd(context.Context, *weapi.ResourceCommentReq) (*weapi.ResourceCommentResp, error)
	ResourceCommentDelete(context.Context, *weapi.ResourceCommentReq) (*weapi.ResourceCommentResp, error)
}

type fansGroupCommentClient struct {
	api   *weapi.Api
	token string
}

func (c fansGroupCommentClient) ResourceCommentAdd(ctx context.Context, req *weapi.ResourceCommentReq) (*weapi.ResourceCommentResp, error) {
	if c.token == "" {
		return nil, fmt.Errorf("未配置当前账号的网页评论短效令牌，跳过新建评论")
	}
	req.CheckToken = c.token
	return c.api.ResourceCommentAdd(ctx, req)
}
func (c fansGroupCommentClient) ResourceCommentDelete(ctx context.Context, req *weapi.ResourceCommentReq) (*weapi.ResourceCommentResp, error) {
	return c.api.ResourceCommentDelete(ctx, req)
}

// 每个账号最多一个未完成操作。未知创建结果不会重试，防止服务端已提交但回执丢失后重复评论。
type fansGroupCommentReceipt struct {
	Version   int       `json:"version"`
	UID       int64     `json:"uid"`
	ThreadID  string    `json:"threadId"`
	CommentID int64     `json:"commentId"`
	CreatedAt time.Time `json:"createdAt"`
	Attempts  int       `json:"deleteAttempts"`
	LastCode  int       `json:"lastDeleteCode,omitempty"`
}

func (c *FansGroup) commentReceiptPath(uid int64) string {
	return filepath.Join(c.root.Opts.Home, "fansgroup-comment-cleanup", fmt.Sprintf("%d.json", uid))
}

func saveFansGroupComment(path string, receipt *fansGroupCommentReceipt) error {
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0600)
}

func loadFansGroupComment(path string, uid int64) (*fansGroupCommentReceipt, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("无法读取待清理评论记录")
	}
	var receipt fansGroupCommentReceipt
	if len(data) > 4096 || json.Unmarshal(data, &receipt) != nil || receipt.Version != 1 || receipt.UID != uid || !validFansGroupThread(receipt.ThreadID) || receipt.CommentID < 0 {
		return nil, fmt.Errorf("待清理评论记录损坏或账号不符，停止新评论")
	}
	return &receipt, nil
}

func deleteFansGroupComment(ctx context.Context, path string, receipt *fansGroupCommentReceipt, client fansGroupCommentAPI) error {
	if receipt.CommentID == 0 {
		return fmt.Errorf("上次评论创建结果不明，已保留目标 %s 与时间，请人工核对并补齐 commentId 后重试；不会自动重复发表", receipt.ThreadID)
	}
	// 停止任务时仍给已创建的评论一次有界清理机会；强制结束时由下次登录同账号恢复。
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	response, err := client.ResourceCommentDelete(cleanupCtx, &weapi.ResourceCommentReq{ThreadID: receipt.ThreadID, CommentID: receipt.CommentID})
	receipt.Attempts++
	if response != nil {
		receipt.LastCode = response.Code
	}
	if err != nil || response == nil || response.Code != 200 {
		if saveErr := saveFansGroupComment(path, receipt); saveErr != nil {
			return fmt.Errorf("评论 %d 删除失败且记录更新失败，原记录已保留", receipt.CommentID)
		}
		return fmt.Errorf("评论 %d 删除失败（code=%d），已保留记录，下次同账号执行乐迷团时重试", receipt.CommentID, receipt.LastCode)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("评论 %d 已删除但本地记录未能清除，请核对后清理", receipt.CommentID)
	}
	return nil
}

func recoverFansGroupComment(ctx context.Context, path string, uid int64, client fansGroupCommentAPI) error {
	lock, err := filelock.TryAcquire(path + ".lock")
	if err != nil {
		return fmt.Errorf("该账号正在处理乐迷团评论或清理目录不可用")
	}
	defer lock.Close()
	receipt, err := loadFansGroupComment(path, uid)
	if err != nil || receipt == nil {
		return err
	}
	return deleteFansGroupComment(ctx, path, receipt, client)
}

func runFansGroupComment(ctx context.Context, path string, uid int64, threadID string, client fansGroupCommentAPI, delay time.Duration) error {
	if uid <= 0 || !validFansGroupThread(threadID) {
		return fmt.Errorf("评论账号或目标无效")
	}
	lock, err := filelock.TryAcquire(path + ".lock")
	if err != nil {
		return fmt.Errorf("该账号正在处理乐迷团评论或清理目录不可用")
	}
	defer lock.Close()
	pending, err := loadFansGroupComment(path, uid)
	if err != nil {
		return err
	}
	if pending != nil {
		return fmt.Errorf("存在待清理评论，本次不发表，请先完成恢复清理")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	receipt := &fansGroupCommentReceipt{Version: 1, UID: uid, ThreadID: threadID, CreatedAt: time.Now().UTC()}
	if err := saveFansGroupComment(path, receipt); err != nil {
		return fmt.Errorf("无法持久化清理意图，未发表评论")
	}
	response, err := client.ResourceCommentAdd(ctx, &weapi.ResourceCommentReq{ThreadID: threadID, Content: "支持好音乐！"})
	if err == nil && response != nil && definiteFansGroupCommentRejection(response.Code) {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("评论明确被拒绝（code=%d），但意图记录清理失败，请核对记录", response.Code)
		}
		return fmt.Errorf("评论接口拒绝请求（code=%d），本轮不会重试", response.Code)
	}
	// 官方网页把 407 也显示为“评论成功”；缺少评论 ID 时仍保留未知记录，不能重复发表。
	if err != nil || response == nil || (response.Code != 200 && response.Code != 407) || response.Comment.CommentID <= 0 {
		return fmt.Errorf("评论创建未得到确定回执，已保留记录并停止自动重试，请人工核对评论区")
	}
	receipt.CommentID = response.Comment.CommentID
	if err := saveFansGroupComment(path, receipt); err != nil {
		cleanupErr := deleteFansGroupComment(ctx, path, receipt, client)
		if cleanupErr != nil {
			return fmt.Errorf("评论 %d 回执保存失败：%v", receipt.CommentID, cleanupErr)
		}
		return fmt.Errorf("评论回执保存失败，已立即删除评论")
	}
	waitFansGroup(ctx, delay)
	return deleteFansGroupComment(ctx, path, receipt, client)
}

func definiteFansGroupCommentRejection(code int) bool {
	// 已知明确拒绝才可释放创建意图；内部异常等不确定结果一律保留。
	switch code {
	case 250, 315, 401, 403, 404, 405, 429, 512:
		return true
	}
	return false
}

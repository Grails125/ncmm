package ncmm

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/3899/ncmm/api/eapi"
)

func (c *SignIn) followYunbeiArtist(ctx context.Context, request *eapi.Api, artist eapi.ArtistHotRespArtist, delay time.Duration) {
	if ctx.Err() != nil || artist.Id <= 0 || artist.Followed == nil || *artist.Followed {
		c.cmd.Println("  ℹ️ 未满足安全关注条件，已跳过关注歌手任务")
		return
	}
	artistID := strconv.FormatInt(artist.Id, 10)
	reply, err := request.ArtistSub(ctx, &eapi.ArtistSubReq{ArtistId: artistID})
	if err != nil || reply == nil {
		c.cmd.Printf("  ❌ 关注歌手 [%s] 失败: %v\n", artist.Name, err)
		return
	}
	if reply.Code != 200 {
		c.cmd.Printf("  ❌ 关注歌手 [%s] 失败: Code=%d\n", artist.Name, reply.Code)
		return
	}
	c.cmd.Printf("  ⏳ 已关注歌手 [%s]，等待 %d 秒后清理本次新增关注...\n", artist.Name, int(delay.Seconds()))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		c.cmd.Println("  ℹ️ 关注歌手任务已取消，正在清理本次新增关注")
	case <-timer.C:
	}

	// 仅补偿本次明确新增成功的关注；任务取消后也保留有界清理时间。
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	unsub, err := request.ArtistUnsub(cleanupCtx, &eapi.ArtistUnsubReq{ArtistIds: fmt.Sprintf("[%s]", artistID)})
	if err != nil || unsub == nil {
		c.cmd.Printf("  ❌ 清理本次新增关注 [%s] 失败，请在 App 检查: %v\n", artist.Name, err)
		return
	}
	if unsub.Code != 200 {
		c.cmd.Printf("  ❌ 清理本次新增关注 [%s] 失败，请在 App 检查: Code=%d\n", artist.Name, unsub.Code)
		return
	}
	if ctx.Err() != nil {
		c.cmd.Printf("  ℹ️ 已清理本次新增关注 [%s]，任务已取消\n", artist.Name)
		return
	}
	c.cmd.Printf("  ✅ 关注歌手 [%s] 操作及清理已完成，奖励以云贝任务状态为准\n", artist.Name)
}

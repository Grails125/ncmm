package ncmm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/pkg/log"
)

// 访问打点只是打开音乐人中心；同一签到任务变为 100 才能确认签到与领奖完成。
func (c *Musician) doSignPhase(ctx context.Context, mctx *musicianContext, cookieFile string) error {
	c.cmd.Println("  👉 [第一阶段] 开始执行音乐人日常任务 (日常签到与云豆领奖)...")
	before, beforeErr := c.fetchMusicianRewardTasks(ctx, mctx)
	c.cmd.Println("    👉 音乐人任务列表（执行前）...")
	c.printMusicianTaskList(before)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if beforeErr != nil {
		c.cmd.Println("    ⚠️ 执行前任务读取不完整，后续以重新查询的官方状态确认")
	}
	var original *eapi.MusicianMissionTask
	for i := range before {
		if isMusicianDailySignTask(before[i]) {
			original = &before[i]
			break
		}
	}
	if original == nil || original.Status != 100 {
		c.cmd.Println("    👉 提交音乐人中心访问打点，随后检查签到任务...")
		access, err := mctx.eapiCli.MusicianSign(ctx, &eapi.MusicianSignReq{})
		if err != nil {
			c.printMusicianRequestFailure("音乐人中心访问", err)
		} else if access != nil {
			c.cmd.Printf("    ℹ️ 音乐人中心访问回执: code=%d，结果=%t；此回执不代表签到完成\n", access.Code, access.Data)
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var after []eapi.MusicianMissionTask
	var sign *eapi.MusicianMissionTask
	for attempt := 0; attempt < 3; attempt++ {
		var err error
		after, err = c.fetchMusicianRewardTasks(ctx, mctx)
		if err != nil {
			return fmt.Errorf("音乐人签到状态未确认：%w", err)
		}
		sign = musicianSignAfterAccess(after, original)
		if sign != nil && sign.UserMissionId > 0 {
			break
		}
		if attempt < 2 {
			c.cmd.Println("    ℹ️ 签到任务尚未分配或状态未同步，仅等待后复查，不重复打点")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(750 * time.Millisecond):
			}
		}
	}
	if sign == nil || sign.UserMissionId <= 0 {
		c.cmd.Println("    👉 签到复查任务（仅显示任务标识与状态）...")
		c.printMusicianTaskList(after)
		return fmt.Errorf("音乐人签到状态未确认：未取得同一周期的音乐人中心签到任务")
	}
	if original != nil && original.UserMissionId <= 0 {
		c.cmd.Printf("    ℹ️ 签到任务已分配：missionId=%d，userMissionId=%d，period=%d\n", sign.MissionId, sign.UserMissionId, sign.Period)
	}
	attempted := c.claimMusicianRewards(ctx, mctx, after)
	final := after
	if len(attempted) > 0 {
		var err error
		final, err = c.fetchMusicianRewardTasks(ctx, mctx)
		if err != nil {
			return fmt.Errorf("云豆领奖结果未确认，仅复查任务状态，不自动重发：%w", err)
		}
	}
	c.cmd.Println("    👉 音乐人任务列表（执行后）...")
	c.printMusicianTaskList(final)
	var failures []error
	for _, task := range attempted {
		confirmed := matchingMusicianTask(final, task)
		if confirmed == nil || confirmed.Status != 100 {
			failures = append(failures, fmt.Errorf("[%s] 云豆领奖结果未确认，本轮不重复提交", musicianTaskDisplayName(task)))
			continue
		}
		c.saveMusicianRewardClaimCache(ctx, mctx, cookieFile, *confirmed)
		c.cmd.Printf("    🎉 [%s] 云豆奖励已由官方状态 100 确认领取\n", musicianTaskDisplayName(*confirmed))
	}
	confirmedSign := matchingMusicianTask(final, *sign)
	if confirmedSign == nil || confirmedSign.Status != 100 {
		failures = append(failures, fmt.Errorf("音乐人日常签到未确认完成，需同一任务状态为 100（已领取）"))
	} else {
		c.cmd.Println("    ✅ 音乐人日常签到成功（官方任务状态：100 已领取）")
	}
	if ctx.Err() != nil {
		failures = append(failures, ctx.Err())
	}
	return errors.Join(failures...)
}

// 首次签到前 userMissionId 可以为 0；仅接受同一任务和周期唯一的新分配记录。
// 已分配的 ID 不允许替换，领奖后的确认仍必须严格匹配该 ID。
func musicianSignAfterAccess(tasks []eapi.MusicianMissionTask, original *eapi.MusicianMissionTask) *eapi.MusicianMissionTask {
	var found *eapi.MusicianMissionTask
	for i := range tasks {
		task := &tasks[i]
		if !isMusicianDailySignTask(*task) || task.UserMissionId <= 0 {
			continue
		}
		if original != nil {
			if task.Period != original.Period || task.MissionId != original.MissionId || original.MissionId <= 0 {
				continue
			}
			if original.UserMissionId > 0 && task.UserMissionId != original.UserMissionId {
				continue
			}
		}
		if found != nil {
			return nil
		}
		found = task
	}
	return found
}

func isMusicianDailySignTask(task eapi.MusicianMissionTask) bool {
	if task.ActionType != 102 {
		return false
	}
	for _, name := range []string{task.Description, task.Name, task.Title} {
		switch strings.TrimSpace(name) {
		case "音乐人中心签到", "音乐人日常签到", "每日签到":
			return true
		}
	}
	return false
}

func matchingMusicianTask(tasks []eapi.MusicianMissionTask, expected eapi.MusicianMissionTask) *eapi.MusicianMissionTask {
	for i := range tasks {
		task := &tasks[i]
		if task.UserMissionId != expected.UserMissionId || task.Period != expected.Period || task.MissionId != expected.MissionId {
			continue
		}
		if isMusicianDailySignTask(expected) && !isMusicianDailySignTask(*task) {
			continue
		}
		return task
	}
	return nil
}

// 每任务每轮只提交一次；无论请求回执如何，只有随后查询到 100 才记录成功。
func (c *Musician) claimMusicianRewards(ctx context.Context, mctx *musicianContext, tasks []eapi.MusicianMissionTask) []eapi.MusicianMissionTask {
	var attempted []eapi.MusicianMissionTask
	seen := make(map[string]bool)
	for _, task := range tasks {
		if ctx.Err() != nil {
			break
		}
		if !isMusicianRewardClaimable(task) {
			continue
		}
		key := fmt.Sprintf("%d:%d", task.UserMissionId, task.Period)
		if seen[key] {
			continue
		}
		seen[key] = true
		attempted = append(attempted, task)
		// 平台状态 20 优先于旧版错误写入的本地成功缓存。
		reward, err := mctx.eapiCli.MusicianRewardObtain(ctx, &eapi.MusicianRewardObtainReq{UserMissionId: task.UserMissionId, Period: task.Period})
		if err != nil {
			c.printMusicianRequestFailure("领取 ["+musicianTaskDisplayName(task)+"] 云豆奖励", err)
		} else if reward != nil {
			c.cmd.Printf("    ℹ️ [%s] 云豆领奖回执: code=%d；等待任务状态复核，不自动重发\n", musicianTaskDisplayName(task), reward.Code)
		}
	}
	return attempted
}

func (c *Musician) printMusicianRequestFailure(operation string, err error) {
	var status *api.HTTPStatusError
	if errors.As(err, &status) {
		c.cmd.Printf("    ⚠️ %s请求未确认: HTTP=%d，服务码=%d；将复查任务状态\n", operation, status.StatusCode, status.ServiceCode)
		return
	}
	c.cmd.Printf("    ⚠️ %s请求未确认（错误类型 %T）；将复查任务状态\n", operation, err)
}

func (c *Musician) saveMusicianRewardClaimCache(ctx context.Context, mctx *musicianContext, cookieFile string, task eapi.MusicianMissionTask) {
	if mctx.db == nil || task.UserMissionId <= 0 || task.Status != 100 {
		return
	}
	if err := mctx.db.Set(ctx, musicianRewardClaimCacheKey(cookieFile, task.UserMissionId, task.Period), "1", 45*24*time.Hour); err != nil {
		log.Warn("[musician] 写入云豆领奖缓存失败（错误类型 %T）", err)
	}
}

func (c *Musician) fetchMusicianRewardTasks(ctx context.Context, mctx *musicianContext) ([]eapi.MusicianMissionTask, error) {
	var tasks []eapi.MusicianMissionTask
	var failures []error
	var readFailures []error
	successfulSources := 0
	seen := make(map[string]eapi.MusicianMissionTask)
	for _, query := range []struct {
		name  string
		stage bool
		req   *eapi.MusicianMissionListReq
	}{
		{"移动端周期任务", false, &eapi.MusicianMissionListReq{Platform: 300, Tag: 101}},
		{"兼容周期任务", false, &eapi.MusicianMissionListReq{Platform: 200, ActionType: 102}},
		{"阶段任务", true, &eapi.MusicianMissionListReq{Platform: 300, Tag: 303}},
	} {
		if ctx.Err() != nil {
			return tasks, errors.Join(append(failures, ctx.Err())...)
		}
		var reply *eapi.MusicianMissionListResp
		var err error
		if query.stage {
			reply, err = mctx.eapiCli.MusicianMissionStageList(ctx, query.req)
		} else {
			reply, err = mctx.eapiCli.MusicianMissionCycleList(ctx, query.req)
		}
		if err != nil {
			c.printMusicianRequestFailure("读取音乐人"+query.name, err)
			readFailures = append(readFailures, fmt.Errorf("音乐人%s读取失败", query.name))
			continue
		}
		if reply == nil || reply.Code != 200 {
			code := int64(0)
			if reply != nil {
				code = reply.Code
			}
			c.cmd.Printf("    ⚠️ 音乐人%s读取未确认: code=%d\n", query.name, code)
			readFailures = append(readFailures, fmt.Errorf("音乐人%s读取未确认（服务码 %d）", query.name, code))
			continue
		}
		successfulSources++
		for _, task := range reply.Data.List {
			key := fmt.Sprintf("%d:%d", task.UserMissionId, task.Period)
			if task.UserMissionId <= 0 {
				key = fmt.Sprintf("unassigned:%d:%d", task.MissionId, task.Period)
			}
			if previous, exists := seen[key]; exists {
				if previous.Status != task.Status || previous.MissionId != task.MissionId || isMusicianDailySignTask(previous) != isMusicianDailySignTask(task) {
					failures = append(failures, fmt.Errorf("[%s] 的多个任务来源状态不一致，请稍后复查", musicianTaskDisplayName(task)))
				}
				continue
			}
			seen[key] = task
			tasks = append(tasks, task)
		}
	}
	if successfulSources == 0 {
		failures = append(failures, readFailures...)
	}
	if len(tasks) == 0 {
		failures = append(failures, fmt.Errorf("音乐人任务列表为空，无法确认签到状态"))
	}
	return tasks, errors.Join(failures...)
}

func musicianTaskDisplayName(task eapi.MusicianMissionTask) string {
	for _, value := range []string{task.Description, task.Name, task.Title} {
		if name := musicPartnerSafeText(strings.TrimSpace(value)); name != "" {
			return name
		}
	}
	if task.MissionId > 0 {
		return fmt.Sprintf("mission-%d", task.MissionId)
	}
	return "未知任务"
}

func (c *Musician) printMusicianTaskList(tasks []eapi.MusicianMissionTask) {
	if len(tasks) == 0 {
		c.cmd.Println("    ℹ️ 暂无可确认的音乐人周期/阶段任务")
		return
	}
	for _, task := range tasks {
		c.cmd.Printf("    - 任务: %-20s | %s | 进度: %d/%d | 云豆: %s | missionId=%d userMissionId=%d period=%d\n", musicianTaskDisplayName(task), musicianTaskStatusText(task), task.ProgressRate, task.TargetCount, musicianTaskRewardWorth(task), task.MissionId, task.UserMissionId, task.Period)
	}
}

func musicianTaskStatusText(task eapi.MusicianMissionTask) string {
	switch task.Status {
	case 0:
		return "未完成（0）"
	case 10:
		return "进行中（10）"
	case 20:
		return "达标待领奖（20）"
	case 100:
		return "已领取（100）"
	default:
		return fmt.Sprintf("未知状态（%d）", task.Status)
	}
}

func musicianTaskRewardWorth(task eapi.MusicianMissionTask) string {
	return musicPartnerSafeText(strings.TrimSpace(task.RewardWorth))
}

func isMusicianRewardClaimable(task eapi.MusicianMissionTask) bool {
	return task.UserMissionId > 0 && task.Status == 20 && len(task.UserStageTargetList) == 0
}

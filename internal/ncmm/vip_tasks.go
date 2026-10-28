// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

package ncmm

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/types"
	"github.com/3899/ncmm/api/weapi"
)

type vipSignState struct {
	Today    bool
	Signed   bool
	RecordId int64
	Time     int64
	TimeStr  string
	Score    int64
}

type vipGrowthState struct {
	GrowthPoint         int64
	TodayScore          int64
	TodayScoreKnown     bool
	MonthTaskTotalScore int64
	CurrentDay          string
	LatestVipStatus     int64
}

type vipMonthPrizeState struct {
	MonthCheckInTotalDay int64
	TodayDailyGrowth     int64
}

func newVipTaskListReq(deviceId string) *eapi.VipTaskListReq {
	return &eapi.VipTaskListReq{
		DeviceId: deviceId,
		OS:       "iOS",
		VerifyId: 1,
		Header:   struct{}{},
		IsNew:    1,
		ER:       true,
	}
}

func newVipSignInfoReq(deviceId string) *eapi.VipSignInfoReq {
	return &eapi.VipSignInfoReq{
		DeviceId: deviceId,
		OS:       "iOS",
		VerifyId: 1,
		Header:   struct{}{},
		ER:       true,
	}
}

func newVipGrowPointReq(deviceId string) *eapi.VipGrowPointReq {
	return &eapi.VipGrowPointReq{
		DeviceId: deviceId,
		OS:       "iOS",
		VerifyId: 1,
		Header:   struct{}{},
		ER:       true,
	}
}

func newVipCommonReq(deviceId string) *eapi.VipCommonReq {
	return &eapi.VipCommonReq{
		DeviceId: deviceId,
		OS:       "iOS",
		VerifyId: 1,
		Header:   struct{}{},
		ER:       true,
	}
}

func isVipSignTask(task eapi.VipTaskListData) bool {
	return task.MissionId == -500 ||
		strings.Contains(task.MainTitle, "乐签") ||
		(strings.Contains(task.MainTitle, "黑胶") && strings.Contains(task.MainTitle, "签"))
}

func isVipSignTaskDone(task eapi.VipTaskListData) bool {
	return task.Status == 100
}

func isVipAppListenTask(task eapi.VipTaskListData) bool {
	return task.MissionCode == "MRGSSVIPGQ" || strings.Contains(task.MainTitle, "听3首VIP") || strings.Contains(task.MainTitle, "听3首会员")
}

func isVipAppStartupTask(task eapi.VipTaskListData) bool {
	return task.MissionCode == "SZKJQDT" || task.MainTitle == "设置开机启动图"
}

func getVipSignState(ctx context.Context, request *eapi.Api, deviceId string) (vipSignState, error) {
	reply, err := request.VipSignInfo(ctx, newVipSignInfoReq(deviceId))
	if err != nil {
		return vipSignState{}, err
	}
	if reply == nil || reply.Code != 200 {
		return vipSignState{}, fmt.Errorf("乐签记录查询未成功")
	}
	for _, info := range reply.Data {
		if !info.Today {
			continue
		}
		return vipSignState{
			Today:    true,
			Signed:   info.RecordId > 0 || info.Time > 0,
			RecordId: info.RecordId,
			Time:     info.Time,
			TimeStr:  info.TimeStr,
			Score:    info.Score,
		}, nil
	}
	return vipSignState{}, nil
}

func getVipGrowthState(ctx context.Context, request *eapi.Api, deviceId string) (vipGrowthState, error) {
	reply, err := request.VipGrowPoint(ctx, newVipGrowPointReq(deviceId))
	if err != nil {
		return vipGrowthState{}, err
	}
	if reply == nil || reply.Code != 200 {
		return vipGrowthState{}, fmt.Errorf("成长值查询未成功")
	}

	state := vipGrowthState{
		GrowthPoint:     reply.Data.UserLevel.GrowthPoint,
		LatestVipStatus: reply.Data.UserLevel.LatestVipStatus,
	}
	now := time.Now().In(neteaseLocation)
	if todayScore, monthScore, currentDay, ok := parseVipGrowthExt(reply.Data.UserLevel.ExtJson); ok && vipGrowthDayMatches(currentDay, now) {
		state.TodayScore = todayScore
		state.TodayScoreKnown = true
		state.MonthTaskTotalScore = monthScore
		state.CurrentDay = currentDay
		return state, nil
	}

	// 旧日期、缺失日期或损坏的累计值不能当作今日收益；回退到带时间的明细。
	var total, previousTime int64
	for page := int64(0); page < 5; page++ {
		details, err := request.VipGrowthDetails(ctx, &eapi.VipGrowthDetailsReq{
			EApiReqCommon: types.EApiReqCommon{DeviceId: deviceId, OS: "iOS", VerifyId: 1, Header: struct{}{}, ER: true},
			Limit:         20, Offset: page * 20,
		})
		if err != nil || details == nil || details.Code != 200 {
			return state, nil
		}
		crossedDay := false
		for _, detail := range details.Data.Details {
			// 明细按时间倒序返回；出现无效时间或乱序时，不把部分明细声明为完整累计。
			if detail.Time <= 0 || (previousTime > 0 && detail.Time > previousTime) {
				return state, nil
			}
			previousTime = detail.Time
			date := time.UnixMilli(detail.Time).In(neteaseLocation).Format("2006-01-02")
			if date < now.Format("2006-01-02") {
				crossedDay = true
			}
		}
		total += vipGrowthOnDate(details.Data.Details, now.Format("2006-01-02"))
		if crossedDay || !details.Data.HasMore {
			state.TodayScore = total
			state.TodayScoreKnown = true
			return state, nil
		}
		if len(details.Data.Details) == 0 {
			return state, nil
		}
	}
	// 查询有界；五页仍未覆盖完整当天时保留未知。
	return state, nil
}

func vipGrowthDayMatches(day string, now time.Time) bool {
	day = strings.TrimSpace(day)
	if day == now.Format("2006-01-02") || day == now.Format("20060102") {
		return true
	}
	// 无补零的 YYYYMD 格式可能将 1 月 11 日与 11 月 1 日混淆，仅接受唯一有效拆分。
	if len(day) < 6 || len(day) > 8 || !strings.HasPrefix(day, fmt.Sprint(now.Year())) {
		return false
	}
	matches := 0
	var matched time.Time
	for month := 1; month <= 12; month++ {
		for date := 1; date <= 31; date++ {
			candidate := time.Date(now.Year(), time.Month(month), date, 0, 0, 0, 0, neteaseLocation)
			if int(candidate.Month()) == month && candidate.Day() == date && fmt.Sprintf("%d%d%d", now.Year(), month, date) == day {
				matches++
				matched = candidate
			}
		}
	}
	return matches == 1 && matched.Month() == now.Month() && matched.Day() == now.Day()
}

func parseVipGrowthExt(raw string) (todayScore int64, monthScore int64, currentDay string, ok bool) {
	if raw == "" {
		return 0, 0, "", false
	}
	var ext struct {
		TodayScore          *int64 `json:"todayScore"`
		MonthTaskTotalScore int64  `json:"monthTaskTotalScore"`
		CurrentDay          string `json:"currentDay"`
	}
	if err := json.Unmarshal([]byte(raw), &ext); err != nil || ext.TodayScore == nil {
		return 0, 0, "", false
	}
	return *ext.TodayScore, ext.MonthTaskTotalScore, ext.CurrentDay, true
}

func vipGrowthOnDate(details []eapi.VipGrowthDetail, date string) int64 {
	var total int64
	for _, detail := range details {
		if detail.Time <= 0 {
			continue
		}
		if time.UnixMilli(detail.Time).In(neteaseLocation).Format("2006-01-02") == date {
			total += detail.GrowthPoint
		}
	}
	return total
}

func getVipMonthPrizeState(ctx context.Context, request *eapi.Api, deviceId string) (vipMonthPrizeState, error) {
	reply, err := request.VipMonthPrizeList(ctx, newVipCommonReq(deviceId))
	if err != nil {
		return vipMonthPrizeState{}, err
	}
	if reply == nil || reply.Code != 200 {
		return vipMonthPrizeState{}, fmt.Errorf("月签进度查询未成功")
	}
	return vipMonthPrizeState{
		MonthCheckInTotalDay: reply.Data.MonthCheckInTotalDay,
		TodayDailyGrowth:     reply.Data.TodayDailyGrowth,
	}, nil
}

// getVipSignCompleted 综合判定乐签是否已完成。
// taskListData 为已获取的任务列表，传 nil 时内部自行获取（避免重复请求）。
// 优先级：月签进度（更准确） > sign/info 记录（兜底）。
func getVipSignCompleted(ctx context.Context, request *eapi.Api, deviceId string, taskListData []eapi.VipTaskListData) (bool, string) {
	// 1. 主判定：任务列表 + 月签进度
	var taskDone bool
	if taskListData != nil {
		for _, task := range taskListData {
			if !isVipSignTask(task) {
				continue
			}
			taskDone = isVipSignTaskDone(task)
			break
		}
	} else {
		taskList, taskErr := request.VipTaskList(ctx, newVipTaskListReq(deviceId))
		if taskErr == nil && taskList != nil && taskList.Code == 200 {
			for _, task := range taskList.Data {
				if !isVipSignTask(task) {
					continue
				}
				taskDone = isVipSignTaskDone(task)
				break
			}
		}
	}
	if taskDone {
		month, monthErr := getVipMonthPrizeState(ctx, request, deviceId)
		if monthErr == nil && month.TodayDailyGrowth > 0 && month.MonthCheckInTotalDay > 0 {
			return true, fmt.Sprintf("任务中心已打卡，月签进度=%d天，今日成长值+%d", month.MonthCheckInTotalDay, month.TodayDailyGrowth)
		}
	}

	// 2. 兜底：sign/info 记录
	if state, err := getVipSignState(ctx, request, deviceId); err == nil && state.Signed {
		return true, fmt.Sprintf("乐签记录已确认，今日成长值+%d", state.Score)
	}

	return false, ""
}

func (c *SignIn) executeVipSign(ctx context.Context, request *eapi.Api, deviceId string) bool {
	c.cmd.Println("  👉 调用黑胶乐签接口...")
	resp, err := request.VipTaskSign(ctx, &eapi.VipTaskSignReq{
		Header: struct{}{},
		IsNew:  "1",
		ER:     true,
	})
	if err != nil {
		c.cmd.Printf("  ⚠️ 黑胶 VIP 乐签请求未获确定回执（错误类型 %T），只查询状态，不自动重发\n", err)
	} else if resp != nil {
		c.cmd.Printf("  👉 黑胶 VIP 乐签回执: code=%d，accepted=%t；正在核对官方签到状态\n", resp.Code, resp.Code == 200 && resp.Data)
	}
	if confirmed, reason := getVipSignCompleted(ctx, request, deviceId, nil); confirmed {
		c.cmd.Printf("  ✅ 黑胶 VIP 乐签成功（%s）\n", reason)
		return true
	}
	c.cmd.Println("  ⚠️ 黑胶 VIP 乐签未获官方状态确认，本轮不会自动重复提交")
	return false
}

func vipManualTaskReason(task eapi.VipTaskListData) string {
	if isVipAppStartupTask(task) || isVipAppListenTask(task) {
		return "需 App 完成"
	}
	if isVipSignTask(task) || task.MissionCode == "HXSSG" || strings.Contains(task.MainTitle, "红心3首") || strings.Contains(task.MainTitle, "调音") || strings.Contains(task.MainTitle, "云贝") || strings.Contains(task.MainTitle, "分享") || task.MissionCode == "FLQ" || strings.Contains(task.MainTitle, "领福利") {
		return ""
	}
	return "未支持自动执行，请手动完成"
}

func vipTaskExecutionKey(task eapi.VipTaskListData) string {
	return fmt.Sprintf("%d/%s/%s", task.MissionId, task.MissionCode, task.MainTitle)
}

func (c *SignIn) executeSingleVipTask(ctx context.Context, cli *api.Client, request *weapi.Api, eapiRequest *eapi.Api, v eapi.VipTaskListData, deviceId string, userLevel int64, likedSongIds *[]string) {
	if isVipAppStartupTask(v) {
		c.cmd.Println("  ℹ️ [设置开机启动图] 请在网易云音乐 App 内完成启动图设置，已跳过自动执行。")
		return
	}
	if isVipAppListenTask(v) {
		c.cmd.Println("  ℹ️ [每日听3首VIP歌曲] 请在网易云音乐 App 内完成，已跳过自动播放。")
		return
	}

	if v.MissionCode == "HXSSG" || strings.Contains(v.MainTitle, "红心3首") {
		c.cmd.Println("  👉 开始执行 [红心3首VIP单曲]...")
		ids := c.doVipSongLike(ctx, request, eapiRequest)
		if len(ids) > 0 && likedSongIds != nil {
			*likedSongIds = append(*likedSongIds, ids...)
		}
	}

	if strings.Contains(v.MainTitle, "调音") {
		c.cmd.Println("  👉 开始执行 [查看AI调音大师]...")
		c.doVipSimulateBrowse(ctx, cli, request, v.JumpUrl, 16, 20, "查看AI调音大师")
	}

	if strings.Contains(v.MainTitle, "云贝") {
		c.cmd.Println("  👉 开始执行 [浏览云贝中心]...")
		c.doVipSimulateBrowse(ctx, cli, request, v.JumpUrl, 16, 20, "浏览云贝中心")
	}

	if strings.Contains(v.MainTitle, "分享") {
		c.cmd.Println("  👉 开始执行 [分享单曲到站外]...")
		c.doVipSimulateBrowse(ctx, cli, request, v.JumpUrl, 3, 5, "分享单曲到站外")
		c.doVipSongShare(ctx, request, eapiRequest)
	}

	if v.MissionCode == "FLQ" || strings.Contains(v.MainTitle, "领福利") {
		c.cmd.Println("  👉 开始执行 [免费领福利]...")
		taskIdStr := strconv.FormatInt(v.MissionId, 10)
		if taskIdStr != "" && taskIdStr != "0" {
			jumpUrl := v.JumpUrl
			if !strings.Contains(jumpUrl, "view_task_id=") {
				if strings.Contains(jumpUrl, "?") {
					jumpUrl = fmt.Sprintf("%s&view_task_id=%s&view_task_business=music.vip_growth&view_time=15", jumpUrl, taskIdStr)
				} else {
					jumpUrl = fmt.Sprintf("%s?view_task_id=%s&view_task_business=music.vip_growth&view_time=15", jumpUrl, taskIdStr)
				}
			}
			c.doVipSimulateBrowse(ctx, cli, request, jumpUrl, 16, 20, "免费领福利")
		}
		c.doVipWelfareClaim(ctx, request, eapiRequest, userLevel)
	}
}

func (c *SignIn) handleVipTasks(ctx context.Context, cli *api.Client, request *weapi.Api, userLevel int64, accounts ...string) *vipGrowthState {
	account := "-"
	if len(accounts) > 0 && accounts[0] != "" {
		account = accounts[0]
	}
	enableVipTask := true
	if c.root.Cfg.Sign != nil && c.root.Cfg.Sign.EnableVipTask != nil {
		enableVipTask = *c.root.Cfg.Sign.EnableVipTask
	}

	if !enableVipTask {
		// 原有的极简 WEAPI 黑胶签到与领取逻辑
		vipSign, err := request.VipTaskSign(ctx, &weapi.VipTaskSignReq{IsNew: ""})
		if err == nil && vipSign != nil && vipSign.Code == 200 && vipSign.Data {
			c.cmd.Println("  👉 黑胶 VIP 乐签请求已接受，核对官方状态")
		}
		if confirmed, reason := getVipSignCompleted(ctx, eapi.New(cli), cli.GetDeviceId(), nil); confirmed {
			c.cmd.Printf("  ✅ 黑胶 VIP 乐签成功（%s）\n", reason)
		} else {
			c.cmd.Println("  ⚠️ 黑胶 VIP 乐签结果未确认，不自动重发")
			c.root.ReportUnconfirmed(account, "vip-sign", "乐签未获官方状态确认")
		}

		if c.isAutomatic() {
			reward, err := request.VipRewardGetAll(ctx, &weapi.VipRewardGetAllReq{})
			if err == nil && reward != nil && reward.Code == 200 && reward.Data.Result {
				c.cmd.Println("  👉 黑胶成长值领取请求已接受，实际到账以成长值与任务状态为准")
			} else {
				c.cmd.Println("  ⚠️ 黑胶成长值领取未确认，不自动重发")
				c.root.ReportUnconfirmed(account, "vip-reward", "成长值领取未获确定回执")
			}
		}
		return nil
	}

	eapiRequest := eapi.New(cli)
	var likedSongIds []string

	deviceId := cli.GetDeviceId()

	if deviceId != "" {
		c.cmd.Println("  👉 [deviceId] 使用本地已缓存的设备 ID（不显示内容）")
	} else {
		c.cmd.Println("  👉 [deviceId] 缓存中无有效设备 ID，本次打卡将不传递设备 ID 字段以规避验证风险。")
	}

	// 0. 获取今日乐签状态作为真实现状。
	var signedToday bool
	var signAttempted bool
	var rewardUnconfirmed bool
	attemptedTasks := make(map[string]eapi.VipTaskListData)
	var beforeGrowth *vipGrowthState
	if growth, err := getVipGrowthState(ctx, eapiRequest, deviceId); err == nil {
		beforeGrowth = &growth
		if growth.TodayScoreKnown {
			c.cmd.Printf("  👉 [执行前] 黑胶成长值现状: 今日已获得 %d，当前成长值 %d\n", growth.TodayScore, growth.GrowthPoint)
		} else {
			c.cmd.Printf("  👉 [执行前] 黑胶成长值现状: 今日累计未知，当前成长值 %d\n", growth.GrowthPoint)
		}
	} else {
		c.cmd.Printf("  ⚠️ 获取黑胶成长值现状失败（错误类型 %T）\n", err)
	}

	for round := 1; round <= 3; round++ {
		c.cmd.Printf("  👉 [黑胶任务第 %d/3 轮] 获取黑胶 VIP 任务列表...\n", round)
		taskList, err := eapiRequest.VipTaskList(ctx, newVipTaskListReq(deviceId))
		if err != nil || taskList == nil || taskList.Code != 200 {
			c.cmd.Printf("  ❌ 获取黑胶 VIP 任务列表失败（错误类型 %T）\n", err)
			c.root.ReportUnconfirmed(account, "vip-tasks", "无法读取黑胶任务状态")
			return nil
		}

		allTasks := taskList.Data
		if progressList, err := getVipProgressTasks(ctx, request); err == nil {
			existing := make(map[string]bool)
			for _, t := range allTasks {
				if t.MissionCode != "" {
					existing[t.MissionCode] = true
				}
				existing[t.MainTitle] = true
			}
			for _, t := range progressList {
				isDup := false
				if t.MissionCode != "" && existing[t.MissionCode] {
					isDup = true
				}
				if existing[t.MainTitle] {
					isDup = true
				}
				if !isDup {
					allTasks = append(allTasks, t)
				}
			}
		} else {
			c.cmd.Printf("  ⚠️ 获取成长中心任务列表失败（错误类型 %T）\n", err)
		}

		var signVerifyReason string
		if !signedToday {
			if ok, reason := getVipSignCompleted(ctx, eapiRequest, deviceId, allTasks); ok {
				signedToday = true
				signVerifyReason = reason
			}
		}

		// 筛选待做任务
		var todoTasks []eapi.VipTaskListData
		var hasVipSignTask bool
		for _, v := range allTasks {
			if isVipSignTask(v) {
				hasVipSignTask = true
				if !signedToday && !signAttempted {
					todoTasks = append(todoTasks, v)
				}
				continue
			}
			if v.Status != 100 {
				// 启动图由 App 原生能力保存，服务端不能用浏览上报代替实际设置。
				if isVipAppStartupTask(v) {
					if round == 1 {
						c.cmd.Println("  ℹ️ [设置开机启动图] 请在网易云音乐 App 内完成启动图设置，已跳过自动执行。")
						c.root.ReportSkip(account, "vip:"+v.MainTitle, "需 App 完成")
					}
					continue
				}
				// 该任务需 App 内完成，不加入队列，避免无效播放和轮次间等待。
				if isVipAppListenTask(v) {
					if round == 1 {
						c.cmd.Println("  ℹ️ [每日听3首VIP歌曲] 请在网易云音乐 App 内完成，已跳过自动播放。")
						c.root.ReportSkip(account, "vip:"+v.MainTitle, "需 App 完成")
					}
					continue
				}
				if reason := vipManualTaskReason(v); reason != "" {
					if round == 1 {
						c.cmd.Printf("  ℹ️ [%s] %s，已跳过自动执行。\n", v.MainTitle, reason)
						c.root.ReportSkip(account, "vip:"+v.MainTitle, reason)
					}
					continue
				}
				if _, attempted := attemptedTasks[vipTaskExecutionKey(v)]; attempted {
					c.cmd.Printf("  ℹ️ [%s] 本轮已执行，官方状态=%d；仅复查，不重复提交\n", musicPartnerSafeText(v.MainTitle), v.Status)
					continue
				}
				todoTasks = append(todoTasks, v)
			}
		}

		// 首轮仍需领取已完成任务的奖励；列表缺少乐签时保留后面的兜底签到。
		if len(todoTasks) == 0 && (signedToday || signAttempted) && round > 1 {
			c.cmd.Printf("  ℹ️ [黑胶任务第 %d/3 轮] 无待执行的未完成任务，退出循环\n", round)
			break
		}

		c.cmd.Printf("  👉 [黑胶任务第 %d/3 轮] 待完成任务列表:\n", round)
		for _, v := range todoTasks {
			worth := v.Worth
			if worth == 0 && isVipSignTask(v) {
				worth = 3
			}
			c.cmd.Printf("    - 任务: %-15s | 奖励: %d成长值\n", v.MainTitle, worth)
		}
		if signVerifyReason != "" {
			c.cmd.Printf("  ✅ 乐签状态验证通过: %s\n", signVerifyReason)
		}

		// 执行待做任务
		for i, v := range todoTasks {
			if isVipSignTask(v) {
				c.cmd.Println("  👉 开始执行 [黑胶乐签打卡]...")
				signAttempted = true
				signedToday = c.executeVipSign(ctx, eapiRequest, deviceId)
			} else {
				// 其他常规任务
				attemptedTasks[vipTaskExecutionKey(v)] = v
				c.executeSingleVipTask(ctx, cli, request, eapiRequest, v, deviceId, userLevel, &likedSongIds)
			}
			if i < len(todoTasks)-1 {
				c.sleepBetweenSubtasks(ctx, v.MainTitle)
			}
		}

		// 兜底签到逻辑
		if !hasVipSignTask && !signedToday && !signAttempted {
			c.cmd.Println("  👉 任务列表缺少乐签且未找到有效记录，执行一次乐签请求")
			signAttempted = true
			signedToday = c.executeVipSign(ctx, eapiRequest, deviceId)
		}

		// 一键领取所有已完成的成长值
		if c.isAutomatic() && !rewardUnconfirmed {
			c.cmd.Println("  👉 提交黑胶 VIP 成长值领取请求...")
			eapiReward, err := eapiRequest.VipRewardGetAll(ctx, &eapi.VipRewardGetAllReq{
				DeviceId: deviceId,
				OS:       "iOS",
				VerifyId: 1,
				Header:   struct{}{},
				ER:       true,
			})
			if err == nil && eapiReward != nil && eapiReward.Code == 200 && eapiReward.Data.Result {
				c.cmd.Println("  👉 黑胶成长值领取请求已接受，实际到账以最终成长值和任务状态为准")
			} else {
				rewardUnconfirmed = true
				c.cmd.Printf("  ⚠️ 黑胶成长值领取未获确定回执（错误类型 %T），不切换接口重发\n", err)
				c.root.ReportUnconfirmed(account, "vip-reward", "成长值领取未获确定回执")
			}
		}
		if len(todoTasks) == 0 && (signedToday || signAttempted) {
			break
		}
	}

	// 最终状态拉取与比对展示
	finalList, err := eapiRequest.VipTaskList(ctx, newVipTaskListReq(deviceId))
	var finalSignTasks []eapi.VipTaskListData
	if err == nil && finalList != nil && finalList.Code == 200 {
		finalSignTasks = finalList.Data
		c.cmd.Println("  👉 最终的黑胶 VIP 任务列表状态:")
		allFinalTasks := finalList.Data
		if progressList, err := getVipProgressTasks(ctx, request); err == nil {
			existing := make(map[string]bool)
			for _, t := range allFinalTasks {
				if t.MissionCode != "" {
					existing[t.MissionCode] = true
				}
				existing[t.MainTitle] = true
			}
			for _, t := range progressList {
				isDup := false
				if t.MissionCode != "" && existing[t.MissionCode] {
					isDup = true
				}
				if existing[t.MainTitle] {
					isDup = true
				}
				if !isDup {
					allFinalTasks = append(allFinalTasks, t)
				}
			}
		}
		for _, v := range allFinalTasks {
			key := vipTaskExecutionKey(v)
			_, attempted := attemptedTasks[key]
			delete(attemptedTasks, key)
			statusStr := fmt.Sprintf("未确认（状态 %d）", v.Status)
			if v.Status == 100 {
				statusStr = "已完成"
			} else if reason := vipManualTaskReason(v); reason != "" {
				statusStr = reason + "（已跳过）"
			} else if !isVipSignTask(v) && attempted {
				c.root.ReportUnconfirmed(account, "vip:"+v.MainTitle, fmt.Sprintf("官方任务状态仍为 %d", v.Status))
			} else if !isVipSignTask(v) {
				c.root.ReportSkip(account, "vip:"+v.MainTitle, "最终查询中出现，本轮未执行")
			}
			worth := v.Worth
			if worth == 0 && isVipSignTask(v) {
				worth = 3
			}
			c.cmd.Printf("    - 任务: %-15s | 状态: %-6s | 奖励: %d成长值\n", v.MainTitle, statusStr, worth)
		}
		for _, task := range attemptedTasks {
			c.cmd.Printf("  ⚠️ [%s] 已尝试执行，但最终任务列表缺失，结果未确认\n", task.MainTitle)
			c.root.ReportUnconfirmed(account, "vip:"+task.MainTitle, "已尝试执行，但最终任务列表缺失")
		}
	} else {
		c.cmd.Printf("  ❌ 无法获取最终对照任务列表（错误类型 %T）\n", err)
		c.root.ReportUnconfirmed(account, "vip-tasks", "最终任务状态查询失败")
	}
	if signAttempted || signedToday {
		if confirmed, reason := getVipSignCompleted(ctx, eapiRequest, deviceId, finalSignTasks); confirmed {
			c.cmd.Printf("  ✅ 乐签最终状态已确认：%s\n", reason)
		} else {
			c.root.ReportUnconfirmed(account, "vip-sign", "乐签未获官方状态确认，请在 App 核对")
		}
	}

	// 再次获取并展示今日乐签和成长值状态以作对比
	c.cmd.Println("  👉 获取执行后黑胶 VIP 状态以作对比...")
	var finalGrowthState *vipGrowthState
	if finalGrowth, err := getVipGrowthState(ctx, eapiRequest, deviceId); err == nil {
		if finalGrowth.TodayScoreKnown {
			c.cmd.Printf("  👉 [执行后] 黑胶成长值最终状态: 今日已获得 %d，当前成长值 %d\n", finalGrowth.TodayScore, finalGrowth.GrowthPoint)
		} else {
			c.cmd.Printf("  👉 [执行后] 黑胶成长值最终状态: 今日累计未知，当前成长值 %d\n", finalGrowth.GrowthPoint)
		}
		finalGrowthState = &finalGrowth
		if beforeGrowth != nil {
			c.cmd.Printf("  👉 本轮黑胶总成长值变化: %d -> %d，变化 %+d（与当日累计分开统计）\n", beforeGrowth.GrowthPoint, finalGrowth.GrowthPoint, finalGrowth.GrowthPoint-beforeGrowth.GrowthPoint)
		}
	} else {
		c.cmd.Printf("  ⚠️ 获取黑胶成长值最终状态失败（错误类型 %T）\n", err)
	}
	if finalSignState, err := getVipSignState(ctx, eapiRequest, deviceId); err == nil {
		if finalSignState.Today {
			c.cmd.Printf("  👉 [执行后] 乐签最终记录: recordId=%d time=%d score=%d\n", finalSignState.RecordId, finalSignState.Time, finalSignState.Score)
		}
	} else {
		c.cmd.Printf("  ⚠️ 获取黑胶乐签最终记录失败（错误类型 %T）\n", err)
	}

	// 统一取消红心，避免干扰用户歌单
	if len(likedSongIds) > 0 {
		c.cmd.Printf("  👉 统一取消 %d 首热门 VIP 歌曲的红心，彻底恢复用户歌单...\n", len(likedSongIds))
		for _, songIdStr := range likedSongIds {
			_, _ = eapiRequest.SongLike(ctx, &eapi.SongLikeReq{
				TrackId:    songIdStr,
				Like:       "false",
				Time:       "3",
				CheckToken: "",
			})
			sleepSec := 1 + rand.Intn(3)
			time.Sleep(time.Duration(sleepSec) * time.Second)
		}
		c.cmd.Println("  ✅ 统一取消红心完成")
	}
	return finalGrowthState
}

// doVipSongLike 专门操作热门 VIP 歌曲，不干扰个人收藏歌单
func (c *SignIn) doVipSongLike(ctx context.Context, request *weapi.Api, eapiRequest *eapi.Api) []string {
	// 获取热门 VIP 歌曲歌单 8402996200
	detail, err := request.PlaylistDetail(ctx, &weapi.PlaylistDetailReq{
		Id: "8402996200",
		N:  "10",
		S:  "0",
	})
	if err != nil || detail.Code != 200 || len(detail.Playlist.TrackIds) == 0 {
		c.cmd.Printf("  ❌ 获取热门 VIP 歌曲歌单失败，取消操作以避嫌: %v\n", err)
		return nil
	}

	trackIds := detail.Playlist.TrackIds
	c.cmd.Printf("  👉 成功获取热门 VIP 歌单，包含 %d 首歌曲，准备随机挑选 3 首进行红心打卡...\n", len(trackIds))

	n := len(trackIds)
	count := 3
	if n < 3 {
		count = n
	}

	// 随机打乱下标选择 3 首歌
	indices := rand.Perm(n)
	var likedIds []string
	successCount := 0
	for i := 0; i < count; i++ {
		idx := indices[i]
		songId := trackIds[idx].Id
		songIdStr := fmt.Sprintf("%d", songId)

		// 1. 红心该热门歌曲
		_, likeErr := eapiRequest.SongLike(ctx, &eapi.SongLikeReq{
			TrackId:    songIdStr,
			Like:       "true",
			Time:       "3",
			CheckToken: "",
		})
		if likeErr != nil {
			c.cmd.Printf("    ❌ [%d/3] 红心歌曲 ID %d 失败: %v\n", i+1, songId, likeErr)
			continue
		}

		// 2. 模拟播放器停留避嫌
		sleepSec := 3 + rand.Intn(8) // 3 ~ 10 秒
		c.cmd.Printf("    ⏳ [%d/3] 歌曲 ID %d 已红心，模拟在播放器停留 %d 秒以避嫌...\n", i+1, songId, sleepSec)

		select {
		case <-ctx.Done():
			c.cmd.Println("    ❌ 任务被取消")
			return likedIds
		case <-time.After(time.Duration(sleepSec) * time.Second):
		}

		likedIds = append(likedIds, songIdStr)
		successCount++
	}
	c.cmd.Printf("  ✅ 红心 3 首 VIP 单曲操作完毕，成功: %d/%d (暂保留红心以待领取奖励)\n", successCount, count)
	return likedIds
}

// doVipSongShare 专门分享热门 VIP 歌曲，用于打卡“分享单曲到站外”任务
func (c *SignIn) doVipSongShare(ctx context.Context, request *weapi.Api, eapiRequest *eapi.Api) {
	// 获取热门 VIP 歌曲歌单 8402996200
	detail, err := request.PlaylistDetail(ctx, &weapi.PlaylistDetailReq{
		Id: "8402996200",
		N:  "10",
		S:  "0",
	})
	if err != nil || detail.Code != 200 || len(detail.Playlist.TrackIds) == 0 {
		c.cmd.Printf("  ❌ 获取热门 VIP 歌曲歌单失败，无法执行分享: %v\n", err)
		return
	}

	trackIds := detail.Playlist.TrackIds
	n := len(trackIds)
	if n == 0 {
		c.cmd.Println("  ❌ 热门 VIP 歌单歌曲为空")
		return
	}

	// 随机挑选一首进行分享
	idx := rand.Intn(n)
	songId := trackIds[idx].Id
	songIdStr := fmt.Sprintf("%d", songId)

	c.cmd.Printf("  👉 随机选择 VIP 歌曲 ID %d 进行站外分享...\n", songId)

	// 调用 DailySongShareTrigger 进行分享（优先 wxsession 微信会话，失败时尝试 copylink 复制链接兜底）
	reqObj := &eapi.DailySongShareTriggerReq{
		SongID:  songIdStr,
		Channel: "wxsession",
	}
	reqObj.CryptoMode = api.CryptoModeEAPI
	resp, shareErr := eapiRequest.DailySongShareTrigger(ctx, reqObj)

	if shareErr != nil || resp.Code != 200 || !resp.Data {
		if shareErr != nil {
			c.cmd.Printf("  ⚠️ 微信分享(wxsession)上报失败: %v，尝试复制链接(copylink)兜底...\n", shareErr)
		} else {
			c.cmd.Printf("  ⚠️ 微信分享(wxsession)上报异常: code=%d msg=%s data=%v，尝试复制链接(copylink)兜底...\n", resp.Code, resp.Message, resp.Data)
		}
		// 复制链接 (copylink) 兜底
		reqObj.Channel = "copylink"
		resp, shareErr = eapiRequest.DailySongShareTrigger(ctx, reqObj)
		if shareErr != nil {
			c.cmd.Printf("  ❌ 复制链接(copylink)分享上报失败: %v\n", shareErr)
		} else if resp.Code != 200 || !resp.Data {
			c.cmd.Printf("  ⚠️ 复制链接(copylink)分享上报异常: code=%d msg=%s data=%v\n", resp.Code, resp.Message, resp.Data)
		} else {
			c.cmd.Printf("  ✅ 复制链接(copylink)分享单曲上报成功 (SongID: %s)\n", songIdStr)
		}
	} else {
		c.cmd.Printf("  ✅ 微信(wxsession)分享单曲上报成功 (SongID: %s)\n", songIdStr)
	}
}

// doVipSimulateBrowse 模拟请求活动页面并在本地进行停留随机延迟，达成网页停留要求
func (c *SignIn) doVipSimulateBrowse(ctx context.Context, cli *api.Client, request *weapi.Api, jumpUrl string, minSec, maxSec int, taskName string) {
	if jumpUrl == "" {
		c.cmd.Printf("  ⚠️ [%s] 的跳转链接为空，跳过浏览模拟\n", taskName)
		return
	}

	// Ensure jumpUrl has fromRN=1 to indicate App environment
	if !strings.Contains(jumpUrl, "fromRN=") {
		if strings.Contains(jumpUrl, "?") {
			jumpUrl += "&fromRN=1"
		} else {
			jumpUrl += "?fromRN=1"
		}
	}

	// Try parsing task parameters from jumpUrl for page view reporting
	var (
		taskId       string
		taskType     int    = 200
		viewTime     int64  = 15
		taskBusiness string = "music.vip_growth"
	)

	var pageCodes []string

	if parsedUrl, err := url.Parse(jumpUrl); err == nil {
		q := parsedUrl.Query()
		if q.Get("view_task_id") != "" {
			taskId = q.Get("view_task_id")
		}
		if q.Get("view_task_business") != "" {
			taskBusiness = q.Get("view_task_business")
		}
		if q.Get("view_time") != "" {
			if vt, err := strconv.ParseInt(q.Get("view_time"), 10, 64); err == nil {
				viewTime = vt
			}
		}
		if q.Get("view_task_type") != "" {
			if tt, err := strconv.Atoi(q.Get("view_task_type")); err == nil {
				taskType = tt
			}
		}

		// Dynamically construct pageCodes based on URL path base, component or route param
		if q.Get("component") != "" {
			comp := q.Get("component")
			cleaned := strings.ReplaceAll(comp, "-", "_")
			pageCodes = append(pageCodes, "music_vip_"+cleaned)
			pageCodes = append(pageCodes, cleaned)
			pageCodes = append(pageCodes, "music_vip_"+comp)
			pageCodes = append(pageCodes, comp)
			if strings.HasPrefix(comp, "rn-") {
				noRn := comp[3:]
				cleanedNoRn := strings.ReplaceAll(noRn, "-", "_")
				pageCodes = append(pageCodes, "music_vip_"+cleanedNoRn)
				pageCodes = append(pageCodes, cleanedNoRn)
				pageCodes = append(pageCodes, "music_vip_"+noRn)
				pageCodes = append(pageCodes, noRn)
			}
		}

		if q.Get("route") != "" {
			route := q.Get("route")
			cleanedRoute := strings.ReplaceAll(route, "-", "_")
			pageCodes = append(pageCodes, "music_vip_"+cleanedRoute)
			pageCodes = append(pageCodes, cleanedRoute)
			pageCodes = append(pageCodes, "music_vip_"+route)
			pageCodes = append(pageCodes, route)
		}

		if q.Get("component") != "" && q.Get("route") != "" {
			comp := q.Get("component")
			route := q.Get("route")
			comb := comp + "_" + route
			cleanedComb := strings.ReplaceAll(comb, "-", "_")
			pageCodes = append(pageCodes, "music_vip_"+cleanedComb)
			pageCodes = append(pageCodes, cleanedComb)
			pageCodes = append(pageCodes, "music_vip_"+comb)
			pageCodes = append(pageCodes, comb)
		}

		pathParts := strings.Split(parsedUrl.Path, "/")
		if len(pathParts) > 0 {
			lastPart := pathParts[len(pathParts)-1]
			if lastPart != "" {
				cleaned := strings.ReplaceAll(lastPart, "-", "_")
				pageCodes = append(pageCodes, "music_vip_"+cleaned)
				pageCodes = append(pageCodes, cleaned)
			}
		}
	}

	if len(pageCodes) == 0 {
		pageCodes = append(pageCodes, "music_vip_sound_effect_detail")
	}

	var webkitContext string
	webViewId := fmt.Sprintf("%d", 1000000000+rand.Int63n(9000000000))
	escapedHref := strings.ReplaceAll(jumpUrl, "/", "\\/")
	webkitContext = fmt.Sprintf(`{"webViewId":"%s","href":"%s","newebkit":1}`, webViewId, escapedHref)

	if strings.HasPrefix(jumpUrl, "http://") || strings.HasPrefix(jumpUrl, "https://") {
		c.cmd.Printf("  👉 模拟加载 [%s] 页面...\n", taskName)
		httpClient := cli.GetClient()
		req, err := http.NewRequestWithContext(ctx, "GET", jumpUrl, nil)
		if err != nil {
			c.cmd.Printf("  ⚠️ [%s] 创建请求失败: %v\n", taskName, err)
		} else {
			req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_6_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 CloudMusic/0.1.1 NeteaseMusic/9.4.95")
			req.Header.Set("netease_webkit_context", webkitContext)
			req.Header.Set("Referer", "https://music.163.com/")
			req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
			req.Header.Set("Accept-Language", "zh-CN,zh-Hans;q=0.9")

			resp, err := httpClient.Do(req)
			if err != nil {
				c.cmd.Printf("  ⚠️ [%s] 模拟页面请求失败 (but usually does not affect result): %v\n", taskName, err)
			} else {
				resp.Body.Close()
				c.cmd.Printf("  ℹ️ 页面加载响应码: %d\n", resp.StatusCode)
			}
		}
	} else {
		c.cmd.Printf("  ℹ️ [%s] 为 App 内部跳转链接，跳过 HTTP 模拟加载，直接进行浏览上报...\n", taskName)
	}

	// Report viewStart to create a server-side browsing session (for all candidate pageCodes and resourceTypes)
	if taskId != "" {
		resourceTypes := []string{"vip_growth", taskBusiness, ""}
		for _, pc := range pageCodes {
			for _, rt := range resourceTypes {
				startBytes, _ := json.Marshal(weapi.VipMiddlePageViewReportData{
					ActionType:   "viewStart",
					Time:         time.Now().UnixNano() / int64(time.Millisecond),
					TaskId:       taskId,
					TaskType:     taskType,
					ViewTime:     0,
					JumpUrl:      jumpUrl,
					TaskBusiness: taskBusiness,
					ResourceType: rt,
					PageCode:     pc,
				})
				_, _ = request.VipMiddlePageViewReport(ctx, &weapi.VipMiddlePageViewReportReq{
					WebkitContext: webkitContext,
					Data:          string(startBytes),
				})
			}
		}
	}

	// Stay at least viewTime seconds plus a little random delay
	sleepSec := int(viewTime)
	if minSec > sleepSec {
		sleepSec = minSec
	}
	if maxSec > sleepSec {
		sleepSec = sleepSec + rand.Intn(maxSec-sleepSec+1)
	} else {
		sleepSec = sleepSec + rand.Intn(5) // default 0~4s random delay
	}

	c.cmd.Printf("  ⏳ 正在模拟 [%s] 的页面停留，等待 %d 秒...\n", taskName, sleepSec)
	select {
	case <-ctx.Done():
		c.cmd.Println("  ❌ 浏览模拟被取消")
		return
	case <-time.After(time.Duration(sleepSec) * time.Second):
	}
	c.cmd.Printf("  ✅ [%s] 页面浏览模拟完成\n", taskName)

	// Report page view end to complete the task (for all candidate pageCodes and resourceTypes)
	if taskId != "" {
		resourceTypes := []string{"vip_growth", taskBusiness, ""}
		for _, pc := range pageCodes {
			for _, rt := range resourceTypes {
				c.cmd.Printf("  👉 正在上报 [%s] 的浏览完成状态 (taskId: %s, pageCode: %s, resourceType: %s, duration: %d秒)...\n", taskName, taskId, pc, rt, sleepSec)
				dataBytes, _ := json.Marshal(weapi.VipMiddlePageViewReportData{
					ActionType:   "viewEnd",
					Time:         time.Now().UnixNano() / int64(time.Millisecond),
					TaskId:       taskId,
					TaskType:     taskType,
					ViewTime:     int64(sleepSec) * 1000,
					JumpUrl:      jumpUrl,
					TaskBusiness: taskBusiness,
					ResourceType: rt,
					PageCode:     pc,
				})
				resp, reportErr := request.VipMiddlePageViewReport(ctx, &weapi.VipMiddlePageViewReportReq{
					WebkitContext: webkitContext,
					Data:          string(dataBytes),
				})
				if reportErr != nil {
					c.cmd.Printf("  ❌ 上报 [%s](%s/%s) 浏览完成状态失败: %v\n", taskName, pc, rt, reportErr)
				} else {
					c.cmd.Printf("  ✅ 上报 [%s](%s/%s) 浏览完成状态成功: code=%d, data=%v, msg=%s\n", taskName, pc, rt, resp.Code, resp.Data, resp.Message)
				}
			}
		}
	}
}

// doVipWelfareClaim 获取会员等级福利列表并自动领取第一个未领福利
func (c *SignIn) doVipWelfareClaim(ctx context.Context, request *weapi.Api, eapiRequest *eapi.Api, userLevel int64) {
	// 1. 优先尝试自动领券打卡日常“免费领福利”任务
	c.cmd.Println("  👉 获取当前常驻免费商家福利券列表...")
	benefitList, err := eapiRequest.VipBenefitCategoryList(ctx, &eapi.VipBenefitCategoryListReq{
		Category: "1291816",
		Header:   "{}",
		ER:       true,
	})
	if err == nil && benefitList.Code == 200 && len(benefitList.Data) > 0 {
		var targetBenefitId int64
		var targetBenefitName string
		// 优先查找含有“免费”或“0元”等字样的纯免费商家福利券
		for _, b := range benefitList.Data {
			if !b.BenefitGet && b.Id > 0 {
				nameLower := strings.ToLower(b.Name)
				if strings.Contains(nameLower, "免费") || strings.Contains(nameLower, "0元") {
					targetBenefitId = b.Id
					targetBenefitName = b.Name
					break
				}
			}
		}
		// 如果没有找到带免费字样的，再兜底选择第一个未领取的福利券
		if targetBenefitId == 0 {
			for _, b := range benefitList.Data {
				if !b.BenefitGet && b.Id > 0 {
					targetBenefitId = b.Id
					targetBenefitName = b.Name
					break
				}
			}
		}

		if targetBenefitId > 0 {
			c.cmd.Printf("  👉 发现尚未领取的商家福利券: [%s] (Id: %d)，开始自动领券打卡...\n", targetBenefitName, targetBenefitId)
			getResp, getErr := eapiRequest.VipBenefitGet(ctx, &eapi.VipBenefitGetReq{
				Id:     fmt.Sprintf("%d", targetBenefitId),
				Header: "{}",
				ER:     true,
			})
			if getErr != nil {
				c.cmd.Printf("  ❌ 领取福利券 [%s] 失败 (网络错误): %v\n", targetBenefitName, getErr)
			} else if getResp.Code != 200 || !getResp.Result.BenefitGet {
				c.cmd.Printf("  ❌ 领取福利券 [%s] 失败: code=%d, msg=%s\n", targetBenefitName, getResp.Code, getResp.Message)
			} else {
				c.cmd.Printf("  👉 商家券接口确认领取: [%s]；福利券成长任务以最终官方状态为准\n", musicPartnerSafeText(targetBenefitName))
			}
		} else {
			c.cmd.Println("  ℹ️ 所有商家福利券都已领过，继续后续福利打卡")
		}
	} else {
		c.cmd.Printf("  ⚠️ 获取商家福利券列表失败 (可能是网络波动或接口微调): %v\n", err)
	}

	// 2. 兜底方案：触发 EAPI 版本的福利列表获取，此操作同样可尝试触发日常任务判定
	_, _ = eapiRequest.VipWelfareList(ctx, &eapi.VipWelfareListReq{
		Header: "{}",
		ER:     true,
	})

	c.cmd.Println("  👉 获取当前可领取的黑胶等级福利列表...")
	welfareList, err := request.VipWelfareList(ctx, &weapi.VipWelfareListReq{})
	if err != nil || welfareList.Code != 200 {
		c.cmd.Printf("  ❌ 获取福利列表失败: %v\n", err)
		return
	}

	var targetWelfareId int64
	var targetWelfareName string

	// 遍历等级 map 寻找 UserReceiveStatus 为 0 (代表未领取) 且 Id > 0 的等级特权
	for levelKey, list := range welfareList.Data {
		var level int64
		if lVal, err := strconv.ParseInt(levelKey, 10, 64); err == nil {
			level = lVal
		}
		if userLevel > 0 && level > userLevel {
			continue
		}

		for _, w := range list {
			if w.UserReceiveStatus == 0 && w.Id > 0 {
				targetWelfareId = w.Id
				targetWelfareName = fmt.Sprintf("%s (等级特权:%s)", w.ShowName, levelKey)
				break
			}
		}
		if targetWelfareId > 0 {
			break
		}
	}

	if targetWelfareId == 0 {
		c.cmd.Println("  ℹ️ 没有找到当前可领取的等级特权福利，可能已全部领取")
		return
	}

	c.cmd.Printf("  👉 发现可领取福利: [%s] (Id: %d)，开始执行领取...\n", targetWelfareName, targetWelfareId)
	claimResp, err := request.VipWelfareClaim(ctx, &weapi.VipWelfareClaimReq{
		WelfareId: targetWelfareId,
	})
	if err != nil {
		c.cmd.Printf("  ❌ 领取福利 [%s] 失败 (网络错误): %v\n", targetWelfareName, err)
	} else if claimResp.Code == 404 {
		c.cmd.Printf("  ℹ️ 福利 [%s] 无法自动领取，可能已被领完或相关活动接口已下线 (404)\n", targetWelfareName)
	} else if claimResp.Code != 200 {
		c.cmd.Printf("  ❌ 领取福利 [%s] 失败 (服务限制): code=%d, msg=%s\n", targetWelfareName, claimResp.Code, claimResp.Message)
	} else {
		c.cmd.Printf("  🎉 成功领取尊享等级福利: [%s]\n", targetWelfareName)
	}
}

// getVipProgressTasks 获取成长中心任务列表，将其转换为标准任务格式以丰富黑胶任务列表
func getVipProgressTasks(ctx context.Context, request *weapi.Api) ([]eapi.VipTaskListData, error) {
	resp, err := request.VipProgressList(ctx, &weapi.VipProgressListReq{})
	if err != nil {
		return nil, err
	}
	if resp.Code != 200 {
		return nil, fmt.Errorf("VipProgressList code=%d", resp.Code)
	}

	var list []eapi.VipTaskListData
	for _, item := range resp.Data {
		var jumpUrl string
		if item.BasicMissionDTO.SchemaContent != "" {
			var sc struct {
				JumpUrl string `json:"jumpUrl"`
			}
			_ = json.Unmarshal([]byte(item.BasicMissionDTO.SchemaContent), &sc)
			jumpUrl = sc.JumpUrl
			if jumpUrl == "" {
				var scSpace struct {
					JumpUrl string `json:"jumpUrl "`
				}
				_ = json.Unmarshal([]byte(item.BasicMissionDTO.SchemaContent), &scSpace)
				jumpUrl = scSpace.JumpUrl
			}
		}

		worth := int64(0)
		if len(item.StageProgressDTOS) > 0 {
			worth = item.StageProgressDTOS[0].Worth
		}

		list = append(list, eapi.VipTaskListData{
			MissionId:       item.BasicMissionDTO.MissionId,
			MissionCode:     item.BasicMissionDTO.MissionCode,
			MissionType:     item.BasicMissionDTO.MissionType,
			MissionEntityId: item.BasicMissionDTO.MissionEntityId,
			Status:          item.MissionStatus,
			Worth:           worth,
			MainTitle:       item.BasicMissionDTO.Name,
			JumpUrl:         jumpUrl,
		})
	}
	return list, nil
}

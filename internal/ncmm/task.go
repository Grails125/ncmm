// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

package ncmm

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/3899/ncmm/pkg/log"
	"github.com/3899/ncmm/pkg/notify"

	"github.com/spf13/cobra"
)

type TaskOpts struct {
	Sign           bool
	PlayIds        bool
	MusicianSign   bool
	MusicianVip    bool
	Note           bool
	FansGroup      bool
	DailySongShare bool
	VipMemberGift  bool
	MusicPartner   bool
	OnlyFast       bool
	OnlySlow       bool
}

type Task struct {
	root            *Root
	cmd             *cobra.Command
	l               *log.Logger
	opts            TaskOpts
	rng             *rand.Rand
	wait            func(context.Context, time.Duration)
	failures        []error
	skipped         int
	musicPartnerErr error // 评分未能执行时保留非零退出；已执行的评分错误统一记入 failures。
}

type Account struct {
	Filepath string
	IsMain   bool
}

func NewTask(root *Root, l *log.Logger) *Task {
	c := &Task{
		root: root,
		l:    l,
		rng:  rand.New(rand.NewSource(time.Now().UnixNano())),
		wait: taskWait,
		cmd: &cobra.Command{
			Use:     "task",
			Short:   "Batch execute configured tasks",
			Example: "  ncmm task\n  ncmm task --sign --playids\n  ncmm task --musician-sign --musician-vip\n  ncmm task --only-fast",
		},
	}
	c.addFlags()
	c.cmd.RunE = func(cmd *cobra.Command, args []string) error {
		err := c.execute(cmd.Context())
		if err != nil {
			// 运行时错误由 Root.Execute 统一输出，避免重复错误和无关的命令用法。
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
		}
		return err
	}
	return c
}

func (c *Task) Command() *cobra.Command {
	return c.cmd
}

func (c *Task) addFlags() {
	c.cmd.Flags().BoolVar(&c.opts.MusicPartner, "music-partner", false, "执行音乐合伙人自动评分")
	c.cmd.Flags().BoolVar(&c.opts.DailySongShare, "daily-song-share", false, "execute daily song share task")
	c.cmd.Flags().BoolVar(&c.opts.VipMemberGift, "vip-member-gift", false, "execute VIP member gift task")
	c.cmd.Flags().BoolVar(&c.opts.Sign, "sign", false, "执行日常签到任务")
	c.cmd.Flags().BoolVar(&c.opts.PlayIds, "playids", false, "执行播放指定歌曲任务")
	c.cmd.Flags().BoolVar(&c.opts.MusicianSign, "musician-sign", false, "执行音乐人日常签到任务")
	c.cmd.Flags().BoolVar(&c.opts.MusicianVip, "musician-vip", false, "执行音乐人VIP进阶任务")
	c.cmd.Flags().BoolVar(&c.opts.Note, "note", false, "执行笔记发布任务")
	c.cmd.Flags().BoolVar(&c.opts.FansGroup, "fansgroup", false, "执行乐迷团任务")
	c.cmd.Flags().BoolVar(&c.opts.OnlyFast, "only-fast", false, "仅执行快任务组")
	c.cmd.Flags().BoolVar(&c.opts.OnlySlow, "only-slow", false, "仅执行慢任务组")
}

func (c *Task) getAccounts() []Account {
	var accounts []Account
	cfg := c.root.Cfg
	if cfg.Accounts != nil {
		if cfg.Accounts.Main != "" {
			accounts = append(accounts, Account{Filepath: cfg.Accounts.Main, IsMain: true})
		}
		for _, sec := range cfg.Accounts.Secondary {
			if sec != "" {
				accounts = append(accounts, Account{Filepath: sec, IsMain: false})
			}
		}
	}
	return accounts
}

func (c *Task) standardizeActionKey(action string) string {
	keys := []string{
		"VipTask", "Reserve", "ViewVipCenter", "LikeComment", "FollowArtist",
		"LikeSong", "CollectSong", "PublishNote", "ShareSong", "ListenIndie", "PlayDailyRecommend",
		"musician-sign", "note", "daily-song-share", "vip-member-gift", "fansgroup", "playids", "musician-vip", "music-partner",
	}
	for _, k := range keys {
		if strings.EqualFold(k, action) {
			return k
		}
	}
	return ""
}

func (c *Task) isActionActive(stdAction string, activeTasks map[string]bool) bool {
	signSubtasks := []string{
		"VipTask", "Reserve", "ViewVipCenter", "LikeComment", "FollowArtist",
		"LikeSong", "CollectSong", "PublishNote", "ShareSong", "ListenIndie", "PlayDailyRecommend",
	}
	for _, sub := range signSubtasks {
		if stdAction == sub {
			return activeTasks["sign"]
		}
	}
	return activeTasks[stdAction]
}

func (c *Task) isActionEnabledForAccount(stdAction string, isMain bool) bool {
	cfg := c.root.Cfg
	actionLower := strings.ToLower(stdAction)

	if actionLower == "note" {
		return isMain
	}
	if actionLower == "music-partner" {
		if isMain {
			return cfg.MusicPartner != nil && cfg.MusicPartner.EnableMain
		}
		return cfg.MusicPartner != nil && cfg.MusicPartner.EnableSecondaries
	}

	isSignSubtask := false
	signSubtasks := []string{
		"viptask", "reserve", "viewvipcenter", "likecomment", "followartist",
		"likesong", "collectsong", "publishnote", "sharesong", "listenindie", "playdailyrecommend",
	}
	for _, sub := range signSubtasks {
		if actionLower == sub {
			isSignSubtask = true
			break
		}
	}

	if isSignSubtask || actionLower == "sign" {
		if isMain {
			return cfg.Sign != nil && cfg.Sign.EnableMain
		} else {
			return cfg.Sign != nil && cfg.Sign.EnableSecondaries
		}
	}

	if actionLower == "playids" {
		if isMain {
			return cfg.PlayIds != nil && cfg.PlayIds.EnableMain
		} else {
			return cfg.PlayIds != nil && cfg.PlayIds.EnableSecondaries
		}
	}

	if actionLower == "musician-sign" {
		if isMain {
			return cfg.Musician != nil && cfg.Musician.EnableMain
		} else {
			return cfg.Musician != nil && cfg.Musician.EnableSecondaries
		}
	}

	if actionLower == "musician-vip" {
		if isMain {
			return cfg.Musician != nil && cfg.Musician.EnableMain
		} else {
			return cfg.Musician != nil && cfg.Musician.EnableSecondaries
		}
	}

	if actionLower == "fansgroup" {
		if isMain {
			return cfg.FansGroup != nil && cfg.FansGroup.EnableMain
		} else {
			return cfg.FansGroup != nil && cfg.FansGroup.EnableSecondaries
		}
	}

	if actionLower == "daily-song-share" {
		if isMain {
			return cfg.DailySongShare != nil && cfg.DailySongShare.EnableMain
		} else {
			return cfg.DailySongShare != nil && cfg.DailySongShare.EnableSecondaries
		}
	}

	if actionLower == "vip-member-gift" {
		if isMain {
			return cfg.VipMemberGift != nil && cfg.VipMemberGift.EnableMain
		} else {
			return cfg.VipMemberGift != nil && cfg.VipMemberGift.EnableSecondaries
		}
	}

	return false
}

func (c *Task) sleepBetweenTasks(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	sleepSec := 1 + c.rng.Intn(5) // 1 ~ 5 秒
	c.cmd.Printf("[task] ⏳ 为规避数据库锁竞争与接口频率限制，随机等待 %d 秒后继续下一个任务...\n", sleepSec)
	c.wait(ctx, time.Duration(sleepSec)*time.Second)
}

func (c *Task) sleepBetweenAccounts(ctx context.Context, currentAccount string) {
	if ctx.Err() != nil {
		return
	}
	sleepSec := 5 + c.rng.Intn(16) // 5 ~ 20 秒
	c.cmd.Printf("[task] ⏳ 账号 (%s) 任务处理完毕，为规避风控，随机等待 %d 秒后继续下一个账号...\n", currentAccount, sleepSec)
	c.wait(ctx, time.Duration(sleepSec)*time.Second)
}

func taskWait(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (c *Task) reportFailure(account, action string, err error) {
	if err == nil {
		return
	}
	label := action
	if action == "music-partner" {
		label = "音乐合伙人评分"
	}
	c.failures = append(c.failures, fmt.Errorf("账号 (%s) [%s]: %w", account, label, err))
	c.root.ReportFailure(account, action, err)
}

func (c *Task) reportSkip(account, action, reason string) {
	c.skipped++
	c.root.ReportSkip(account, action, reason)
}

// isSessionInvalidError 判断错误是否为 Cookie/会话失效，此类错误应跳过该账号后续任务。
func isSessionInvalidError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errMusicPartnerAccountBlocked) {
		return true
	}
	msg := strings.ToLower(err.Error())
	patterns := []string{
		"cookie 已失效",
		"需要登录",
		"need login",
		"not logged in",
		"session expired",
		"user not logged in",
	}
	for _, p := range patterns {
		if strings.Contains(msg, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// executeAction runs one task action. sessionInvalid is true when the cookie/session is dead
// and remaining tasks for this account should be skipped.
func (c *Task) executeAction(
	ctx context.Context,
	stdAction string,
	account Account,
	queue []string,
	activeTasks map[string]bool,
	signRunMap map[string]bool,
	earningsReports map[string]*accountEarningsReport,
	finalizeSignEarnings bool,
) (executed bool, sessionInvalid bool) {
	if ctx.Err() != nil {
		return false, false
	}
	if stdAction == "music-partner" {
		c.cmd.Printf("[task] >>> 账号 (%s) 开始执行 [音乐合伙人评分] <<<\n", account.Filepath)
		p := NewMusicPartner(c.root, c.l)
		p.Command().SetOut(c.cmd.OutOrStdout())
		p.Command().SetErr(c.cmd.ErrOrStderr())
		if err := p.executeAccount(ctx, account.Filepath, c.root.musicPartnerCookiePath(account.Filepath)); err != nil {
			c.cmd.Printf("[task] ❌ 账号 (%s) [音乐合伙人评分] 失败: %s\n", account.Filepath, err)
			c.reportFailure(account.Filepath, "music-partner", err)
			sessionInvalid = isSessionInvalidError(err)
		}
		c.sleepBetweenTasks(ctx)
		return true, sessionInvalid
	}
	isSignSubtask := false
	signSubtasks := []string{
		"VipTask", "Reserve", "ViewVipCenter", "LikeComment", "FollowArtist",
		"LikeSong", "CollectSong", "PublishNote", "ShareSong", "ListenIndie", "PlayDailyRecommend",
	}
	for _, sub := range signSubtasks {
		if stdAction == sub {
			isSignSubtask = true
			break
		}
	}

	if isSignSubtask {
		if !signRunMap[account.Filepath] {
			allowedTasks := make(map[string]bool)
			for _, qItem := range queue {
				stdQItem := c.standardizeActionKey(qItem)
				for _, sub := range signSubtasks {
					if stdQItem == sub && c.isActionActive(stdQItem, activeTasks) {
						allowedTasks[stdQItem] = true
					}
				}
			}
			if len(allowedTasks) > 0 {
				c.cmd.Printf("[task] >>> 账号 (%s) 开始执行 [日常签到] 子任务组 <<<\n", account.Filepath)
				s := NewSign(c.root, c.l)
				report := earningsReports[account.Filepath]
				if report == nil {
					report = &accountEarningsReport{Account: account.Filepath}
					earningsReports[account.Filepath] = report
				}
				if err := s.runSignForCookie(ctx, account.Filepath, account.IsMain, allowedTasks, report, finalizeSignEarnings); err != nil {
					c.cmd.Printf("[task] ❌ 账号 (%s) [日常签到] 执行失败: %s\n", account.Filepath, err)
					c.reportFailure(account.Filepath, "sign", err)
					sessionInvalid = isSessionInvalidError(err)
				} else {
					c.cmd.Printf("[task] ✅ 账号 (%s) [日常签到] 执行完毕\n", account.Filepath)
				}
				c.sleepBetweenTasks(ctx)
				executed = true
			}
			signRunMap[account.Filepath] = true
		}
		return executed, sessionInvalid
	}

	if stdAction == "playids" {
		c.cmd.Printf("[task] >>> 账号 (%s) 开始执行 [播放指定歌曲] 任务 <<<\n", account.Filepath)
		p := NewPlayIds(c.root, c.l)
		if err := p.RunForCookie(ctx, account.Filepath); err != nil {
			c.cmd.Printf("[task] ❌ 账号 (%s) [播放指定歌曲] 执行失败: %s\n", account.Filepath, err)
			c.reportFailure(account.Filepath, "playids", err)
			sessionInvalid = isSessionInvalidError(err)
		} else {
			c.cmd.Printf("[task] ✅ 账号 (%s) [播放指定歌曲] 执行完毕\n", account.Filepath)
		}
		c.sleepBetweenTasks(ctx)
		return true, sessionInvalid
	}

	if stdAction == "musician-sign" {
		c.cmd.Printf("[task] >>> 账号 (%s) 开始执行 [音乐人日常签到] 任务 <<<\n", account.Filepath)
		m := NewMusician(c.root, c.l)
		if err := m.RunSignForCookie(ctx, account.Filepath); err != nil {
			c.cmd.Printf("[task] ❌ 账号 (%s) [音乐人日常签到] 执行失败: %s\n", account.Filepath, err)
			c.reportFailure(account.Filepath, "musician-sign", err)
			sessionInvalid = isSessionInvalidError(err)
		} else {
			c.cmd.Printf("[task] ✅ 账号 (%s) [音乐人日常签到] 执行完毕\n", account.Filepath)
		}
		c.sleepBetweenTasks(ctx)
		return true, sessionInvalid
	}

	if stdAction == "musician-vip" {
		c.cmd.Printf("[task] >>> 账号 (%s) 开始执行 [音乐人VIP进阶] 任务 <<<\n", account.Filepath)
		m := NewMusician(c.root, c.l)
		if err := m.RunVipForCookie(ctx, account.Filepath); err != nil {
			c.cmd.Printf("[task] ❌ 账号 (%s) [音乐人VIP进阶] 执行失败: %s\n", account.Filepath, err)
			c.reportFailure(account.Filepath, "musician-vip", err)
			sessionInvalid = isSessionInvalidError(err)
		} else {
			c.cmd.Printf("[task] ✅ 账号 (%s) [音乐人VIP进阶] 执行完毕\n", account.Filepath)
		}
		c.sleepBetweenTasks(ctx)
		return true, sessionInvalid
	}

	if stdAction == "note" {
		c.cmd.Printf("[task] >>> 账号 (%s) 开始执行 [发布图文动态] 任务 <<<\n", account.Filepath)
		n := NewNote(c.root, c.l)
		if _, err := n.ExecuteForCookie(ctx, account.Filepath); err != nil {
			c.cmd.Printf("[task] ❌ 账号 (%s) [发布图文动态] 执行失败: %s\n", account.Filepath, err)
			c.reportFailure(account.Filepath, "note", err)
			sessionInvalid = isSessionInvalidError(err)
		} else {
			c.cmd.Printf("[task] ✅ 账号 (%s) [发布图文动态] 执行完毕\n", account.Filepath)
		}
		c.sleepBetweenTasks(ctx)
		return true, sessionInvalid
	}

	if stdAction == "daily-song-share" {
		c.cmd.Printf("[task] >>> account (%s) start [daily song share] task <<<\n", account.Filepath)
		d := NewDailySongShare(c.root, c.l)
		cfg, err := d.getConfig()
		if err != nil {
			c.cmd.Printf("[task] account (%s) [daily song share] skipped: %s\n", account.Filepath, err)
			c.reportSkip(account.Filepath, "daily-song-share", err.Error())
			return false, false
		}
		if err := d.validatePrerequisites(cfg); err != nil {
			c.cmd.Printf("[task] account (%s) [daily song share] skipped: %s\n", account.Filepath, err)
			c.reportSkip(account.Filepath, "daily-song-share", err.Error())
			return false, false
		}
		if _, err := d.ExecuteForCookie(ctx, account.Filepath); err != nil {
			// missing token is operational skip rather than hard failure
			if strings.Contains(err.Error(), "跳过") || strings.Contains(strings.ToLower(err.Error()), "skip") {
				c.cmd.Printf("[task] 账号 (%s) [每日推歌] 跳过: %s\n", account.Filepath, err)
				c.reportSkip(account.Filepath, "daily-song-share", err.Error())
			} else {
				c.cmd.Printf("[task] 账号 (%s) [每日推歌] 失败: %s\n", account.Filepath, err)
				c.reportFailure(account.Filepath, "daily-song-share", err)
				sessionInvalid = isSessionInvalidError(err)
			}
		} else {
			c.cmd.Printf("[task] account (%s) [daily song share] done\n", account.Filepath)
		}
		c.sleepBetweenTasks(ctx)
		return true, sessionInvalid
	}

	if stdAction == "vip-member-gift" {
		c.cmd.Printf("[task] >>> account (%s) start [vip member gift] task <<<\n", account.Filepath)
		v := NewVipMemberGift(c.root, c.l)
		cfg, err := v.getConfig()
		if err != nil {
			c.cmd.Printf("[task] account (%s) [vip member gift] skipped: %s\n", account.Filepath, err)
			c.reportSkip(account.Filepath, "vip-member-gift", err.Error())
			return false, false
		}
		if err := v.validateCommonPrerequisites(cfg); err != nil {
			c.cmd.Printf("[task] account (%s) [vip member gift] skipped: %s\n", account.Filepath, err)
			c.reportSkip(account.Filepath, "vip-member-gift", err.Error())
			return false, false
		}
		if err := v.ExecuteForCookie(ctx, account.Filepath); err != nil {
			if strings.Contains(err.Error(), "跳过") || strings.Contains(strings.ToLower(err.Error()), "skip") {
				c.cmd.Printf("[task] 账号 (%s) [会员礼品] 跳过: %s\n", account.Filepath, err)
				c.reportSkip(account.Filepath, "vip-member-gift", err.Error())
			} else {
				c.cmd.Printf("[task] 账号 (%s) [会员礼品] 失败: %s\n", account.Filepath, err)
				c.reportFailure(account.Filepath, "vip-member-gift", err)
				sessionInvalid = isSessionInvalidError(err)
			}
		} else {
			c.cmd.Printf("[task] account (%s) [vip member gift] done\n", account.Filepath)
		}
		c.sleepBetweenTasks(ctx)
		return true, sessionInvalid
	}

	if stdAction == "fansgroup" {
		c.cmd.Printf("[task] >>> 账号 (%s) 开始执行 [乐迷团任务] <<<\n", account.Filepath)
		f := NewFansGroup(c.root, c.l)
		if err := f.ExecuteForCookie(ctx, account.Filepath); err != nil {
			c.cmd.Printf("[task] ❌ 账号 (%s) [乐迷团任务] 执行失败: %s\n", account.Filepath, err)
			c.reportFailure(account.Filepath, "fansgroup", err)
			sessionInvalid = isSessionInvalidError(err)
		} else {
			c.cmd.Printf("[task] ✅ 账号 (%s) [乐迷团任务] 执行完毕\n", account.Filepath)
		}
		c.sleepBetweenTasks(ctx)
		return true, sessionInvalid
	}
	return false, false
}

func (c *Task) queueHasActiveSignTasks(queue []string, activeTasks map[string]bool) bool {
	for _, action := range queue {
		switch c.standardizeActionKey(action) {
		case "VipTask", "Reserve", "ViewVipCenter", "LikeComment", "FollowArtist",
			"LikeSong", "CollectSong", "PublishNote", "ShareSong", "ListenIndie", "PlayDailyRecommend":
			if activeTasks["sign"] {
				return true
			}
		}
	}
	return false
}

// runQueue executes a task queue. invalidSessions is shared across fast/slow queues for the whole run.
func (c *Task) runQueue(
	ctx context.Context,
	queue []string,
	accounts []Account,
	activeTasks map[string]bool,
	invalidSessions map[string]bool,
	earningsReports map[string]*accountEarningsReport,
	finalizeSignEarnings bool,
) bool {
	if invalidSessions == nil {
		invalidSessions = make(map[string]bool)
	}
	signRunMap := make(map[string]bool)
	var queueExecuted bool

	for i, account := range accounts {
		if ctx.Err() != nil {
			break
		}
		if invalidSessions[account.Filepath] {
			c.markAccountTasksBlocked(account, queue, activeTasks, false)
			c.cmd.Printf("[task] ⚠️ 账号 (%s) 鉴权失效或被限流，跳过本队列全部任务\n", account.Filepath)
			continue
		}

		var hasExecuted bool
		for actionIndex, action := range queue {
			if ctx.Err() != nil {
				break
			}
			if invalidSessions[account.Filepath] {
				break
			}

			stdAction := c.standardizeActionKey(action)
			if stdAction == "" {
				continue
			}

			if !c.isActionActive(stdAction, activeTasks) {
				continue
			}

			if !c.isActionEnabledForAccount(stdAction, account.IsMain) {
				continue
			}

			executed, sessionInvalid := c.executeAction(ctx, stdAction, account, queue, activeTasks, signRunMap, earningsReports, finalizeSignEarnings)
			if executed {
				hasExecuted = true
				queueExecuted = true
			}
			if sessionInvalid {
				invalidSessions[account.Filepath] = true
				c.markAccountTasksBlocked(account, queue[actionIndex+1:], activeTasks, signRunMap[account.Filepath])
				c.cmd.Printf("[task] ⚠️ 账号 (%s) 鉴权失效或被限流，跳过该账号后续任务\n", account.Filepath)
				break
			}
		}
		if hasExecuted && i < len(accounts)-1 {
			c.sleepBetweenAccounts(ctx, account.Filepath)
		}
	}
	return queueExecuted
}

func (c *Task) markAccountTasksBlocked(account Account, queue []string, activeTasks map[string]bool, signAlreadyRun bool) {
	seen := make(map[string]bool)
	for _, action := range queue {
		stdAction := c.standardizeActionKey(action)
		if stdAction == "" || !c.isActionActive(stdAction, activeTasks) || !c.isActionEnabledForAccount(stdAction, account.IsMain) {
			continue
		}
		reportAction := stdAction
		if !activeTasks[stdAction] {
			// 签到子任务由同一个 runSignForCookie 调用处理，不重复统计该调用。
			if signAlreadyRun {
				continue
			}
			reportAction = "sign"
		}
		if seen[reportAction] {
			continue
		}
		seen[reportAction] = true
		reason := "账号鉴权失效或被限流，任务未执行"
		if reportAction == "music-partner" && c.musicPartnerErr == nil {
			c.musicPartnerErr = fmt.Errorf("账号 (%s) 鉴权失效或被限流，音乐合伙人评分未执行", account.Filepath)
		}
		c.reportSkip(account.Filepath, reportAction, reason)
	}
}

func (c *Task) execute(ctx context.Context) error {
	c.failures = nil
	c.skipped = 0
	c.musicPartnerErr = nil
	c.root.yunbeiRewards = nil
	if c.root.Report == nil {
		c.root.Report = notify.NewReport("task")
	} else {
		c.root.Report.Reset("task")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	hasFlags := c.cmd.Flags().Changed("sign") ||
		c.cmd.Flags().Changed("playids") ||
		c.cmd.Flags().Changed("musician-sign") ||
		c.cmd.Flags().Changed("musician-vip") ||
		c.cmd.Flags().Changed("note") ||
		c.cmd.Flags().Changed("fansgroup") ||
		c.cmd.Flags().Changed("daily-song-share") ||
		c.cmd.Flags().Changed("vip-member-gift") ||
		c.cmd.Flags().Changed("music-partner")

	var runSign, runPlayIds, runMusicianSign, runMusicianVip, runNote, runFansGroup, runDailySongShare, runVipMemberGift, runMusicPartner bool
	cfg := c.root.Cfg

	if hasFlags {
		runSign = c.opts.Sign
		runPlayIds = c.opts.PlayIds
		runMusicianSign = c.opts.MusicianSign
		runMusicianVip = c.opts.MusicianVip
		runNote = c.opts.Note
		runFansGroup = c.opts.FansGroup
		runDailySongShare = c.opts.DailySongShare
		runVipMemberGift = c.opts.VipMemberGift
		runMusicPartner = c.opts.MusicPartner
	} else {
		if cfg.Task != nil {
			runSign = cfg.Task.Sign
			runPlayIds = cfg.Task.PlayIds
			runMusicianSign = cfg.Task.MusicianSign
			runMusicianVip = cfg.Task.MusicianVip
			runNote = cfg.Task.Note
			runFansGroup = cfg.Task.FansGroup
			runDailySongShare = cfg.Task.DailySongShare
			runVipMemberGift = cfg.Task.VipMemberGift
			runMusicPartner = cfg.Task.MusicPartner
		} else {
			c.cmd.Println("[task] 提示: 配置文件中未定义 task 节点且未传递 any 命令行标志，默认不执行任何任务")
			return nil
		}
	}

	if !runSign && !runPlayIds && !runMusicianSign && !runMusicianVip && !runNote && !runFansGroup && !runDailySongShare && !runVipMemberGift && !runMusicPartner {
		c.cmd.Println("[task] 没有需要执行的任务")
		return nil
	}

	activeTasks := make(map[string]bool)
	activeTasks["music-partner"] = runMusicPartner
	if runSign {
		activeTasks["sign"] = true
	}
	if runPlayIds {
		activeTasks["playids"] = true
	}
	if runMusicianSign {
		activeTasks["musician-sign"] = true
	}
	if runMusicianVip {
		activeTasks["musician-vip"] = true
	}
	if runNote {
		activeTasks["note"] = true
	}
	if runFansGroup {
		activeTasks["fansgroup"] = true
	}
	if runDailySongShare {
		activeTasks["daily-song-share"] = true
	}
	if runVipMemberGift {
		activeTasks["vip-member-gift"] = true
	}

	accounts := c.getAccounts()
	if len(accounts) == 0 {
		if runMusicPartner {
			return fmt.Errorf("音乐合伙人评分未配置任何账号，请先添加账号")
		}
		c.cmd.Println("[task] 未配置任何账号，请检查 config.yaml")
		return nil
	}

	runFast := !c.opts.OnlySlow
	runSlow := !c.opts.OnlyFast

	mode := "by-task-group"
	if cfg.Task != nil && cfg.Task.Mode != "" {
		mode = cfg.Task.Mode
	}

	var fastQueue, slowQueue []string
	if cfg.Task != nil {
		fastQueue = cfg.Task.FastTasks
		slowQueue = cfg.Task.SlowTasks
	}
	// 旧配置保留自定义顺序时也可启用新任务，不要求用户手动编辑队列。
	if runMusicPartner && !hasMusicPartnerAction(fastQueue) && !hasMusicPartnerAction(slowQueue) {
		slowQueue = append(append([]string(nil), slowQueue...), "music-partner")
	}
	if runMusicPartner && ((runFast && hasMusicPartnerAction(fastQueue)) || (runSlow && hasMusicPartnerAction(slowQueue))) {
		selected := false
		for _, account := range accounts {
			if c.isActionEnabledForAccount("music-partner", account.IsMain) {
				selected = true
				break
			}
		}
		if !selected {
			c.musicPartnerErr = fmt.Errorf("音乐合伙人未选择可执行账号，请启用已配置的主账号或辅助账号")
			c.reportSkip("-", "music-partner", c.musicPartnerErr.Error())
		}
	}
	// 整次 task 进程共享：同一账号 Cookie 失效后，快/慢任务组均不再继续执行该账号
	invalidSessions := make(map[string]bool)
	// 快慢任务组共享账号收益，避免同一账号重复查询，并等待最后一个签到任务组再汇总云贝。
	earningsReports := make(map[string]*accountEarningsReport)
	slowHasSignTasks := runSlow && c.queueHasActiveSignTasks(slowQueue, activeTasks)

	if mode == "by-task-group" {
		if runFast {
			c.cmd.Println("[task] >>>>>> 开始执行 [快任务组] (跨账号串行) <<<<<<")
			c.runQueue(ctx, fastQueue, accounts, activeTasks, invalidSessions, earningsReports, !slowHasSignTasks)
			if err := ctx.Err(); err != nil {
				return err
			}
			c.cmd.Printf("[task] >>>>>> [快任务组] 执行完毕 <<<<<<\n\n")
		}

		if runSlow {
			c.cmd.Println("[task] >>>>>> 开始执行 [慢任务组] (跨账号串行) <<<<<<")
			c.runQueue(ctx, slowQueue, accounts, activeTasks, invalidSessions, earningsReports, true)
			if err := ctx.Err(); err != nil {
				return err
			}
			c.cmd.Printf("[task] >>>>>> [慢任务组] 执行完毕 <<<<<<\n\n")
		}
	} else if mode == "by-account" {
		for i, account := range accounts {
			if ctx.Err() != nil {
				break
			}
			c.cmd.Printf("[task] >>>>>> 开始执行账号 (%s) <<<<<<\n", account.Filepath)
			var hasExecuted bool
			if runFast {
				c.cmd.Println("  --- [快任务组] ---")
				if c.runQueue(ctx, fastQueue, []Account{account}, activeTasks, invalidSessions, earningsReports, !slowHasSignTasks) {
					hasExecuted = true
				}
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if runSlow {
				c.cmd.Println("  --- [慢任务组] ---")
				if c.runQueue(ctx, slowQueue, []Account{account}, activeTasks, invalidSessions, earningsReports, true) {
					hasExecuted = true
				}
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			c.cmd.Printf("[task] >>>>>> 账号 (%s) 执行完毕 <<<<<<\n\n", account.Filepath)
			if hasExecuted && i < len(accounts)-1 {
				c.sleepBetweenAccounts(ctx, account.Filepath)
			}
		}
	} else {
		return fmt.Errorf("未知的任务执行模式: %s，仅支持 by-task-group 和 by-account", mode)
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	return c.finishRun()
}

func (c *Task) finishRun() error {
	failures, skips, unconfirmed := c.root.Report.OutcomeCounts()
	if failures > 0 || skips > 0 || unconfirmed > 0 {
		c.cmd.Printf("[task] 批量任务执行结束：%d 项任务失败，%d 项跳过，%d 项未确认；其余可执行任务已继续处理。\n", failures, skips, unconfirmed)
		for _, item := range c.root.Report.Items() {
			label := "跳过"
			if item.Kind == notify.KindFailure {
				label = "失败"
			}
			if item.Kind == notify.KindUnconfirmed {
				label = "未确认"
			}
			c.cmd.Printf("[task] %s | 账号 (%s) [%s]：%s\n", label, item.Account, item.Task, item.Message)
		}
	}
	if failures > 0 || unconfirmed > 0 {
		batchErr := errors.Join(c.failures...)
		if c.musicPartnerErr != nil {
			batchErr = errors.Join(batchErr, c.musicPartnerErr)
		}
		if batchErr != nil {
			return fmt.Errorf("批量任务执行结束：%d 项任务失败，%d 项未确认: %w", failures, unconfirmed, batchErr)
		}
		return fmt.Errorf("批量任务执行结束：%d 项任务失败，%d 项未确认，请查看子任务汇总", failures, unconfirmed)
	}
	if c.musicPartnerErr != nil {
		return c.musicPartnerErr
	}
	if skips > 0 {
		return nil
	}
	c.cmd.Println("[task] 所有可执行任务已处理，未记录失败、跳过或未确认项。")
	return nil
}

func hasMusicPartnerAction(queue []string) bool {
	for _, action := range queue {
		if strings.EqualFold(action, "music-partner") {
			return true
		}
	}
	return false
}

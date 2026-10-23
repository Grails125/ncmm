// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

package ncmm

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/pkg/database"
	"github.com/3899/ncmm/pkg/log"

	"github.com/spf13/cobra"
)

type Musician struct {
	root      *Root
	cmd       *cobra.Command
	l         *log.Logger
	rng       *rand.Rand
	newClient func(*api.Config, *log.Logger) (*api.Client, error)
}

func NewMusician(root *Root, l *log.Logger) *Musician {
	c := &Musician{
		root:      root,
		l:         l,
		rng:       rand.New(rand.NewSource(time.Now().UnixNano())),
		newClient: api.NewClient,
		cmd: &cobra.Command{
			Use:     "musician",
			Short:   "[need login] Auto-complete musician daily check-ins and VIP tasks",
			Example: "  ncmm musician\n  ncmm musician-sign\n  ncmm musician-vip",
		},
	}
	c.cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return c.execute(cmd.Context())
	}
	return c
}

func (c *Musician) validate() error {
	if c.root.Cfg.Musician == nil {
		return fmt.Errorf("musician config is not set in config.yaml")
	}
	return nil
}

func (c *Musician) Add(command ...*cobra.Command) {
	c.cmd.AddCommand(command...)
}

func (c *Musician) Command() *cobra.Command {
	return c.cmd
}

// SignCommand 返回顶级命令 ncmm musician-sign
func (c *Musician) SignCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "musician-sign",
		Short:   "[need login] Execute musician daily sign-in and claim cloud beans (daily task)",
		Example: "  ncmm musician-sign",
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.executeSign(cmd.Context())
		},
	}
}

// VipCommand 返回顶级命令 ncmm musician-vip
func (c *Musician) VipCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "musician-vip",
		Short:   "[need login] Execute musician VIP advanced tasks: publish notes & relay play (monthly task)",
		Example: "  ncmm musician-vip",
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.executeVip(cmd.Context())
		},
	}
}

// ==================== 身份缓存逻辑 ====================

// musicianIdentityCacheKey 返回身份缓存的 badger key
func musicianIdentityCacheKey(userID int64) string {
	if userID <= 0 {
		return ""
	}
	return fmt.Sprintf("musician:identity:uid:%d", userID)
}

func musicianRewardClaimCacheKey(cookieFile string, userMissionId, period int64) string {
	return fmt.Sprintf("musician:reward:%s:%d:%d", cookieFile, userMissionId, period)
}

// checkMusicianIdentityCache 检查本地缓存的音乐人身份状态
// 返回: (isMusician, cacheHit, error)
func (c *Musician) checkMusicianIdentityCache(ctx context.Context, db database.Database, userID int64) (bool, bool, error) {
	key := musicianIdentityCacheKey(userID)
	if db == nil || key == "" {
		return false, false, nil
	}
	cacheDays := c.root.Cfg.Musician.IdentityCacheDays

	// -1 = 关闭缓存，始终走 API
	if cacheDays != nil && *cacheDays == -1 {
		return false, false, nil
	}

	cached, err := db.Get(ctx, key)
	if err != nil {
		// 缓存未命中（key not found 或其他错误）
		return false, false, nil
	}

	// 缓存命中
	return cached == "1", true, nil
}

// saveMusicianIdentityCache 保存音乐人身份状态到本地缓存
func (c *Musician) saveMusicianIdentityCache(ctx context.Context, db database.Database, userID int64, isMusician bool) {
	key := musicianIdentityCacheKey(userID)
	if db == nil || key == "" {
		return
	}
	cacheDays := c.root.Cfg.Musician.IdentityCacheDays
	if cacheDays != nil && *cacheDays == -1 {
		return // 缓存已关闭
	}

	value := "0"
	if isMusician {
		value = "1"
	}

	if cacheDays != nil && *cacheDays > 0 {
		ttl := time.Duration(*cacheDays) * 24 * time.Hour
		if err := db.Set(ctx, key, value, ttl); err != nil {
			log.Warn("[musician] 写入身份缓存失败: %s", err)
		}
	} else {
		// 0 = 永久有效
		if err := db.Set(ctx, key, value); err != nil {
			log.Warn("[musician] 写入身份缓存失败: %s", err)
		}
	}
}

// ==================== 公共初始化 ====================

// musicianContext 保存单次执行所需的公共上下文
type musicianContext struct {
	userID   int64
	cli      *api.Client
	eapiCli  *eapi.Api
	weapiCli *weapi.Api
	db       database.Database
	resp     *eapi.MusicianVipTasksResp // 可能为 nil（当缓存命中且仅执行 sign 时）
}

// initMusicianContext 初始化客户端并检查音乐人身份（优先读缓存）
// needVipData: true 表示需要 VIP 任务进度数据（vip 子命令或完整执行时需要）
func (c *Musician) initMusicianContext(ctx context.Context, cookieFile string, needVipData bool) (*musicianContext, error) {
	absPath, err := filepath.Abs(cookieFile)
	if err != nil {
		return nil, fmt.Errorf("解析 cookie 路径失败: %w", err)
	}

	// 初始化网络客户端
	networkCfg := *c.root.Cfg.Network
	networkCfg.Cookie.Filepath = absPath
	// 领取等写请求响应丢失时不能自动重提，下次执行先查询官方状态。
	networkCfg.Retry = 0
	cli, err := c.newClient(&networkCfg, c.l)
	if err != nil {
		return nil, fmt.Errorf("实例化客户端失败: %w", err)
	}

	mctx := &musicianContext{
		cli:      cli,
		eapiCli:  eapi.New(cli),
		weapiCli: weapi.New(cli),
	}
	// 先确认当前会话的真实 UID；路径和登录元数据不能代表账号身份。
	mctx.userID, err = c.resolveMusicianUserID(ctx, mctx.weapiCli)
	if err != nil {
		mctx.close(ctx)
		return nil, err
	}
	// 数据库不可用时仍可直接查询官方状态，不借用其他账号的记录。
	db, err := database.NewWithOptions(c.root.Cfg.Database, 1, 0, true)
	if err != nil {
		c.cmd.Println("    ⚠️  本地数据库已被其他进程占用，已自动降级为无缓存模式直接执行")
		db = nil
	}
	mctx.db = db

	// 检查身份缓存
	isMusician, cacheHit, _ := c.checkMusicianIdentityCache(ctx, db, mctx.userID)

	if cacheHit && !isMusician {
		// 缓存命中且非音乐人 → 直接返回错误
		mctx.close(ctx)
		return nil, fmt.Errorf("当前账号不是音乐人 (来自身份缓存)")
	}

	if cacheHit && !needVipData {
		// 当前 UID 已验证；仅签到时可复用其身份缓存，跳过音乐人身份接口。
		c.cmd.Println("  ✅ 已认证音乐人 (来自身份缓存)")
		return mctx, nil
	}

	if !needVipData {
		// 日常签到只需要确认音乐人身份，使用 HAR 中的移动端 EAPI 身份接口。
		c.cmd.Println("  👉 检查音乐人身份...")
		role, err := mctx.eapiCli.MusicianRoleGet(ctx, &eapi.MusicianRoleGetReq{})
		if err != nil {
			mctx.close(ctx)
			return nil, fmt.Errorf("MusicianRoleGet: %w", err)
		}
		if role.Code != 200 {
			mctx.close(ctx)
			return nil, fmt.Errorf("MusicianRoleGet error: code=%d msg=%s", role.Code, role.Message)
		}

		if role.Data.UserId != 0 && role.Data.UserId != mctx.userID {
			log.Warn("[musician] 身份响应 UID 与登录会话不一致: account=%d role=%d", mctx.userID, role.Data.UserId)
			mctx.close(ctx)
			return nil, fmt.Errorf("音乐人身份与当前登录账号不一致，请重新登录后重试")
		}
		c.saveMusicianIdentityCache(ctx, db, mctx.userID, role.Data.IsMusician)
		if !role.Data.IsMusician {
			mctx.close(ctx)
			return nil, fmt.Errorf("当前账号不是音乐人")
		}

		c.cmd.Printf("  ✅ 已认证音乐人 | 用户ID: %d | 音乐人ID: %d\n", role.Data.UserId, role.Data.ArtistId)
		return mctx, nil
	}

	// 需要 VIP 数据 → 调用 VIP 权益任务 API。
	c.cmd.Println("  👉 检查任务状态...")
	resp, err := mctx.eapiCli.MusicianVipTasks(ctx, &eapi.MusicianVipTasksReq{ER: false})
	if err != nil {
		mctx.close(ctx)
		return nil, fmt.Errorf("MusicianVipTasks: %w", err)
	}
	if resp.Code != 200 {
		mctx.close(ctx)
		return nil, fmt.Errorf("MusicianVipTasks error: code=%d msg=%s", resp.Code, resp.Message)
	}

	// 写入身份缓存
	c.saveMusicianIdentityCache(ctx, db, mctx.userID, resp.Data.IsMusician)

	if !resp.Data.IsMusician {
		mctx.close(ctx)
		return nil, fmt.Errorf("当前账号不是音乐人")
	}

	c.cmd.Printf("  ✅ 已认证音乐人 | 维持天数: %d 天 | 近30天播放: %d 次 | 解锁VIP权益: %v\n",
		resp.Data.MaintainDays, resp.Data.RecentPlayCount30, resp.Data.UnlockVipRight)

	mctx.resp = resp
	return mctx, nil
}

func (c *Musician) resolveMusicianUserID(ctx context.Context, client *weapi.Api) (int64, error) {
	if client == nil {
		log.Warn("[musician] 缺少用于验证账号的登录会话")
		return 0, fmt.Errorf("无法验证当前登录账号，请检查 Cookie 后重试")
	}
	user, err := client.GetUserInfo(ctx, &weapi.GetUserInfoReq{})
	if err != nil {
		log.Warn("[musician] 验证登录账号失败: %s", err)
		return 0, fmt.Errorf("无法验证当前登录账号，请检查 Cookie 后重试")
	}
	if user == nil || user.Code != 200 || user.Account == nil || user.Profile == nil || user.Account.Id <= 0 {
		log.Warn("[musician] 登录响应未包含有效账号身份")
		return 0, fmt.Errorf("无法验证当前登录账号，请检查 Cookie 后重试")
	}
	if user.Profile.UserId > 0 && user.Profile.UserId != user.Account.Id {
		log.Warn("[musician] 登录响应 UID 不一致: account=%d profile=%d", user.Account.Id, user.Profile.UserId)
		return 0, fmt.Errorf("无法验证当前登录账号，请重新登录后重试")
	}
	return user.Account.Id, nil
}

func (mc *musicianContext) close(ctx context.Context) {
	if mc.db != nil {
		mc.db.Close(ctx)
	}
	if mc.cli != nil {
		mc.cli.Close(ctx)
	}
}

// ==================== execute: 向后兼容，执行 sign + vip ====================

func (c *Musician) execute(ctx context.Context) error {
	if err := c.validate(); err != nil {
		err = fmt.Errorf("validate: %w", err)
		c.root.ReportFailure("-", "musician", err)
		return err
	}

	cfg := c.root.Cfg
	if cfg.Accounts == nil {
		err := fmt.Errorf("配置文件中缺少 accounts 账号节点")
		c.root.ReportFailure("-", "musician", err)
		return err
	}

	var activeAccounts []struct {
		Filepath string
		IsMain   bool
	}
	if cfg.Musician != nil && cfg.Musician.EnableMain && cfg.Accounts.Main != "" {
		activeAccounts = append(activeAccounts, struct {
			Filepath string
			IsMain   bool
		}{cfg.Accounts.Main, true})
	} else {
		if cfg.Musician != nil && !cfg.Musician.EnableMain {
			c.cmd.Println("[musician] 提示: 主账号音乐人任务已在配置文件中关闭 (enableMain = false)")
		} else if cfg.Accounts == nil || cfg.Accounts.Main == "" {
			c.cmd.Println("[musician] 提示: 主账号音乐人任务未执行，因为未配置主账号 (accounts.main)")
		}
	}

	if cfg.Musician != nil && cfg.Musician.EnableSecondaries && len(cfg.Accounts.Secondary) > 0 {
		for _, secCookie := range cfg.Accounts.Secondary {
			if secCookie != "" {
				activeAccounts = append(activeAccounts, struct {
					Filepath string
					IsMain   bool
				}{secCookie, false})
			}
		}
	} else {
		if cfg.Musician != nil && !cfg.Musician.EnableSecondaries {
			c.cmd.Println("[musician] 提示: 辅助账号音乐人任务已在配置文件中关闭 (enableSecondaries = false)")
		} else if cfg.Accounts == nil || len(cfg.Accounts.Secondary) == 0 {
			c.cmd.Println("[musician] 提示: 辅助账号音乐人任务未执行，因为未配置辅助账号 (accounts.secondary)")
		}
	}

	if len(activeAccounts) == 0 {
		c.cmd.Println("[musician] 未启用或未配置任何账号进行音乐人任务，请检查 config.yaml")
		c.root.ReportSkip("-", "musician", "未启用或未配置任何账号")
		return nil
	}

	var failures []error
	for i, acc := range activeAccounts {
		if ctx.Err() != nil {
			failures = append(failures, ctx.Err())
			break
		}
		if acc.IsMain {
			c.cmd.Printf("[musician] >>>>>> 开始主账号音乐人任务 (%s) <<<<<<\n", acc.Filepath)
			if err := c.runMusicianForCookie(ctx, acc.Filepath, true); err != nil {
				c.cmd.Printf("[musician] ❌ 主账号任务失败: %s\n", err)
				c.root.ReportFailure(acc.Filepath, "musician", err)
				failures = append(failures, err)
			}
		} else {
			c.cmd.Printf("[musician] >>>>>> 开始辅助账号音乐人任务 (%s) <<<<<<\n", acc.Filepath)
			if err := c.runMusicianForCookie(ctx, acc.Filepath, false); err != nil {
				c.cmd.Printf("[musician] ❌ 辅助账号任务失败: %s\n", err)
				c.root.ReportFailure(acc.Filepath, "musician", err)
				failures = append(failures, err)
			}
		}

		if i < len(activeAccounts)-1 {
			c.sleepBetweenAccounts(ctx, acc.Filepath)
		}
	}

	c.cmd.Println("[musician] 所有音乐人日常与 VIP 任务执行完毕！")
	return errors.Join(failures...)
}

func (c *Musician) sleepBetweenAccounts(ctx context.Context, currentAccount string) {
	sleepSec := 5 + c.rng.Intn(16) // 5 ~ 20 秒
	c.cmd.Printf("[musician] ⏳ 账号 (%s) 任务处理完毕，为规避风控，随机等待 %d 秒后继续下一个账号...\n", currentAccount, sleepSec)
	select {
	case <-ctx.Done():
	case <-time.After(time.Duration(sleepSec) * time.Second):
	}
}

// runMusicianForCookie 执行单个账号的完整音乐人任务（sign + vip），单次 API 调用
func (c *Musician) runMusicianForCookie(ctx context.Context, cookieFile string, isPrimary bool) error {
	mctx, err := c.initMusicianContext(ctx, cookieFile, true)
	if err != nil {
		return err
	}
	defer mctx.close(ctx)

	// 第一阶段：音乐人日常任务
	signErr := c.doSignPhase(ctx, mctx, cookieFile)

	// 第二阶段：音乐人 VIP 任务
	c.doVipPhase(ctx, mctx, cookieFile)

	// 第三阶段：音乐人 VIP 领取
	c.doVipClaimPhase(ctx, mctx, cookieFile)

	// 统计接口每天 09:00 后最多请求一次；历史缺口才调用趋势接口回补。
	c.syncAndPrintPlayStats(ctx, mctx, cookieFile)

	return signErr
}

// ==================== executeSign: 仅日常签到 ====================

func (c *Musician) executeSign(ctx context.Context) error {
	if err := c.validate(); err != nil {
		err = fmt.Errorf("validate: %w", err)
		c.root.ReportFailure("-", "musician-sign", err)
		return err
	}

	cfg := c.root.Cfg
	if cfg.Accounts == nil {
		err := fmt.Errorf("配置文件中缺少 accounts 账号节点")
		c.root.ReportFailure("-", "musician-sign", err)
		return err
	}

	var hasExecuted bool
	var failures []error

	// 1. 主账号
	if cfg.Musician != nil && cfg.Musician.EnableMain && cfg.Accounts.Main != "" {
		c.cmd.Printf("[musician sign] >>>>>> 开始主账号音乐人日常签到 (%s) <<<<<<\n", cfg.Accounts.Main)
		if err := c.RunSignForCookie(ctx, cfg.Accounts.Main); err != nil {
			c.cmd.Printf("[musician sign] ❌ 主账号签到失败: %s\n", err)
			c.root.ReportFailure(cfg.Accounts.Main, "musician-sign", err)
			failures = append(failures, err)
		}
		hasExecuted = true
	} else {
		if cfg.Musician != nil && !cfg.Musician.EnableMain {
			c.cmd.Println("[musician sign] 提示: 主账号音乐人任务已在配置文件中关闭 (enableMain = false)")
		} else if cfg.Accounts == nil || cfg.Accounts.Main == "" {
			c.cmd.Println("[musician sign] 提示: 主账号音乐人任务未执行，因为未配置主账号 (accounts.main)")
		}
	}

	// 2. 辅助账号
	if cfg.Musician != nil && cfg.Musician.EnableSecondaries && len(cfg.Accounts.Secondary) > 0 {
		for _, secCookie := range cfg.Accounts.Secondary {
			if ctx.Err() != nil {
				failures = append(failures, ctx.Err())
				break
			}
			c.cmd.Printf("[musician sign] >>>>>> 开始辅助账号音乐人日常签到 (%s) <<<<<<\n", secCookie)
			if err := c.RunSignForCookie(ctx, secCookie); err != nil {
				c.cmd.Printf("[musician sign] ❌ 辅助账号签到失败: %s\n", err)
				c.root.ReportFailure(secCookie, "musician-sign", err)
				failures = append(failures, err)
			}
			hasExecuted = true
		}
	} else {
		if cfg.Musician != nil && !cfg.Musician.EnableSecondaries {
			c.cmd.Println("[musician sign] 提示: 辅助账号音乐人任务已在配置文件中关闭 (enableSecondaries = false)")
		} else if cfg.Accounts == nil || len(cfg.Accounts.Secondary) == 0 {
			c.cmd.Println("[musician sign] 提示: 辅助账号音乐人任务未执行，因为未配置辅助账号 (accounts.secondary)")
		}
	}

	if !hasExecuted {
		c.cmd.Println("[musician sign] 未启用或未配置任何账号进行音乐人签到，请检查 config.yaml")
		c.root.ReportSkip("-", "musician-sign", "未启用或未配置任何账号")
	} else {
		c.cmd.Println("[musician sign] 所有音乐人日常签到任务执行完毕！")
	}
	return errors.Join(failures...)
}

// RunSignForCookie 执行单个账号的音乐人日常签到
func (c *Musician) RunSignForCookie(ctx context.Context, cookieFile string) error {
	mctx, err := c.initMusicianContext(ctx, cookieFile, false)
	if err != nil {
		return err
	}
	defer mctx.close(ctx)

	signErr := c.doSignPhase(ctx, mctx, cookieFile)
	c.syncAndPrintPlayStats(ctx, mctx, cookieFile)
	return signErr
}

// ==================== executeVip: 仅 VIP 进阶任务 ====================

func (c *Musician) executeVip(ctx context.Context) error {
	if err := c.validate(); err != nil {
		err = fmt.Errorf("validate: %w", err)
		c.root.ReportFailure("-", "musician-vip", err)
		return err
	}

	cfg := c.root.Cfg
	if cfg.Accounts == nil {
		err := fmt.Errorf("配置文件中缺少 accounts 账号节点")
		c.root.ReportFailure("-", "musician-vip", err)
		return err
	}

	var hasExecuted bool

	// 1. 主账号
	if cfg.Musician != nil && cfg.Musician.EnableMain && cfg.Accounts.Main != "" {
		c.cmd.Printf("[musician vip] >>>>>> 开始主账号音乐人VIP进阶任务 (%s) <<<<<<\n", cfg.Accounts.Main)
		if err := c.RunVipForCookie(ctx, cfg.Accounts.Main); err != nil {
			c.cmd.Printf("[musician vip] ❌ 主账号VIP任务失败: %s\n", err)
			c.root.ReportFailure(cfg.Accounts.Main, "musician-vip", err)
		}
		hasExecuted = true
	} else {
		if cfg.Musician != nil && !cfg.Musician.EnableMain {
			c.cmd.Println("[musician vip] 提示: 主账号音乐人任务已在配置文件中关闭 (enableMain = false)")
		} else if cfg.Accounts == nil || cfg.Accounts.Main == "" {
			c.cmd.Println("[musician vip] 提示: 主账号音乐人任务未执行，因为未配置主账号 (accounts.main)")
		}
	}

	// 2. 辅助账号
	if cfg.Musician != nil && cfg.Musician.EnableSecondaries && len(cfg.Accounts.Secondary) > 0 {
		for _, secCookie := range cfg.Accounts.Secondary {
			c.cmd.Printf("[musician vip] >>>>>> 开始辅助账号音乐人VIP进阶任务 (%s) <<<<<<\n", secCookie)
			if err := c.RunVipForCookie(ctx, secCookie); err != nil {
				c.cmd.Printf("[musician vip] ❌ 辅助账号VIP任务失败: %s\n", err)
				c.root.ReportFailure(secCookie, "musician-vip", err)
			}
			hasExecuted = true
		}
	} else {
		if cfg.Musician != nil && !cfg.Musician.EnableSecondaries {
			c.cmd.Println("[musician vip] 提示: 辅助账号音乐人任务已在配置文件中关闭 (enableSecondaries = false)")
		} else if cfg.Accounts == nil || len(cfg.Accounts.Secondary) == 0 {
			c.cmd.Println("[musician vip] 提示: 辅助账号音乐人任务未执行，因为未配置辅助账号 (accounts.secondary)")
		}
	}

	if !hasExecuted {
		c.cmd.Println("[musician vip] 未启用或未配置任何账号进行音乐人VIP任务，请检查 config.yaml")
		c.root.ReportSkip("-", "musician-vip", "未启用或未配置任何账号")
	} else {
		c.cmd.Println("[musician vip] 所有音乐人VIP进阶任务执行完毕！")
	}
	return nil
}

// RunVipForCookie 执行单个账号的音乐人 VIP 进阶任务
func (c *Musician) RunVipForCookie(ctx context.Context, cookieFile string) error {
	mctx, err := c.initMusicianContext(ctx, cookieFile, true)
	if err != nil {
		return err
	}
	defer mctx.close(ctx)

	c.doVipPhase(ctx, mctx, cookieFile)
	c.doVipClaimPhase(ctx, mctx, cookieFile)
	c.syncAndPrintPlayStats(ctx, mctx, cookieFile)
	return nil
}

// ==================== 任务执行阶段 ====================

// doVipPhase 执行第二阶段：音乐人 VIP 进阶任务
func (c *Musician) doVipPhase(ctx context.Context, mctx *musicianContext, cookieFile string) {
	if mctx.resp == nil || mctx.resp.Data.FurtherTask == nil {
		c.cmd.Println("  👉 [第二阶段] 提示: 没有音乐人 VIP 进阶任务 (跳过)")
		return
	}

	c.cmd.Printf("  👉 [第二阶段] 开始执行音乐人 VIP 任务 (发布笔记与接力刷歌)... 进度: %d/%d 完成\n",
		mctx.resp.Data.FurtherTask.ProgressRate, mctx.resp.Data.FurtherTask.TotalCompleteNum)

	// 遍历子任务，检查并执行
	for _, sub := range mctx.resp.Data.FurtherTask.Children {
		if sub.MissionCode == musicianPlayPercentCode {
			c.cmd.Printf("    - 任务: %s — 状态: %d, 播放进度百分比: %d/%d\n", sub.Name, sub.MissionStatus, sub.ProgressRate, sub.TotalCompleteNum)
		} else {
			c.cmd.Printf("    - 任务: %-15s — 状态: %d, 进度: %d/%d\n", sub.Name, sub.MissionStatus, sub.ProgressRate, sub.TotalCompleteNum)
		}

		if sub.MissionStatus == 100 {
			c.cmd.Printf("    ✅ 任务已完成: %s\n", sub.Name)
			continue
		}

		switch sub.MissionCode {
		case "mission_code_musician_notebook_publish":
			if c.root.Cfg.Musician.EnableVipNote != nil && !*c.root.Cfg.Musician.EnableVipNote {
				c.cmd.Println("    ℹ️ 笔记任务已在配置中关闭 (enableVipNote = false)，跳过")
			} else if sub.ProgressRate >= sub.TotalCompleteNum {
				c.cmd.Println("    ℹ️ 笔记任务已完成，无需发布")
			} else {
				c.cmd.Println("    👉 处理笔记任务...")
				n := NewNote(c.root, c.l)
				_, err := n.ExecuteForCookie(ctx, cookieFile)
				if err != nil {
					log.Error("    ❌ 笔记任务执行失败: %s", err)
					c.cmd.Printf("    ❌ 笔记任务失败: %s\n", err)
				}
			}

		case musicianPlayCountCode, musicianPlayPercentCode:
			if c.root.Cfg.Musician.EnableVipPlay != nil && !*c.root.Cfg.Musician.EnableVipPlay {
				c.cmd.Println("    ℹ️ 播放任务已在配置中关闭 (enableVipPlay = false)，跳过")
			} else if err := c.handlePlayTask(ctx, mctx.db, sub, mctx.resp.Data.RecentPlayCount30); err != nil {
				log.Error("    ❌ 播放任务执行失败: %s", err)
				c.cmd.Printf("    ❌ 播放任务失败: %s\n", err)
			}

		default:
			c.cmd.Printf("    ⚠️ 未知任务类型: %s\n", sub.MissionCode)
		}
	}
}

// handlePlayTask 处理播放任务
func (c *Musician) handlePlayTask(ctx context.Context, db database.Database, sub eapi.MusicianVipSubTask, recentPlayCount30 int) error {
	c.cmd.Println("    👉 处理播放任务...")
	neededEffective, err := calcNeededPlayCount(sub)
	if err != nil {
		c.cmd.Printf("    ⚠️ 播放任务无法计算：类型=%s, 状态=%d, 进度=%d/%d, 原因=%s\n", sub.MissionCode, sub.MissionStatus, sub.ProgressRate, sub.TotalCompleteNum, err)
		return err
	}
	c.cmd.Printf("    - 本期预计尚缺有效播放: %d 次；近30天历史总播放: %d 次（不参与本期计算）\n", neededEffective, recentPlayCount30)
	if neededEffective == 0 {
		if sub.MissionStatus == 100 {
			c.cmd.Println("    ℹ️ 官方播放任务已完成，无需播放")
		} else {
			c.cmd.Println("    ℹ️ 官方播放进度已达到目标，等待任务状态更新")
		}
		return nil
	}

	cfg := c.root.Cfg.Musician.Play
	rootPlayCfg := c.root.Cfg.PlayIds

	// 歌单保留为来源，在每个实际听歌账号登录后读取，不能复用其他账号的访问结果。
	songIDs, playlistSources := musicianPlaySources(cfg, rootPlayCfg)
	if len(songIDs) == 0 && len(playlistSources) == 0 {
		return fmt.Errorf("没有配置有效歌曲或歌单，请在 musician.play 配置 ids / idsFile，或在 playids 配置歌曲及 playlistIds")
	}

	// 2. 确定播放时间间隔与每日目标上限参数
	dailyMin := cfg.DailyMin
	if dailyMin <= 0 && rootPlayCfg != nil {
		dailyMin = rootPlayCfg.DailyMin
	}

	dailyMax := cfg.DailyMax
	if dailyMax <= 0 && rootPlayCfg != nil {
		dailyMax = rootPlayCfg.DailyMax
	}

	gapMin := cfg.GapMin
	if gapMin <= 0 && rootPlayCfg != nil {
		gapMin = rootPlayCfg.GapMin
	}
	if gapMin <= 0 {
		gapMin = 5
	}

	gapMax := cfg.GapMax
	if gapMax <= 0 && rootPlayCfg != nil {
		gapMax = rootPlayCfg.GapMax
	}
	if gapMax <= 0 {
		gapMax = 20
	}

	// 4. 获取执行刷歌的辅助账号池 (secondary)
	var playAccounts []string
	if c.root.Cfg.Accounts != nil && len(c.root.Cfg.Accounts.Secondary) > 0 {
		playAccounts = c.root.Cfg.Accounts.Secondary
	} else if c.root.Cfg.Accounts != nil && c.root.Cfg.Accounts.Main != "" {
		c.cmd.Println("    [WARN] 未配置辅助账号池 (accounts.secondary)，将直接使用主账号自己为自己刷播")
		playAccounts = []string{c.root.Cfg.Accounts.Main}
	} else {
		return fmt.Errorf("未配置任何账号，请在 config.yaml 的 accounts 节点下配置 primary 或 secondary")
	}

	// 5. 循环分配与上限回滚重平衡算法
	var currentSecondaryIndex = 0

	for neededEffective > 0 && currentSecondaryIndex < len(playAccounts) {
		currentAccount := playAccounts[currentSecondaryIndex]

		// 根据干扰比例 R 计算当前所需总播放数 T = N / 0.7
		ratio := 0.3
		if c.root.Cfg.MixPlay != nil && c.root.Cfg.MixPlay.Enabled {
			ratio = c.root.Cfg.MixPlay.DailyRecommendRatio
		}
		if ratio >= 1.0 {
			ratio = 0.5
		}
		neededTotal := int64(float64(neededEffective) / (1.0 - ratio))
		if neededTotal < neededEffective {
			neededTotal = neededEffective
		}

		// 确立此次刷歌数量 (支持随机与继承关系)
		var runTarget int64
		runMin := cfg.RunMin
		runMax := cfg.RunMax
		if runMin == 0 && runMax == 0 && rootPlayCfg != nil {
			runMin = rootPlayCfg.RunMin
			runMax = rootPlayCfg.RunMax
		}

		if runMin == 0 && runMax == 0 {
			runTarget = neededTotal
		} else {
			r := rand.New(rand.NewSource(time.Now().UnixNano()))
			if runMax > runMin {
				runTarget = runMin + r.Int63n(runMax-runMin+1)
			} else {
				runTarget = runMin
			}
			if runTarget > neededTotal {
				runTarget = neededTotal
			}
		}

		c.cmd.Printf("    ⏳ 分摊任务开始: 选用账号 (%s), 本次需刷总数(含日推): %d 首, 尚缺主歌有效数: %d 首\n", currentAccount, runTarget, neededEffective)

		// 实例化 PlayIds 服务并传入特定分摊参数；复用音乐人已打开的 DB，避免 Badger 目录锁冲突
		p := NewPlayIds(c.root, c.l)
		p.sharedDB = db
		p.opts = PlayIdsOpts{
			Ids:        cfg.IDs,
			IdsFile:    "",
			DailyMin:   dailyMin,
			DailyMax:   dailyMax,
			RunMin:     runTarget,
			RunMax:     runTarget,
			GapMin:     gapMin,
			GapMax:     gapMax,
			CookieFile: currentAccount,
		}

		// 执行单账号播放打卡，返回该账号实际成功播放的"主歌"数量
		effectivePlayed, err := p.executeForCookie(ctx, currentAccount, songIDs, playlistSources...)
		if err != nil {
			c.cmd.Printf("    [WARN] 账号 (%s) 运行异常: %s，正准备交由下一辅助号...\n", currentAccount, err)
		} else {
			c.cmd.Printf("    ✅ 账号 (%s) 播放完成，主歌成功上报数: %d 次（有效播放以官方统计为准）\n", currentAccount, effectivePlayed)
			neededEffective -= effectivePlayed
		}

		// 切换到下一个辅助号
		currentSecondaryIndex++
	}

	if neededEffective <= 0 {
		c.cmd.Println("    ✅ 本轮预计播放上报量已完成，任务是否达标以官方进度更新为准")
	} else {
		c.cmd.Printf("    ⚠️ 本轮辅助账号执行结束，预计仍缺有效播放 %d 次；请以官方任务进度为准\n", neededEffective)
	}

	return nil
}

// 音乐人专属歌曲源优先；留空时继承普通播放的歌曲、文件和歌单。
func musicianPlaySources(cfg config.MusicianPlayConf, rootPlayCfg *config.PlayIdsConfig) ([]string, []string) {
	var rawIds []string
	var playlistSources []string

	if cfg.IDs != "" || len(cfg.IDsFile) > 0 {
		if cfg.IDs != "" {
			rawIds = append(rawIds, parsePlaySongIDs(cfg.IDs)...)
		}
		for _, file := range uniqueStrings(cfg.IDsFile) {
			if file != "" {
				fileIds, err := parseIdsFromFile(file)
				if err == nil {
					rawIds = append(rawIds, fileIds...)
				} else {
					log.Warn("[musician] 读取 VIP idsFile (%s) 失败: %s", file, err)
				}
			}
		}
	} else if rootPlayCfg != nil {
		if rootPlayCfg.IDs != "" {
			rawIds = append(rawIds, parsePlaySongIDs(rootPlayCfg.IDs)...)
		}
		for _, file := range uniqueStrings(rootPlayCfg.IDsFile) {
			if file != "" {
				fileIds, err := parseIdsFromFile(file)
				if err == nil {
					rawIds = append(rawIds, fileIds...)
				} else {
					log.Warn("[musician] 读取默认 idsFile (%s) 失败: %s", file, err)
				}
			}
		}
		for _, source := range rootPlayCfg.PlaylistIDs {
			playlistSources = append(playlistSources, playSourceTokens(source)...)
		}
	}

	uniqueIds := make([]string, 0)
	seen := make(map[string]bool)
	for _, id := range rawIds {
		if !seen[id] {
			seen[id] = true
			uniqueIds = append(uniqueIds, id)
		}
	}
	return uniqueIds, uniqueStrings(playlistSources)
}

// ==================== doVipClaimPhase: 音乐人 VIP 自动领取 ====================

func musicianVipClaimCacheKey(userID, cycle int64) string {
	if userID <= 0 || cycle <= 0 {
		return ""
	}
	return fmt.Sprintf("musician:vip:claim:uid:%d:%d", userID, cycle)
}

// doVipClaimPhase 执行第三阶段：音乐人 VIP 领取
func (c *Musician) doVipClaimPhase(ctx context.Context, mctx *musicianContext, cookieFile string) {
	if c.root.Cfg.Musician != nil && !c.root.Cfg.Musician.IsVipClaimEnabled() {
		c.cmd.Println("  👉 [第三阶段] 提示: 音乐人 VIP 自动领取已在配置中关闭 (enableVipClaim = false)")
		return
	}

	c.cmd.Println("  👉 [第三阶段] 检查并尝试领取本期音乐人黑胶 VIP...")
	if mctx.userID <= 0 {
		var err error
		mctx.userID, err = c.resolveMusicianUserID(ctx, mctx.weapiCli)
		if err != nil {
			c.cmd.Printf("    ⚠️ %s，已跳过 VIP 领取\n", err)
			return
		}
	}

	// 1. 查询最新的音乐人 VIP 权益与任务状态 (WEAPI)
	info, err := mctx.weapiCli.MusicianVipInfo(ctx, &weapi.MusicianVipInfoReq{})
	if err != nil {
		c.cmd.Printf("    ⚠️ 查询音乐人 VIP 领取状态失败: %v\n", err)
		return
	}
	if info.Code != 200 {
		c.cmd.Printf("    ⚠️ 查询音乐人 VIP 状态失败: code=%d\n", info.Code)
		return
	}

	data := info.Data
	nowMs := time.Now().UnixMilli()
	// 先读取官方周期，再查本期成功记录；旧版自然月缓存可能来自预约误判，不能沿用。
	cacheKey := musicianVipClaimCacheKey(mctx.userID, data.FurtherVipGetTime)
	if mctx.db != nil && cacheKey != "" {
		cached, err := mctx.db.Exists(ctx, cacheKey)
		if err != nil {
			c.cmd.Printf("    ⚠️ 读取音乐人 VIP 本期成功记录失败，将按官方状态判断: %v\n", err)
		} else if cached {
			c.cmd.Printf("    ✅ 本期音乐人黑胶 VIP 已领取 (成功记录，周期: %d)\n", data.FurtherVipGetTime)
			return
		}
	}

	// 2. 检查任务达成状态
	taskFinished := data.TaskStatus
	if data.FurtherTask != nil && data.FurtherTask.MissionStatus == 100 {
		taskFinished = true
	}

	var getTimeStr string
	if data.FurtherVipGetTime > 0 {
		getTimeStr = time.UnixMilli(data.FurtherVipGetTime).Format("2006-01-02 15:04:05")
	}

	if !taskFinished {
		c.cmd.Println("    ℹ️ 音乐人进阶任务（笔记/播放量）尚未全部达成，暂不可领取 VIP")
		return
	}

	// 3. 预约状态不等于已领取，只有实际成功的领取请求才能写入缓存。
	if data.FurtherVipGetTime > nowMs {
		c.cmd.Printf("    ℹ️ 进阶任务已全部达标！本期 VIP 将于 %s 开放领取，到达时间后由后续任务运行自动领取\n", getTimeStr)
		return
	}

	if !data.CanOpen {
		c.cmd.Println("    ℹ️ 当前账号暂不可领取 VIP (canOpen = false)，未记录领取成功")
		return
	}

	// 4. 检查 antiCheatToken
	antiCheatToken := c.root.Cfg.Accounts.AntiCheatTokenFor(cookieFile)
	if strings.TrimSpace(antiCheatToken) == "" {
		c.cmd.Println("    ⚠️ 当前已满足音乐人 VIP 领取条件，但 accounts.antiCheatTokens 中未配置该账号的 token，已跳过领取")
		return
	}
	if cacheKey == "" {
		c.cmd.Println("    ⚠️ 官方未返回有效领取周期，本次按可领取状态提交，成功后不写入周期缓存")
	}

	// 5. 调用 WEAPI vip/get 接口领取
	c.cmd.Println("    👉 正在提交领取音乐人黑胶 VIP 请求...")
	claimResp, err := mctx.weapiCli.MusicianVipGet(ctx, &weapi.MusicianVipGetReq{
		CheckToken: antiCheatToken,
	}, antiCheatToken)
	if err != nil {
		c.cmd.Printf("    ❌ 领取音乐人黑胶 VIP 请求异常: %v\n", err)
		return
	}
	if claimResp.Code == 200 && claimResp.Data {
		c.cmd.Println("    🎉 成功领取本期音乐人黑胶 VIP！")
		if mctx.db != nil && cacheKey != "" {
			if err := mctx.db.Set(ctx, cacheKey, "1", 35*24*time.Hour); err != nil {
				c.cmd.Printf("    ⚠️ 领取已成功，但保存本期成功记录失败: %v\n", err)
			}
		}
	} else {
		c.cmd.Printf("    ❌ 领取音乐人黑胶 VIP 未确认成功: code=%d result=%t，未保存成功记录\n", claimResp.Code, claimResp.Data)
	}
}

// Copyright (c) 2025 ACAne0320.
// Music partner scoring behavior is adapted from the MIT-licensed ACAne0320/ncmp.
// See THIRD_PARTY_NOTICES.md for the upstream revision and license.

package ncmm

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/pkg/log"
	"github.com/spf13/cobra"
)

// musicPartnerAPI 只包含评分流程使用的接口，测试不会连接真实账号。
type musicPartnerAPI interface {
	PartnerDailyTask(context.Context, *weapi.PartnerTaskReq) (*weapi.PartnerTaskResp, error)
	PartnerExtraTask(context.Context, *weapi.PartnerExtraTaskReq) (*weapi.PartnerExtraTaskResp, error)
	PartnerEvaluate(context.Context, *weapi.PartnerEvaluateReq) (*weapi.PartnerEvaluateResp, error)
	PartnerExtraReport(context.Context, *weapi.PartnerExtraReportReq) (*weapi.PartnerExtraReportResp, error)
}

type MusicPartner struct {
	root       *Root
	cmd        *cobra.Command
	l          *log.Logger
	cookieFile string
	wait       func(context.Context, time.Duration) error
	newClient  func(*api.Config, *log.Logger) (*api.Client, error)
}

func NewMusicPartner(root *Root, l *log.Logger) *MusicPartner {
	if l == nil {
		l = log.Default
	}
	c := &MusicPartner{root: root, l: l, wait: musicPartnerWait, newClient: api.NewClient}
	c.cmd = &cobra.Command{
		Use:     "music-partner",
		Short:   "音乐合伙人每日及额外作品自动评分",
		Example: "  ncmm music-partner --cookie-file /data/cookie.json",
		Args:    cobra.NoArgs,
		RunE:    func(cmd *cobra.Command, _ []string) error { return c.execute(cmd.Context()) },
	}
	c.cmd.Flags().StringVar(&c.cookieFile, "cookie-file", "", "指定账号的 Cookie 文件")
	return c
}

func (c *MusicPartner) Command() *cobra.Command { return c.cmd }

func (c *MusicPartner) getConfig() (*config.MusicPartnerConf, error) {
	if c.root == nil || c.root.Cfg == nil || c.root.Cfg.MusicPartner == nil {
		return nil, fmt.Errorf("未配置音乐合伙人任务")
	}
	cfg := *c.root.Cfg.MusicPartner
	if cfg.Score < 1 || cfg.Score > 4 || cfg.ExtraLimit < 1 || cfg.ExtraLimit > 500 || cfg.WaitMin < 1 || cfg.WaitMax < cfg.WaitMin || cfg.WaitMax > 300 {
		return nil, fmt.Errorf("音乐合伙人配置无效，请检查评分策略、额外任务数量及等待时间")
	}
	return &cfg, nil
}

func (c *MusicPartner) execute(ctx context.Context) error {
	cfg, err := c.getConfig()
	if err != nil {
		c.printf("配置检查失败：%v", err)
		return err
	}
	var accounts []string
	if strings.TrimSpace(c.cookieFile) != "" {
		accounts = append(accounts, c.cookieFile)
	} else if c.root.Cfg.Accounts != nil {
		if cfg.EnableMain {
			accounts = append(accounts, c.root.musicPartnerCookiePath(c.root.Cfg.Accounts.Main))
		}
		if cfg.EnableSecondaries {
			for _, account := range c.root.Cfg.Accounts.Secondary {
				accounts = append(accounts, c.root.musicPartnerCookiePath(account))
			}
		}
	}
	seen := make(map[string]bool)
	var failures []error
	for _, account := range accounts {
		if strings.TrimSpace(account) == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		key, err := filepath.Abs(account)
		if err != nil {
			return fmt.Errorf("账号 Cookie 路径无效")
		}
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		c.printf("开始处理账号 %s", filepath.Base(account))
		if err := c.ExecuteForCookie(ctx, account); err != nil {
			c.root.ReportFailure(account, "music-partner", err)
			failures = append(failures, fmt.Errorf("账号 %s：%w", filepath.Base(account), err))
		}
	}
	if len(seen) == 0 {
		err := fmt.Errorf("音乐合伙人未选择可执行账号，请登录并启用主账号或辅助账号")
		c.printf("%v", err)
		c.root.ReportFailure("-", "music-partner", err)
		return err
	}
	return errors.Join(failures...)
}

// 配置中的相对 Cookie 路径属于应用 home；命令行显式路径仍相对当前目录。
func (c *Root) musicPartnerCookiePath(path string) string {
	if strings.TrimSpace(path) == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(c.getHomePath(), path)
}

// ExecuteForCookie 为每个账号创建独立会话，不修改共享网络配置或写入任务数据库。
func (c *MusicPartner) ExecuteForCookie(ctx context.Context, cookieFile string) (result error) {
	defer func() {
		if result != nil {
			c.printf("账号任务失败：%v", result)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg, err := c.getConfig()
	if err != nil {
		return err
	}
	network, err := musicPartnerNetworkConfig(c.root.Cfg.Network, cookieFile)
	if err != nil {
		return err
	}
	client, err := c.newClient(network, c.l)
	if err != nil {
		return c.transportFailure(ctx, "初始化账号会话", err)
	}
	defer client.Close(context.Background())
	// 复用项目客户端，并为此会话启用证书校验。
	if transport, ok := client.GetClient().Transport.(*http.Transport); ok {
		transport = transport.Clone()
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		}
		transport.TLSClientConfig.InsecureSkipVerify = false
		client.GetClient().Transport = transport
	}
	const endpoint = "https://interface.music.163.com"
	musicCookie, ok := client.Cookie(endpoint, "MUSIC_U")
	if !ok || musicCookie.Value == "" {
		musicCookie, ok = client.Cookie("https://music.163.com", "MUSIC_U")
	}
	csrf, hasCSRF := client.GetCSRF(endpoint)
	if !hasCSRF {
		csrf, hasCSRF = client.GetCSRF("https://music.163.com")
	}
	if !ok || musicCookie.Value == "" || !hasCSRF || csrf == "" {
		return fmt.Errorf("账号 Cookie 不完整，请重新登录后执行音乐合伙人任务")
	}
	return c.run(ctx, weapi.New(client), cfg)
}

func musicPartnerNetworkConfig(original *api.Config, cookieFile string) (*api.Config, error) {
	if original == nil {
		return nil, fmt.Errorf("未配置网络连接")
	}
	if strings.TrimSpace(cookieFile) == "" {
		return nil, fmt.Errorf("未指定账号 Cookie 文件")
	}
	path, err := filepath.Abs(cookieFile)
	if err != nil {
		return nil, fmt.Errorf("账号 Cookie 路径无效")
	}
	copy := *original
	copy.Cookie.Filepath = path
	// 写入超时不代表服务端未提交，禁止 HTTP 层自动重投。
	copy.Retry = 0
	copy.Debug = false
	if copy.Timeout <= 0 || copy.Timeout > time.Minute {
		copy.Timeout = 30 * time.Second
	}
	return &copy, nil
}

func (c *MusicPartner) run(ctx context.Context, client musicPartnerAPI, cfg *config.MusicPartnerConf) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// 每日任务快照最多使用到北京时间午夜，跨日后由下一次调度重新读取。
	ctx, cancel := musicPartnerDayContext(ctx, time.Now())
	defer cancel()
	daily, err := c.daily(ctx, client)
	if err != nil {
		return err
	}
	var failures []error
	completed := 0
	for _, task := range daily.Data.Works {
		if task.Completed {
			continue
		}
		if err := c.score(ctx, client, cfg, daily.Data.Id, task.Work, false); err != nil {
			failures = append(failures, err)
			c.printf("作品 %d 未完成：%v", task.Work.Id, err)
			if musicPartnerMustStop(ctx, err) {
				return errors.Join(failures...)
			}
			continue
		}
		completed++
	}
	c.printf("基础任务：本次成功 %d 首，已有完成 %d/%d 首", completed, daily.Data.CompletedCount, daily.Data.Count)
	if cfg.ExtraTasks {
		if err := c.extra(ctx, client, cfg, daily.Data.Id); err != nil {
			failures = append(failures, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(append(failures, err)...)
	}
	if len(failures) > 0 {
		return fmt.Errorf("音乐合伙人任务未全部完成：%w", errors.Join(failures...))
	}
	c.printf("音乐合伙人任务执行完成")
	return nil
}

func (c *MusicPartner) daily(ctx context.Context, client musicPartnerAPI) (*weapi.PartnerTaskResp, error) {
	response, err := client.PartnerDailyTask(ctx, &weapi.PartnerTaskReq{})
	if err != nil {
		return nil, c.transportFailure(ctx, "读取每日任务", err)
	}
	if response == nil {
		return nil, fmt.Errorf("每日任务返回为空")
	}
	if response.Code != 200 {
		return nil, musicPartnerResponseError("读取每日任务", response.Code, response.Message+response.Msg)
	}
	data := response.Data
	if data.Id <= 0 {
		return nil, fmt.Errorf("当前账号没有可执行的音乐合伙人任务，请确认参与资格")
	}
	if data.Count < 0 || data.CompletedCount < 0 || data.CompletedCount > data.Count || int64(len(data.Works)) != data.Count {
		return nil, fmt.Errorf("每日任务数量与作品列表不一致，请稍后重试")
	}
	seen := make(map[int64]bool, len(data.Works))
	var completed int64
	for _, task := range data.Works {
		if task.Work.Id <= 0 || seen[task.Work.Id] {
			return nil, fmt.Errorf("每日任务包含无效或重复的作品")
		}
		seen[task.Work.Id] = true
		if task.Completed {
			completed++
		}
	}
	if completed != data.CompletedCount {
		return nil, fmt.Errorf("每日任务完成数量不一致，请稍后重试")
	}
	return response, nil
}

func (c *MusicPartner) extraList(ctx context.Context, client musicPartnerAPI) (*weapi.PartnerExtraTaskResp, error) {
	response, err := client.PartnerExtraTask(ctx, &weapi.PartnerExtraTaskReq{})
	if err != nil {
		return nil, c.transportFailure(ctx, "读取额外任务", err)
	}
	if response == nil {
		return nil, fmt.Errorf("额外任务返回为空")
	}
	if response.Code != 200 {
		return nil, musicPartnerResponseError("读取额外任务", response.Code, response.Message+response.Msg)
	}
	if response.Data == nil {
		return nil, fmt.Errorf("额外任务列表缺失，请稍后重试")
	}
	seen := make(map[int64]bool, len(response.Data))
	for _, task := range response.Data {
		if task.Work.Id <= 0 || (!task.Completed && task.Work.ResourceId <= 0) || seen[task.Work.Id] {
			return nil, fmt.Errorf("额外任务包含无效或重复的作品")
		}
		seen[task.Work.Id] = true
	}
	return response, nil
}

func (c *MusicPartner) extra(ctx context.Context, client musicPartnerAPI, cfg *config.MusicPartnerConf, taskID int64) error {
	list, err := c.extraList(ctx, client)
	if err != nil {
		return err
	}
	completed := 0
	for _, task := range list.Data {
		if task.Completed {
			completed++
		}
	}
	target := cfg.ExtraLimit
	if cfg.FullExtraTasks {
		target = len(list.Data)
	}
	remaining := target - completed
	if remaining <= 0 {
		c.printf("额外任务已完成 %d 首，达到当前目标", completed)
		return nil
	}
	succeeded := 0
	var failures []error
	for _, task := range list.Data {
		if task.Completed || succeeded >= remaining {
			continue
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		report, err := client.PartnerExtraReport(ctx, &weapi.PartnerExtraReportReq{WorkId: strconv.FormatInt(task.Work.Id, 10), ResourceId: strconv.FormatInt(task.Work.ResourceId, 10), InteractType: "PLAY_END"})
		if err != nil {
			// 上报结果未知时停止本账号；不能重复写入或继续提交该作品评分。
			return errors.Join(append(failures, c.transportFailure(ctx, "上报额外任务播放结束", err))...)
		}
		var reportErr error
		switch {
		case report == nil:
			reportErr = &musicPartnerFailure{message: "播放结束上报返回为空，已停止本账号", stop: true}
		case report.Code != 200:
			reportErr = musicPartnerResponseError("上报播放结束", report.Code, report.Message+report.Msg)
		case !report.Data.InteractResult || musicPartnerReportFailed(report.Data.FailedReason):
			reportErr = fmt.Errorf("作品 %d 播放结束上报未成功", task.Work.Id)
		}
		if reportErr != nil {
			failures = append(failures, reportErr)
			c.printf("作品 %d 未完成：%v", task.Work.Id, reportErr)
			if musicPartnerMustStop(ctx, reportErr) {
				return errors.Join(failures...)
			}
			continue
		}
		if err := c.score(ctx, client, cfg, taskID, task.Work, true); err != nil {
			failures = append(failures, err)
			c.printf("作品 %d 未完成：%v", task.Work.Id, err)
			if musicPartnerMustStop(ctx, err) {
				return errors.Join(failures...)
			}
			continue
		}
		succeeded++
	}
	c.printf("额外任务：本次成功 %d 首，之前已完成 %d 首", succeeded, completed)
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	// 服务端当前没有足够候选时如实记录，不伪造完成数量。
	if succeeded < remaining {
		c.printf("当前可用额外作品已处理，共 %d/%d 首；等待服务端提供后续作品", completed+succeeded, target)
	}
	return nil
}

func (c *MusicPartner) score(ctx context.Context, client musicPartnerAPI, cfg *config.MusicPartnerConf, taskID int64, work weapi.PartnerWork, extra bool) error {
	delay := cfg.WaitMin
	if cfg.WaitMax > cfg.WaitMin {
		delay += rand.IntN(cfg.WaitMax - cfg.WaitMin + 1)
	}
	c.printf("作品 %d 等待 %d 秒后评分", work.Id, delay)
	if err := c.wait(ctx, time.Duration(delay)*time.Second); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	score, tag := musicPartnerScore(work, cfg.Score)
	response, err := client.PartnerEvaluate(ctx, &weapi.PartnerEvaluateReq{TaskId: strconv.FormatInt(taskID, 10), WorkId: strconv.FormatInt(work.Id, 10), Score: score, Tags: tag, CustomTags: "[]", SyncYunCircle: cfg.SyncYunCircle, Source: "mp-music-partner", ExtraResource: extra})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var statusErr *api.HTTPStatusError
	if errors.As(err, &statusErr) && (statusErr.StatusCode == http.StatusUnauthorized || statusErr.StatusCode == http.StatusForbidden || statusErr.StatusCode == http.StatusTooManyRequests) {
		return musicPartnerResponseError("作品评分", int64(statusErr.StatusCode), "")
	}
	if err != nil || response == nil || (response.Code == 200 && !response.Data.EvaluateRes) {
		if err != nil {
			c.printf("作品 %d 提交结果不确定（错误类型 %T），只查询一次完成状态", work.Id, err)
		}
		return c.confirmScore(ctx, client, taskID, work.Id, extra)
	}
	if response.Code != 200 {
		return musicPartnerResponseError("作品评分", response.Code, response.Message+response.Msg)
	}
	c.printf("作品 %d 评分成功：%s 分", work.Id, score)
	return nil
}

func (c *MusicPartner) confirmScore(ctx context.Context, client musicPartnerAPI, taskID, workID int64, extra bool) error {
	// 查询服务端是唯一判据，不用本地日期缓存推断远端写入结果。
	daily, err := c.daily(ctx, client)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errMusicPartnerAccountBlocked) {
			return err
		}
		return &musicPartnerFailure{message: "评分结果核对失败，已停止本账号，请稍后检查任务状态", stop: true}
	}
	if daily.Data.Id != taskID {
		return &musicPartnerFailure{message: "每日任务已切换，无法确认上一任务评分结果，已停止本账号", stop: true}
	}
	if extra {
		list, err := c.extraList(ctx, client)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, errMusicPartnerAccountBlocked) {
				return err
			}
			return &musicPartnerFailure{message: "额外评分结果核对失败，已停止本账号", stop: true}
		}
		for _, task := range list.Data {
			if task.Work.Id == workID && task.Completed {
				c.printf("作品 %d 经服务端确认已完成评分", workID)
				return nil
			}
		}
	} else {
		for _, task := range daily.Data.Works {
			if task.Work.Id == workID && task.Completed {
				c.printf("作品 %d 经服务端确认已完成评分", workID)
				return nil
			}
		}
	}
	return &musicPartnerFailure{message: fmt.Sprintf("作品 %d 的评分结果未确认，未重复提交，已停止本账号", workID), stop: true}
}

func musicPartnerScore(work weapi.PartnerWork, strategy int) (string, weapi.PartnerTags) {
	score := strategy
	if strategy < 4 {
		for _, r := range work.Name + work.AuthorName {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				score++
				break
			}
		}
	}
	value := strconv.Itoa(score)
	return value, weapi.PartnerTags(value + "-A-1")
}

func musicPartnerReportFailed(reason any) bool {
	if reason == nil {
		return false
	}
	message, ok := reason.(string)
	return !ok || strings.TrimSpace(message) != ""
}

func musicPartnerWait(ctx context.Context, duration time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func musicPartnerDayContext(parent context.Context, now time.Time) (context.Context, context.CancelFunc) {
	beijing := now.In(time.FixedZone("UTC+8", 8*60*60))
	year, month, day := beijing.Date()
	deadline := time.Date(year, month, day+1, 0, 0, 0, 0, beijing.Location())
	return context.WithDeadline(parent, deadline)
}

var errMusicPartnerAccountBlocked = errors.New("账号鉴权失效或被限流")

type musicPartnerFailure struct {
	message        string
	stop           bool
	accountBlocked bool
}

func (e *musicPartnerFailure) Error() string { return e.message }

func (e *musicPartnerFailure) Is(target error) bool {
	return e.accountBlocked && target == errMusicPartnerAccountBlocked
}

func musicPartnerMustStop(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	var failure *musicPartnerFailure
	return errors.As(err, &failure) && failure.stop
}

func musicPartnerResponseError(operation string, code int64, message string) error {
	if code == 405 && strings.Contains(message, "资源状态异常") {
		return &musicPartnerFailure{message: "作品资源不可用，已跳过且未计为成功"}
	}
	if code == 703 {
		return &musicPartnerFailure{message: "当前账号没有音乐合伙人参与资格", stop: true}
	}
	if code == 301 || code == 401 || code == 403 || code == 419 {
		return &musicPartnerFailure{message: "登录或参与权限失效，已停止本账号，请重新登录并确认资格", stop: true, accountBlocked: true}
	}
	lower := strings.ToLower(message)
	if code == 429 || code == 405 || code == 460 || strings.Contains(message, "频繁") || strings.Contains(message, "风控") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "captcha") {
		return &musicPartnerFailure{message: "服务端要求限流或安全验证，已停止本账号，请稍后手动检查", stop: true, accountBlocked: true}
	}
	return &musicPartnerFailure{message: fmt.Sprintf("%s失败（服务码 %d）", operation, code)}
}

func (c *MusicPartner) transportFailure(ctx context.Context, operation string, err error) error {
	// 网络错误可能携带带令牌的 URL，仅记录错误类型，不输出请求内容。
	c.printf("%s失败（错误类型 %T）", operation, err)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var statusErr *api.HTTPStatusError
	if errors.As(err, &statusErr) && (statusErr.StatusCode == http.StatusUnauthorized || statusErr.StatusCode == http.StatusForbidden || statusErr.StatusCode == http.StatusTooManyRequests) {
		return musicPartnerResponseError(operation, int64(statusErr.StatusCode), "")
	}
	return &musicPartnerFailure{message: operation + "失败，请检查网络后重试", stop: true}
}

func (c *MusicPartner) printf(format string, args ...any) {
	if c.cmd != nil {
		c.cmd.Printf("[music-partner] "+format+"\n", args...)
	}
}

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
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/internal/partnerpoints"
	"github.com/3899/ncmm/internal/rewardevent"
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
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("音乐合伙人配置无效：%w", err)
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
			accounts = append(accounts, c.root.Cfg.Accounts.Main)
		}
		if cfg.EnableSecondaries {
			for _, account := range c.root.Cfg.Accounts.Secondary {
				accounts = append(accounts, account)
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
		cookieFile := account
		if strings.TrimSpace(c.cookieFile) == "" {
			cookieFile = c.root.musicPartnerCookiePath(account)
		}
		key, err := filepath.Abs(cookieFile)
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
		if err := c.executeAccount(ctx, account, cookieFile); err != nil {
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
func (c *MusicPartner) ExecuteForCookie(ctx context.Context, cookieFile string) error {
	return c.executeAccount(ctx, cookieFile, cookieFile)
}

// account 保留调用方的账号标识，cookieFile 可以是解析后的绝对路径。
func (c *MusicPartner) executeAccount(ctx context.Context, account, cookieFile string) (result error) {
	c.printf(">>> 账号 (%s) 开始评分 <<<", account)
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
	// 手动导入的有效登录 Cookie 可能不含 CSRF；沿用 API 客户端原值，由平台判断会话是否有效。
	if !ok || musicCookie.Value == "" {
		return fmt.Errorf("账号 Cookie 不完整，请重新登录后执行音乐合伙人任务")
	}
	partner := weapi.New(client)
	return c.runWithPoints(ctx, account, func(ctx context.Context) (*partnerpoints.Snapshot, error) {
		return partnerpoints.Read(ctx, partner)
	}, func(ctx context.Context) error {
		scoreErr := c.run(ctx, partner, cfg)
		if ctx.Err() != nil || errors.Is(scoreErr, context.Canceled) || errors.Is(scoreErr, context.DeadlineExceeded) || errors.Is(scoreErr, errMusicPartnerAccountBlocked) {
			return scoreErr
		}
		return errors.Join(scoreErr, c.recommendAccount(ctx, client, cfg, cookieFile))
	})
}

// 积分读取用于日志及收益记录，不能替代或覆盖评分本身的执行结果。
func (c *MusicPartner) runWithPoints(ctx context.Context, account string, read func(context.Context) (*partnerpoints.Snapshot, error), run func(context.Context) error) error {
	before := c.readPoints(ctx, "评分前", read)
	result := run(ctx)
	if ctx.Err() != nil || errors.Is(result, context.Canceled) || errors.Is(result, context.DeadlineExceeded) {
		c.printf("评分后积分未查询：任务已取消或超时")
		c.emitPointsReward(account, before, nil)
		return result
	}
	after := c.readPoints(ctx, "评分后", read)
	c.logPointsChanges(before, after)
	c.emitPointsReward(account, before, after)
	return result
}

func (c *MusicPartner) emitPointsReward(account string, before, after *partnerpoints.Snapshot) {
	if os.Getenv("NCMM_WEB_CHILD") != "1" {
		return
	}
	points := &rewardevent.MusicPartner{}
	if before != nil && after != nil && before.Period != nil && after.Period != nil && *before.Period == *after.Period && before.Integral != nil && after.Integral != nil {
		points.Delta = *after.Integral - *before.Integral
		points.Known = true
	}
	line, err := rewardevent.MarshalLine(rewardevent.Event{Account: account, Domain: rewardevent.DomainMusicPartner, MusicPartner: points})
	if err != nil {
		log.Debug("音乐合伙人收益事件生成失败：%v", err)
		return
	}
	c.cmd.Println("  " + line)
}

func (c *MusicPartner) readPoints(ctx context.Context, phase string, read func(context.Context) (*partnerpoints.Snapshot, error)) *partnerpoints.Snapshot {
	if ctx.Err() != nil {
		c.printf("%s积分未查询：任务已取消或超时", phase)
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	snapshot, err := read(ctx)
	if err != nil || snapshot == nil {
		// 不输出底层错误，避免 URL、响应正文或会话凭据进入任务日志。
		reason := "暂不可用"
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			reason = "请求已取消或超时"
		}
		c.printf("%s积分读取失败：%s；不影响评分结果", phase, reason)
		return nil
	}
	c.printf("%s积分：本期=%s，本周=%s（期号=%s，周次=%s）", phase, musicPartnerPointValue(snapshot.Integral), musicPartnerPointValue(snapshot.CurrentWeekIntegral), musicPartnerPointValue(snapshot.Period), musicPartnerPointValue(snapshot.Week))
	return snapshot
}

func (c *MusicPartner) logPointsChanges(before, after *partnerpoints.Snapshot) {
	if before == nil {
		before = &partnerpoints.Snapshot{}
	}
	if after == nil {
		after = &partnerpoints.Snapshot{}
	}
	periodReason := ""
	if before.Period == nil || after.Period == nil {
		periodReason = "期号缺失"
	} else if *before.Period != *after.Period {
		periodReason = "期号变化"
	}
	weekReason := periodReason
	if weekReason == "" {
		if before.Week == nil || after.Week == nil {
			weekReason = "周次缺失"
		} else if *before.Week != *after.Week {
			weekReason = "周次变化"
		}
	}
	logChange := func(label string, oldValue, newValue *int64, reason string) {
		if oldValue == nil || newValue == nil {
			reason = "积分缺失"
		}
		if reason != "" {
			c.printf("%s积分：%s -> %s，差值未计算（%s）", label, musicPartnerPointValue(oldValue), musicPartnerPointValue(newValue), reason)
			return
		}
		difference := *newValue - *oldValue
		c.printf("%s积分：%d -> %d，变化 %+d（平台账面变化）", label, *oldValue, *newValue, difference)
		if difference == 0 {
			c.printf("%s积分暂未变化，平台可能延迟入账，不能据此确认没有奖励", label)
		}
	}
	logChange("本期", before.Integral, after.Integral, periodReason)
	logChange("本周", before.CurrentWeekIntegral, after.CurrentWeekIntegral, weekReason)
}

func musicPartnerPointValue(value *int64) string {
	if value == nil {
		return "未知"
	}
	return strconv.FormatInt(*value, 10)
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
			c.printf("%s 未完成：%v", musicPartnerWorkLabel(task.Work), err)
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
	c.printf("阶段=读取每日任务，服务码=%d，分类=%s", response.Code, musicPartnerResponseClass(response.Code, response.Message+response.Msg))
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
	c.printf("阶段=读取额外任务，服务码=%d，分类=%s", response.Code, musicPartnerResponseClass(response.Code, response.Message+response.Msg))
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
	if err := ctx.Err(); err != nil {
		return err
	}
	target := musicPartnerDailyTarget(taskID, cfg)
	if !cfg.FullExtraTasks && target == 0 {
		c.printf("当日额外评分目标为 0，跳过额外任务")
		return nil
	}
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
	if cfg.FullExtraTasks {
		target = len(list.Data)
	}
	remaining := target - completed
	c.printf("额外任务进度：已完成=%d，待评=%d，目标=%d", completed, len(list.Data)-completed, target)
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
		if report != nil {
			if report.DataNull {
				c.printf("阶段=播放结束上报，作品=%d，服务码=%d，结果数据=null，分类=%s", task.Work.Id, report.Code, musicPartnerResponseClass(report.Code, report.Message+report.Msg))
			} else {
				c.printf("阶段=播放结束上报，作品=%d，服务码=%d，上报成功标记=%t，存在失败原因=%t，分类=%s", task.Work.Id, report.Code, report.Data.InteractResult, musicPartnerReportFailed(report.Data.FailedReason), musicPartnerResponseClass(report.Code, report.Message+report.Msg))
			}
			c.printf("%s 播放上报响应：%s", musicPartnerWorkLabel(task.Work), musicPartnerReportResponseJSON(report))
		}
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
			c.printf("%s 未完成：%v", musicPartnerWorkLabel(task.Work), reportErr)
			if musicPartnerMustStop(ctx, reportErr) {
				return errors.Join(failures...)
			}
			continue
		}
		if err := c.score(ctx, client, cfg, taskID, task.Work, true); err != nil {
			failures = append(failures, err)
			c.printf("%s 未完成：%v", musicPartnerWorkLabel(task.Work), err)
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
		return fmt.Errorf("额外评分目标未完成：当前共 %d/%d 首，可用作品不足，等待服务端提供后续作品", completed+succeeded, target)
	}
	return nil
}

func (c *MusicPartner) score(ctx context.Context, client musicPartnerAPI, cfg *config.MusicPartnerConf, taskID int64, work weapi.PartnerWork, extra bool) error {
	delay := cfg.WaitMin
	if cfg.WaitMax > cfg.WaitMin {
		delay += rand.IntN(cfg.WaitMax - cfg.WaitMin + 1)
	}
	c.printf("%s 等待 %d 秒后评分", musicPartnerWorkLabel(work), delay)
	if err := c.wait(ctx, time.Duration(delay)*time.Second); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	score, tag, comment := musicPartnerScore(cfg)
	c.printf("阶段=评分提交，任务=%d，作品=%d，额外任务=%t，分数=%s", taskID, work.Id, extra, score)
	req := &weapi.PartnerEvaluateReq{TaskId: strconv.FormatInt(taskID, 10), WorkId: strconv.FormatInt(work.Id, 10), Score: score, Tags: tag, CustomTags: "%5B%5D", Comment: comment, SyncYunCircle: cfg.SyncYunCircle, ExtraResource: extra}
	c.printf("%s 评分请求数据（凭据已脱敏）：%s", musicPartnerWorkLabel(work), musicPartnerEvaluateRequestJSON(req))
	response, err := client.PartnerEvaluate(ctx, req)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var statusErr *api.HTTPStatusError
	if errors.As(err, &statusErr) {
		c.printf("阶段=评分提交，作品=%d，HTTP=%d，服务码=%d", work.Id, statusErr.StatusCode, statusErr.ServiceCode)
	}
	if response != nil {
		if response.DataNull {
			c.printf("阶段=评分提交，作品=%d，服务码=%d，结果数据=null，分类=%s", work.Id, response.Code, musicPartnerResponseClass(response.Code, response.Message+response.Msg))
		} else {
			c.printf("阶段=评分提交，作品=%d，服务码=%d，评分成功标记=%t，当日额外评分数=%d，分类=%s", work.Id, response.Code, response.Data.EvaluateRes, response.Data.TodayExtendEvaNum, musicPartnerResponseClass(response.Code, response.Message+response.Msg))
		}
		c.printf("%s 评分响应数据：%s", musicPartnerWorkLabel(work), musicPartnerEvaluateResponseJSON(response))
	}
	if errors.As(err, &statusErr) && (statusErr.StatusCode == http.StatusUnauthorized || statusErr.StatusCode == http.StatusForbidden || statusErr.StatusCode == http.StatusTooManyRequests) {
		return musicPartnerHTTPError("作品评分", statusErr)
	}
	if err != nil || response == nil || (response.Code == 200 && !response.Data.EvaluateRes) {
		if err != nil {
			c.printf("%s 提交结果不确定（错误类型 %T），只查询一次完成状态", musicPartnerWorkLabel(work), err)
		}
		return c.confirmScore(ctx, client, taskID, work, extra)
	}
	if response.Code != 200 {
		return musicPartnerResponseError("作品评分", response.Code, response.Message+response.Msg)
	}
	c.printf("%s 评分成功：%s 分", musicPartnerWorkLabel(work), score)
	return nil
}

func (c *MusicPartner) confirmScore(ctx context.Context, client musicPartnerAPI, taskID int64, work weapi.PartnerWork, extra bool) error {
	// 查询服务端是唯一判据，不用本地日期缓存推断远端写入结果。
	workID := work.Id
	c.printf("阶段=评分结果核对，任务=%d，作品=%d，额外任务=%t；仅查询一次，不重投评分", taskID, workID, extra)
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
				c.printf("%s 经服务端确认已完成评分", musicPartnerWorkLabel(work))
				return nil
			}
		}
	} else {
		for _, task := range daily.Data.Works {
			if task.Work.Id == workID && task.Completed {
				c.printf("%s 经服务端确认已完成评分", musicPartnerWorkLabel(work))
				return nil
			}
		}
	}
	return &musicPartnerFailure{message: fmt.Sprintf("作品 %d 的评分结果未确认，未重复提交，已停止本账号", workID), stop: true}
}

func musicPartnerScore(cfg *config.MusicPartnerConf) (string, weapi.PartnerTags, string) {
	score := cfg.ScoreMin
	if cfg.ScoreMax > score {
		score += rand.IntN(cfg.ScoreMax - score + 1)
	}
	value := strconv.Itoa(score)
	comment := ""
	if comments := cfg.Comments[value]; len(comments) > 0 {
		comment = comments[rand.IntN(len(comments))]
	}
	return value, weapi.PartnerTags(value + "-A-1"), comment
}

func musicPartnerDailyTarget(taskID int64, cfg *config.MusicPartnerConf) int {
	if cfg.ExtraLimitMax <= cfg.ExtraLimitMin {
		return cfg.ExtraLimitMin
	}
	// 平台每日任务 ID 与范围决定当日目标，重启或重复执行不会不断提高目标。
	rng := rand.New(rand.NewPCG(uint64(taskID), uint64(cfg.ExtraLimitMin)*1000+uint64(cfg.ExtraLimitMax)))
	return cfg.ExtraLimitMin + rng.IntN(cfg.ExtraLimitMax-cfg.ExtraLimitMin+1)
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
var errMusicPartnerStop = errors.New("停止当前账号任务")

type musicPartnerFailure struct {
	message        string
	stop           bool
	accountBlocked bool
}

func (e *musicPartnerFailure) Error() string { return e.message }

func (e *musicPartnerFailure) Is(target error) bool {
	return (e.accountBlocked && target == errMusicPartnerAccountBlocked) || (e.stop && target == errMusicPartnerStop)
}

func musicPartnerMustStop(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	return errors.Is(err, errMusicPartnerStop) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func musicPartnerResponseError(operation string, code int64, message string) error {
	prefix := fmt.Sprintf("%s失败（服务码 %d，分类=%s）：", operation, code, musicPartnerResponseClass(code, message))
	if code == 405 && strings.Contains(message, "今日没有更多可评定歌曲") {
		return &musicPartnerFailure{message: prefix + "今日没有更多可评定歌曲啦；本次目标未完成，停止评分且不重试", stop: true}
	}
	if code == 405 && strings.Contains(message, "资源状态异常") {
		return &musicPartnerFailure{message: prefix + "作品资源不可用，已跳过且未计为成功"}
	}
	if code == 703 {
		return &musicPartnerFailure{message: prefix + "当前账号没有音乐合伙人参与资格", stop: true}
	}
	if code == 301 || code == 401 || code == 403 || code == 419 {
		return &musicPartnerFailure{message: prefix + "登录或参与权限失效，已停止本账号，请重新登录并确认资格", stop: true, accountBlocked: true}
	}
	lower := strings.ToLower(message)
	if code == 429 || code == 405 || code == 460 || strings.Contains(message, "频繁") || strings.Contains(message, "风控") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "captcha") {
		return &musicPartnerFailure{message: prefix + "服务端拒绝本次请求，已停止本账号，请稍后手动检查", stop: true, accountBlocked: true}
	}
	return &musicPartnerFailure{message: prefix + "服务端未接受本次操作"}
}

// 仅记录固定分类，避免原始服务消息回显 Cookie、CSRF 或用户内容。
func musicPartnerResponseClass(code int64, message string) string {
	lower := strings.ToLower(message)
	switch {
	case code == 405 && strings.Contains(message, "今日没有更多可评定歌曲"):
		return "当日可评歌曲已用尽"
	case code == 405 && strings.Contains(message, "资源状态异常"):
		return "资源不可用"
	case code == 703:
		return "无参与资格"
	case code == 301 || code == 401 || code == 403 || code == 419:
		return "登录或权限失效"
	case strings.Contains(message, "风控") || strings.Contains(lower, "captcha"):
		return "安全验证"
	case code == 429 || strings.Contains(message, "频繁") || strings.Contains(lower, "rate limit"):
		return "频率限制"
	case code == 405 || code == 460:
		return "请求被拒绝，具体原因未确认"
	case code == 200:
		return "接口正常响应"
	default:
		return "其他接口错误"
	}
}

func musicPartnerHTTPError(operation string, status *api.HTTPStatusError) error {
	return &musicPartnerFailure{message: fmt.Sprintf("%s失败（HTTP %d，服务码 %d）：服务端拒绝请求，已停止本账号", operation, status.StatusCode, status.ServiceCode), stop: true, accountBlocked: true}
}

func (c *MusicPartner) transportFailure(ctx context.Context, operation string, err error) error {
	// 网络错误可能携带带令牌的 URL，仅记录错误类型，不输出请求内容。
	c.printf("%s失败（错误类型 %T）", operation, err)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var statusErr *api.HTTPStatusError
	if errors.As(err, &statusErr) {
		c.printf("阶段=%s，HTTP=%d，服务码=%d", operation, statusErr.StatusCode, statusErr.ServiceCode)
	}
	if errors.As(err, &statusErr) && (statusErr.StatusCode == http.StatusUnauthorized || statusErr.StatusCode == http.StatusForbidden || statusErr.StatusCode == http.StatusTooManyRequests) {
		return musicPartnerHTTPError(operation, statusErr)
	}
	return &musicPartnerFailure{message: operation + "失败，请检查网络后重试", stop: true}
}

func (c *MusicPartner) printf(format string, args ...any) {
	if c.cmd != nil {
		c.cmd.Printf("["+time.Now().Format("2006-01-02 15:04:05.000")+"] [music-partner] "+format+"\n", args...)
	}
}

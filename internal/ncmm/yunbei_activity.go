package ncmm

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/pkg/notify"
)

// 活动 ID 和 sourceCode 来自上游 v1.2.10 发布包的调用参数，不使用会员锦鲤活动 ID。
var yunbeiLotteries = []struct {
	id     int64
	name   string
	source int
	code   string
}{
	{4274502, "刮刮乐", 82, "AD_INCOME_SCRATCH"},
	{4268502, "好运翻卡", 81, "AD_INCOME_RAFFLE_FLIP"},
	{4236504, "扭蛋", 88, "AD_INCOME_GASHAPON"},
}

const maxYunbeiLotteryDraws = 30

type yunbeiLotteryAPI interface {
	YunbeiLotteryChanceGet(context.Context, *eapi.YunbeiLotteryChanceGetReq) (*eapi.YunbeiLotteryChanceGetResp, error)
	YunbeiLotteryDo(context.Context, *eapi.YunbeiLotteryDoReq) (*eapi.YunbeiLotteryDoResp, error)
}

type yunbeiActivityIssue struct {
	operation   string
	code        int
	unconfirmed bool
	blocked     bool
}

func (e *yunbeiActivityIssue) Error() string {
	if e.code != 0 {
		return fmt.Sprintf("%s未获确认（服务码 %d）", e.operation, e.code)
	}
	return e.operation + "未获确认"
}

func yunbeiActivityResponse(operation string, code int, requestErr error, writing bool) error {
	if requestErr == nil && code == 200 {
		return nil
	}
	issue := &yunbeiActivityIssue{operation: operation, code: code, unconfirmed: writing && (requestErr != nil || code == 0 || code >= 500)}
	var status *api.HTTPStatusError
	if errors.As(requestErr, &status) {
		issue.code = status.StatusCode
		issue.unconfirmed = writing && status.StatusCode >= 500
	}
	issue.blocked = issue.code == 301 || issue.code == 401 || issue.code == 403 || issue.code == 429
	return issue
}

func (c *SignIn) reportYunbeiActivity(account, task, outcome, message string) {
	if c.cmd != nil {
		c.cmd.Printf("  [云贝/%s] %s\n", task, message)
	}
	switch outcome {
	case "failure":
		c.root.ReportFailureMsg(account, "云贝/"+task, message)
	case "skip":
		c.root.ReportSkip(account, "云贝/"+task, message)
	case "unconfirmed":
		c.root.ReportUnconfirmed(account, "云贝/"+task, message)
	}
}

func (c *SignIn) reportYunbeiActivityError(account, task string, err error) error {
	var issue *yunbeiActivityIssue
	outcome := "failure"
	if errors.As(err, &issue) && issue.unconfirmed {
		outcome = "unconfirmed"
	}
	c.reportYunbeiActivity(account, task, outcome, err.Error()+"；本项停止，不自动重投写请求")
	if issue != nil && issue.blocked {
		return issue
	}
	return nil
}

func yunbeiLotteryChances(response *eapi.YunbeiLotteryChanceGetResp) (int, int, int, error) {
	if response == nil || response.Code != 200 || response.Data == nil {
		return 0, 0, 0, fmt.Errorf("抽奖次数状态缺失")
	}
	d := response.Data
	if d.TotalChance == nil || d.UsedChance == nil || d.RemainChance == nil || *d.TotalChance < 0 || *d.UsedChance < 0 || *d.RemainChance < 0 || *d.RemainChance > *d.TotalChance || *d.UsedChance > *d.TotalChance {
		return 0, 0, 0, fmt.Errorf("抽奖次数字段缺失或无效")
	}
	return *d.TotalChance, *d.UsedChance, *d.RemainChance, nil
}

func (c *SignIn) runYunbeiExistingLottery(ctx context.Context, client yunbeiLotteryAPI, account string) error {
	for _, lottery := range yunbeiLotteries {
		if err := ctx.Err(); err != nil {
			return err
		}
		response, err := client.YunbeiLotteryChanceGet(ctx, &eapi.YunbeiLotteryChanceGetReq{PlayId: lottery.id})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		code := 0
		if response != nil {
			code = response.Code
		}
		if issue := yunbeiActivityResponse("查询抽奖次数", code, err, false); issue != nil {
			if stop := c.reportYunbeiActivityError(account, lottery.name, issue); stop != nil {
				return stop
			}
			continue
		}
		total, used, remaining, err := yunbeiLotteryChances(response)
		if err != nil {
			c.reportYunbeiActivityError(account, lottery.name, err)
			continue
		}
		if remaining == 0 {
			c.reportYunbeiActivity(account, lottery.name, "skip", "当前没有可用次数，不兑换或购买次数")
			continue
		}
		budget := min(remaining, maxYunbeiLotteryDraws)
		for draw := 0; draw < budget; draw++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			receipt, submitErr := client.YunbeiLotteryDo(ctx, &eapi.YunbeiLotteryDoReq{ActivityId: lottery.id, DrawCount: 1, Ext: map[string]any{"AD_COIN_CENTER_SOURCE": map[string]any{"sourceCode": lottery.code}}})
			if ctx.Err() != nil {
				return ctx.Err()
			}
			code = 0
			if receipt != nil {
				code = receipt.Code
			}
			if issue := yunbeiActivityResponse("提交抽奖", code, submitErr, true); issue != nil {
				if stop := c.reportYunbeiActivityError(account, lottery.name, issue); stop != nil {
					return stop
				}
				break
			}
			// 写回执成功后仍查询官方次数。未减少时不再抽取，避免耗尽次数或重复提交。
			after, queryErr := client.YunbeiLotteryChanceGet(ctx, &eapi.YunbeiLotteryChanceGetReq{PlayId: lottery.id})
			if ctx.Err() != nil {
				return ctx.Err()
			}
			code = 0
			if after != nil {
				code = after.Code
			}
			if issue := yunbeiActivityResponse("核对抽奖次数", code, queryErr, false); issue != nil {
				c.reportYunbeiActivity(account, lottery.name, "unconfirmed", "抽奖已提交但官方次数未能核对，本轮不再提交")
				if stop := c.reportYunbeiActivityError(account, lottery.name, issue); stop != nil {
					return stop
				}
				break
			}
			afterTotal, afterUsed, afterRemaining, stateErr := yunbeiLotteryChances(after)
			if queryErr != nil || stateErr != nil || afterTotal != total || afterUsed != used+1 || afterRemaining != remaining-1 || receipt.Data == nil || receipt.Data.RestChance == nil || *receipt.Data.RestChance != afterRemaining {
				c.reportYunbeiActivity(account, lottery.name, "unconfirmed", "抽奖回执与官方次数变化尚未一致，本轮不再提交，请在 App 核对")
				break
			}
			used, remaining = afterUsed, afterRemaining
			c.reportYunbeiActivity(account, lottery.name, "", fmt.Sprintf("已核对一次抽奖，剩余次数 %d；实际云贝以账户流水为准", remaining))
			if remaining == 0 {
				break
			}
			if draw+1 == budget {
				c.reportYunbeiActivity(account, lottery.name, "skip", fmt.Sprintf("达到本轮 %d 次执行上限，剩余 %d 次留待后续调度", budget, remaining))
			}
		}
	}
	return ctx.Err()
}

func yunbeiActivityNetworkConfig(original *api.Config, cookieFile string) (*api.Config, error) {
	if original == nil || strings.TrimSpace(cookieFile) == "" {
		return nil, fmt.Errorf("活动网络配置或账号路径缺失")
	}
	path, err := filepath.Abs(cookieFile)
	if err != nil {
		return nil, fmt.Errorf("活动账号路径无效")
	}
	network := *original
	network.Cookie.Filepath = path
	network.Cookie.ReadOnly = true
	network.Debug, network.Retry = false, 0
	if strings.TrimSpace(network.UserAgent.XEAPI) == "" {
		network.UserAgent.XEAPI = api.DefaultXEAPIUserAgent
	}
	return &network, nil
}

func (c *SignIn) canDrawYunbeiVideoChances(account string, report *accountEarningsReport) bool {
	if report == nil || !report.yunbeiActivitiesHandled["Lottery"] || c.root == nil || c.root.Report == nil || c.root.Cfg == nil || c.root.Cfg.Sign == nil || c.root.Cfg.Sign.YunbeiTask == nil || !c.root.Cfg.Sign.YunbeiTask.EnableLottery {
		return false
	}
	for _, item := range c.root.Report.Items() {
		if accountPathKey(item.Account, c.root.Opts.Home) == accountPathKey(account, c.root.Opts.Home) && strings.HasPrefix(item.Task, "云贝/") && item.Kind != notify.KindSkip {
			return false
		}
	}
	return true
}

func (c *SignIn) handleYunbeiActivities(ctx context.Context, original *api.Client, cookieFile string, allowedTasks map[string]bool, report *accountEarningsReport) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.root == nil || c.root.Cfg == nil || c.root.Cfg.Sign == nil || c.root.Cfg.Sign.YunbeiTask == nil || !c.isAutomatic() {
		return nil
	}
	cfg := c.root.Cfg.Sign.YunbeiTask
	if report != nil && report.Account != "" && accountPathKey(report.Account, c.root.Opts.Home) != accountPathKey(cookieFile, c.root.Opts.Home) {
		// 批次状态只能复用于同一账号；调用方传入另一账号的报告时独立处理。
		report = nil
	}
	if report == nil {
		report = &accountEarningsReport{Account: cookieFile}
	}
	if report.yunbeiActivitiesHandled == nil {
		report.yunbeiActivitiesHandled = make(map[string]bool)
	}
	token := ""
	if c.root.Cfg.Accounts != nil {
		token = strings.TrimSpace(c.root.Cfg.Accounts.AntiCheatTokenFor(cookieFile))
	}
	pending := make([]string, 0, 4)
	for _, task := range []struct {
		key     string
		enabled bool
		video   bool
	}{
		{"RedPacket", cfg.EnableRedPacket, false},
		{"Lottery", cfg.EnableLottery, false},
		{"LotteryWatchVideo", cfg.EnableLotteryWatchVideo, true},
		{"WatchVideoYunbei", cfg.EnableWatchVideoYunbei, true},
	} {
		if !task.enabled || !allowedTasks[task.key] || report.yunbeiActivitiesHandled[task.key] {
			continue
		}
		report.yunbeiActivitiesHandled[task.key] = true
		if task.video && (token == "" || original == nil || c.root.Cfg.Network == nil || strings.TrimSpace(c.root.Cfg.Network.UserAgent.XEAPI) == "" || koiWishProtocol(original) != api.CryptoModeXEAPI) {
			c.reportYunbeiActivity(cookieFile, task.key, "skip", "需当前账号配套的 Android 移动端 Cookie、UA 和 Token；内置 UA 不代表配套会话，已跳过")
			continue
		}
		pending = append(pending, task.key)
	}
	if len(pending) == 0 {
		return ctx.Err()
	}
	network, err := yunbeiActivityNetworkConfig(c.root.Cfg.Network, cookieFile)
	if err != nil {
		return c.reportYunbeiActivityError(cookieFile, "活动", err)
	}
	client, err := api.NewClient(network, c.l)
	if err != nil {
		return c.reportYunbeiActivityError(cookieFile, "活动", fmt.Errorf("无法初始化活动客户端"))
	}
	defer client.Close(context.Background())
	activity := eapi.New(client)
	for _, task := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		// 新活动可能改变云贝流水，后续收益汇总必须重新读取。
		report.Yunbei = nil
		switch task {
		case "RedPacket":
			err = c.runYunbeiRedPacket(ctx, activity, cookieFile, waitYunbeiActivity)
		case "Lottery":
			err = c.runYunbeiExistingLottery(ctx, activity, cookieFile)
		case "LotteryWatchVideo":
			err = c.runYunbeiLotteryVideos(ctx, activity, cookieFile, token, waitYunbeiActivity)
			if err == nil && c.canDrawYunbeiVideoChances(cookieFile, report) {
				c.reportYunbeiActivity(cookieFile, "抽奖", "", "视频阶段结束且抽奖已启用，重新核对可用次数；已有未知回执的活动不会重开")
				err = c.runYunbeiExistingLottery(ctx, activity, cookieFile)
			}
		case "WatchVideoYunbei":
			err = c.runYunbeiVideoStages(ctx, activity, cookieFile, token, waitYunbeiActivity)
		}
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

package ncmm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/pkg/notify"
)

type yunbeiLotteryFixture struct {
	chances map[int64][]string
	draws   []*eapi.YunbeiLotteryDoReq
	reply   string
	err     error
	reads   []int64
}

func (f *yunbeiLotteryFixture) YunbeiLotteryChanceGet(_ context.Context, req *eapi.YunbeiLotteryChanceGetReq) (*eapi.YunbeiLotteryChanceGetResp, error) {
	f.reads = append(f.reads, req.PlayId)
	raw := `{"code":200,"data":{"totalChance":0,"usedChance":0,"remainChance":0}}`
	if values := f.chances[req.PlayId]; len(values) > 0 {
		raw = values[0]
		f.chances[req.PlayId] = values[1:]
	}
	var response eapi.YunbeiLotteryChanceGetResp
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (f *yunbeiLotteryFixture) YunbeiLotteryDo(_ context.Context, req *eapi.YunbeiLotteryDoReq) (*eapi.YunbeiLotteryDoResp, error) {
	copy := *req
	f.draws = append(f.draws, &copy)
	if f.err != nil {
		return nil, f.err
	}
	raw := f.reply
	if raw == "" {
		raw = `{"code":200,"data":{"restChance":0}}`
	}
	var response eapi.YunbeiLotteryDoResp
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func activityTestSign() (*SignIn, *bytes.Buffer) {
	c := NewSign(&Root{Report: notify.NewReport("activity-test")}, nil)
	var output bytes.Buffer
	c.cmd.SetOut(&output)
	return c, &output
}

func TestYunbeiLotteryOnlyConsumesConfirmedExistingChances(t *testing.T) {
	c, output := activityTestSign()
	f := &yunbeiLotteryFixture{chances: map[int64][]string{4274502: {
		`{"code":200,"data":{"totalChance":1,"usedChance":0,"remainChance":1}}`,
		`{"code":200,"data":{"totalChance":1,"usedChance":1,"remainChance":0}}`,
	}}}
	if err := c.runYunbeiExistingLottery(t.Context(), f, "account-a"); err != nil {
		t.Fatal(err)
	}
	if len(f.draws) != 1 || f.draws[0].ActivityId != 4274502 || f.draws[0].DrawCount != 1 {
		t.Fatalf("未严格使用已有次数单次抽奖：%+v，%s", f.draws, output.String())
	}
	ext, _ := json.Marshal(f.draws[0].Ext)
	if string(ext) != `{"AD_COIN_CENTER_SOURCE":{"sourceCode":"AD_INCOME_SCRATCH"}}` {
		t.Fatalf("抽奖活动来源不匹配：%s", ext)
	}
	if failures, _, unknown := c.root.Report.OutcomeCounts(); failures != 0 || unknown != 0 {
		t.Fatalf("服务器已核对次数却报错：%+v", c.root.Report.Items())
	}
}

func TestYunbeiLotteryMissingCountNeverTriggersDraw(t *testing.T) {
	c, _ := activityTestSign()
	f := &yunbeiLotteryFixture{chances: map[int64][]string{4274502: {`{"code":200,"data":{}}`}}}
	_ = c.runYunbeiExistingLottery(t.Context(), f, "account-a")
	if len(f.draws) != 0 {
		t.Fatal("次数字段缺失不能猜测为存在免费次数")
	}
	if failures, _, _ := c.root.Report.OutcomeCounts(); failures == 0 {
		t.Fatal("状态缺失应保留真实失败")
	}
}

func TestYunbeiLotteryUnchangedOfficialStateStopsWithoutReposting(t *testing.T) {
	c, _ := activityTestSign()
	f := &yunbeiLotteryFixture{chances: map[int64][]string{4274502: {
		`{"code":200,"data":{"totalChance":2,"usedChance":0,"remainChance":2}}`,
		`{"code":200,"data":{"totalChance":2,"usedChance":0,"remainChance":2}}`,
	}}, reply: `{"code":200,"data":{"restChance":1}}`}
	_ = c.runYunbeiExistingLottery(t.Context(), f, "account-a")
	if len(f.draws) != 1 {
		t.Fatalf("服务端进度未变仍重复提交：%d", len(f.draws))
	}
	if _, _, unknown := c.root.Report.OutcomeCounts(); unknown == 0 {
		t.Fatal("只有code200不能将抽奖记为确认完成")
	}
}

func TestYunbeiLotteryUnknownWriteDoesNotRetry(t *testing.T) {
	c, output := activityTestSign()
	f := &yunbeiLotteryFixture{chances: map[int64][]string{4274502: {
		`{"code":200,"data":{"totalChance":1,"usedChance":0,"remainChance":1}}`,
	}}, err: errors.New("private-token-in-transport-error")}
	_ = c.runYunbeiExistingLottery(t.Context(), f, "account-a")
	if len(f.draws) != 1 {
		t.Fatalf("未知写回执重复提交：%d", len(f.draws))
	}
	if bytes.Contains(output.Bytes(), []byte("private-token")) {
		t.Fatal("原始底层错误泄漏到用户日志")
	}
	if _, _, unknown := c.root.Report.OutcomeCounts(); unknown == 0 {
		t.Fatal("丢失写回执必须保留未确认结果")
	}
}

func TestYunbeiLotteryCancellationPreventsAnyWrite(t *testing.T) {
	c, _ := activityTestSign()
	f := &yunbeiLotteryFixture{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.runYunbeiExistingLottery(ctx, f, "account-a"); !errors.Is(err, context.Canceled) || len(f.draws) != 0 {
		t.Fatalf("取消后仍执行：err=%v draws=%d", err, len(f.draws))
	}
}

func TestYunbeiLotteryPostQueryBlockStopsFollowingActivities(t *testing.T) {
	for _, code := range []string{"403", "429"} {
		t.Run(code, func(t *testing.T) {
			c, _ := activityTestSign()
			f := &yunbeiLotteryFixture{chances: map[int64][]string{
				4274502: {`{"code":200,"data":{"totalChance":1,"usedChance":0,"remainChance":1}}`, `{"code":` + code + `}`},
				4268502: {`{"code":200,"data":{"totalChance":1,"usedChance":0,"remainChance":1}}`},
			}}
			err := c.runYunbeiExistingLottery(t.Context(), f, "account-a")
			var issue *yunbeiActivityIssue
			if !errors.As(err, &issue) || !issue.blocked || len(f.draws) != 1 || len(f.reads) != 2 {
				t.Fatalf("查询被拦截后仍继续活动：err=%v draws=%d reads=%v", err, len(f.draws), f.reads)
			}
			if _, _, unknown := c.root.Report.OutcomeCounts(); unknown == 0 {
				t.Fatal("已提交但无法核对的结果不能丢失")
			}
		})
	}
}

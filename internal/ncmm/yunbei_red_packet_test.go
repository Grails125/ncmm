package ncmm

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/3899/ncmm/api/eapi"
)

type yunbeiRedPacketFixture struct {
	states []string
	plays  int
	gains  []*eapi.YunbeiAdRightDirectGainReq
	reply  string
}

func (f *yunbeiRedPacketFixture) YunbeiRedPacketInfo(context.Context, *eapi.YunbeiRedPacketInfoReq) (*eapi.YunbeiRedPacketInfoResp, error) {
	raw := `{"code":200,"data":{"currentActivity":1,"hasChance":false,"remainingChance":0}}`
	if len(f.states) > 0 {
		raw, f.states = f.states[0], f.states[1:]
	}
	var reply eapi.YunbeiRedPacketInfoResp
	err := json.Unmarshal([]byte(raw), &reply)
	return &reply, err
}

func (f *yunbeiRedPacketFixture) YunbeiRedPacketPlay(_ context.Context, req *eapi.YunbeiRedPacketPlayReq) (*eapi.YunbeiRedPacketPlayResp, error) {
	f.plays++
	return &eapi.YunbeiRedPacketPlayResp{Code: 200, Data: "request-for-this-account"}, nil
}

func (f *yunbeiRedPacketFixture) YunbeiAdRightDirectGain(_ context.Context, req *eapi.YunbeiAdRightDirectGainReq) (*eapi.YunbeiAdRightDirectGainResp, error) {
	copy := *req
	f.gains = append(f.gains, &copy)
	raw := f.reply
	if raw == "" {
		raw = `{"code":200,"success":true,"data":{"showContent":"云贝奖励"}}`
	}
	var reply eapi.YunbeiAdRightDirectGainResp
	err := json.Unmarshal([]byte(raw), &reply)
	return &reply, err
}

func noActivityWait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func TestYunbeiRedPacketWaitsAndConfirmsServerChance(t *testing.T) {
	c, _ := activityTestSign()
	f := &yunbeiRedPacketFixture{states: []string{
		`{"code":200,"data":{"currentActivity":1,"hasChance":true,"remainingChance":1}}`,
		`{"code":200,"data":{"currentActivity":1,"hasChance":false,"remainingChance":0}}`,
	}}
	waits := 0
	err := c.runYunbeiRedPacket(t.Context(), f, "account-a", func(ctx context.Context, duration time.Duration) error {
		waits++
		if duration < 8*time.Second || len(f.gains) != 0 {
			t.Fatalf("未按先等待后领取执行：duration=%s gains=%d", duration, len(f.gains))
		}
		return ctx.Err()
	})
	if err != nil || f.plays != 1 || len(f.gains) != 1 || waits != 1 {
		t.Fatalf("err=%v plays=%d gains=%d waits=%d", err, f.plays, len(f.gains), waits)
	}
	req := f.gains[0]
	if req.ReqUid != "request-for-this-account" || req.Source != 93 || req.RightType != 6 || req.BizParams.Rank < 1 || req.BizParams.Rank > 3 {
		t.Fatalf("红包雨权益参数不一致：%+v", req)
	}
	if failures, _, unknown := c.root.Report.OutcomeCounts(); failures != 0 || unknown != 0 {
		t.Fatalf("已确认的领取误报异常：%+v", c.root.Report.Items())
	}
}

func TestYunbeiRedPacketMissingChanceNeverPlays(t *testing.T) {
	c, _ := activityTestSign()
	f := &yunbeiRedPacketFixture{states: []string{`{"code":200,"data":{"currentActivity":1}}`}}
	_ = c.runYunbeiRedPacket(t.Context(), f, "account-a", noActivityWait)
	if f.plays != 0 || len(f.gains) != 0 {
		t.Fatal("缺失次数不得猜测并领取")
	}
}

func TestYunbeiRedPacketUnconfirmedReceiptDoesNotContinue(t *testing.T) {
	c, _ := activityTestSign()
	f := &yunbeiRedPacketFixture{states: []string{
		`{"code":200,"data":{"currentActivity":1,"hasChance":true,"remainingChance":2}}`,
		`{"code":200,"data":{"currentActivity":1,"hasChance":true,"remainingChance":1}}`,
	}, reply: `{"code":200,"data":{}}`}
	_ = c.runYunbeiRedPacket(t.Context(), f, "account-a", noActivityWait)
	if f.plays != 1 || len(f.gains) != 1 {
		t.Fatal("未知回执后继续消耗次数")
	}
	if _, _, unknown := c.root.Report.OutcomeCounts(); unknown != 1 {
		t.Fatalf("未确认领取被当作成功：%+v", c.root.Report.Items())
	}
}

func TestYunbeiRedPacketCancelDuringWaitPreventsGain(t *testing.T) {
	c, _ := activityTestSign()
	f := &yunbeiRedPacketFixture{states: []string{`{"code":200,"data":{"currentActivity":0,"hasChance":true,"remainingChance":1}}`}}
	ctx, cancel := context.WithCancel(t.Context())
	err := c.runYunbeiRedPacket(ctx, f, "account-a", func(context.Context, time.Duration) error {
		cancel()
		return ctx.Err()
	})
	if err != context.Canceled || len(f.gains) != 0 {
		t.Fatalf("取消没有保留：%v，gains=%d", err, len(f.gains))
	}
}

func TestYunbeiRedPacketPostQueryBlockPropagates(t *testing.T) {
	c, _ := activityTestSign()
	f := &yunbeiRedPacketFixture{states: []string{
		`{"code":200,"data":{"currentActivity":1,"hasChance":true,"remainingChance":1}}`,
		`{"code":403}`,
	}}
	err := c.runYunbeiRedPacket(t.Context(), f, "account-a", noActivityWait)
	var issue *yunbeiActivityIssue
	if !errors.As(err, &issue) || !issue.blocked || len(f.gains) != 1 {
		t.Fatalf("红包雨核对被拦截没有停止账号：err=%v gains=%d", err, len(f.gains))
	}
}

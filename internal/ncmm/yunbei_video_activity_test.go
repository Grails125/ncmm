package ncmm

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/api/eapi"
)

func videoPtr[T any](v T) *T { return &v }

func videoChances(total, used, remaining int) *eapi.YunbeiLotteryChanceGetResp {
	return &eapi.YunbeiLotteryChanceGetResp{Code: 200, Data: &eapi.YunbeiLotteryChanceState{TotalChance: &total, UsedChance: &used, RemainChance: &remaining}}
}

func videoStage(index, count int, amount int64) *eapi.YunbeiStageInfoResp {
	return &eapi.YunbeiStageInfoResp{Code: 200, Data: &eapi.YunbeiStageState{CurrentIndex: &index, TotalStage: &count, CurrentAmount: &amount}}
}

func videoSecKill(limit bool, amount int) *eapi.YunbeiSecKillRecommendResp {
	return &eapi.YunbeiSecKillRecommendResp{Code: 200, Data: &eapi.YunbeiSecKillState{SecKillAdInfo: &eapi.YunbeiSecKillAdInfo{Limit: &limit, Amount: &amount, BaseSource: videoPtr(86)}}}
}

type yunbeiVideoFixture struct {
	chances       map[int64][]*eapi.YunbeiLotteryChanceGetResp
	stages        []*eapi.YunbeiStageInfoResp
	secKills      []*eapi.YunbeiSecKillRecommendResp
	rights        []*eapi.YunbeiAdRightsGainResp
	rightsErr     error
	adResponse    *eapi.YunbeiAdGetResp
	adErr         error
	chanceCalls   []int64
	stageCalls    int
	secKillCalls  int
	ads           []*eapi.YunbeiAdGetReq
	writes        []*eapi.YunbeiAdRightsGainReq
	cancelOnStage context.CancelFunc
	cancelAtStage int
}

func (f *yunbeiVideoFixture) YunbeiLotteryChanceGet(_ context.Context, req *eapi.YunbeiLotteryChanceGetReq) (*eapi.YunbeiLotteryChanceGetResp, error) {
	f.chanceCalls = append(f.chanceCalls, req.PlayId)
	if values := f.chances[req.PlayId]; len(values) > 0 {
		f.chances[req.PlayId] = values[1:]
		return values[0], nil
	}
	return videoChances(0, 0, 0), nil
}

func (f *yunbeiVideoFixture) YunbeiAdGet(_ context.Context, req *eapi.YunbeiAdGetReq) (*eapi.YunbeiAdGetResp, error) {
	f.ads = append(f.ads, req)
	if f.adResponse != nil || f.adErr != nil {
		return f.adResponse, f.adErr
	}
	return &eapi.YunbeiAdGetResp{Code: 200, Ads: map[string]any{"fixture": true}}, nil
}

func (f *yunbeiVideoFixture) YunbeiAdRightsGain(_ context.Context, req *eapi.YunbeiAdRightsGainReq) (*eapi.YunbeiAdRightsGainResp, error) {
	f.writes = append(f.writes, req)
	if len(f.rights) > 0 {
		response := f.rights[0]
		f.rights = f.rights[1:]
		return response, f.rightsErr
	}
	return &eapi.YunbeiAdRightsGainResp{Code: 200, Data: &eapi.YunbeiAdRightsState{GainFlag: videoPtr(true)}}, f.rightsErr
}

func (f *yunbeiVideoFixture) YunbeiSecKillRecommend(_ context.Context, req *eapi.YunbeiSecKillRecommendReq) (*eapi.YunbeiSecKillRecommendResp, error) {
	f.secKillCalls++
	if req.FromPage != "home" {
		panic("fixture requires confirmed home source")
	}
	if len(f.secKills) > 0 {
		response := f.secKills[0]
		f.secKills = f.secKills[1:]
		return response, nil
	}
	return videoSecKill(true, 0), nil
}

func (f *yunbeiVideoFixture) YunbeiStageInfo(_ context.Context, req *eapi.YunbeiStageInfoReq) (*eapi.YunbeiStageInfoResp, error) {
	f.stageCalls++
	if req.EntranceType != "YUNBEI_CENTER" {
		panic("fixture requires confirmed stage entrance")
	}
	if f.cancelOnStage != nil && (f.cancelAtStage == 0 || f.cancelAtStage == f.stageCalls) {
		f.cancelOnStage()
	}
	if len(f.stages) > 0 {
		response := f.stages[0]
		f.stages = f.stages[1:]
		return response, nil
	}
	return videoStage(15, 15, 0), nil
}

func immediateVideoWait(t *testing.T) func(context.Context, time.Duration) error {
	t.Helper()
	return func(ctx context.Context, duration time.Duration) error {
		if duration < 8*time.Second || duration > 10*time.Second {
			t.Fatalf("视频等待未保持确认范围：%s", duration)
		}
		return ctx.Err()
	}
}

func TestYunbeiVideosMissingTokenMakesNoExternalCalls(t *testing.T) {
	for _, stages := range []bool{false, true} {
		c, _ := activityTestSign()
		f := &yunbeiVideoFixture{}
		var err error
		if stages {
			err = c.runYunbeiVideoStages(t.Context(), f, "account-a", " ", immediateVideoWait(t))
		} else {
			err = c.runYunbeiLotteryVideos(t.Context(), f, "account-a", " ", immediateVideoWait(t))
		}
		if err != nil || len(f.chanceCalls)+len(f.ads)+len(f.writes)+f.secKillCalls+f.stageCalls != 0 {
			t.Fatalf("缺少Token时仍调用外部接口：%+v err=%v", f, err)
		}
		if failed, skipped, unknown := c.root.Report.OutcomeCounts(); failed != 0 || skipped != 1 || unknown != 0 {
			t.Fatalf("缺少Token应仅正常跳过：%+v", c.root.Report.Items())
		}
	}
}

func TestYunbeiLotteryVideoUsesConfirmedSourceAndChanceIncrease(t *testing.T) {
	c, output := activityTestSign()
	f := &yunbeiVideoFixture{chances: map[int64][]*eapi.YunbeiLotteryChanceGetResp{}}
	for _, lottery := range yunbeiLotteries {
		f.chances[lottery.id] = []*eapi.YunbeiLotteryChanceGetResp{videoChances(1, 0, 1), videoChances(2, 0, 2)}
	}
	if err := c.runYunbeiLotteryVideos(t.Context(), f, "account-a", "private-account-token", immediateVideoWait(t)); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 3 || len(f.ads) != 3 {
		t.Fatalf("每个活动仅获取一次已核对增次，未自动抽奖：%+v", f)
	}
	for i, lottery := range yunbeiLotteries {
		want := map[string]any{"source": lottery.source, "creativeType": 2, "rightsGainMethod": 4, "extraRightsGainMethod": 0, "generalRightsInfo": map[string]any{"stayTime": 6, "clickStayTime": 6, "source": lottery.source}}
		if !reflect.DeepEqual(f.writes[i].ReqParam, want) || f.writes[i].CheckToken != "private-account-token" || !reflect.DeepEqual(f.ads[i].AdExtJson, map[string]any{"source": lottery.source}) {
			t.Fatalf("活动视频参数未对齐：%+v", f.writes[i])
		}
	}
	if failed, _, unknown := c.root.Report.OutcomeCounts(); failed != 0 || unknown != 0 || strings.Contains(output.String(), "private-account-token") {
		t.Fatalf("已核对的增次被误报或泄漏Token：%s", output.String())
	}
}

func TestYunbeiVideoUnknownWriteStopsWithoutRetryOrTokenLeak(t *testing.T) {
	c, output := activityTestSign()
	f := &yunbeiVideoFixture{stages: []*eapi.YunbeiStageInfoResp{videoStage(0, 15, 10)}, rightsErr: errors.New("private-account-token")}
	if err := c.runYunbeiVideoStages(t.Context(), f, "account-a", "private-account-token", immediateVideoWait(t)); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 1 || strings.Contains(output.String(), "private-account-token") {
		t.Fatalf("未知回执重复提交或泄漏原始错误：%s writes=%d", output.String(), len(f.writes))
	}
	if _, _, unknown := c.root.Report.OutcomeCounts(); unknown != 1 {
		t.Fatalf("写入结果未知必须保留未确认：%+v", c.root.Report.Items())
	}
}

func TestYunbeiVideoStageRequiresGainFlagAndOfficialProgress(t *testing.T) {
	for _, tc := range []struct {
		name string
		gain *eapi.YunbeiAdRightsGainResp
	}{
		{"无确认字段", &eapi.YunbeiAdRightsGainResp{Code: 200}},
		{"拒绝发放", &eapi.YunbeiAdRightsGainResp{Code: 200, Data: &eapi.YunbeiAdRightsState{GainFlag: videoPtr(false)}}},
		{"阶段未推进", &eapi.YunbeiAdRightsGainResp{Code: 200, Data: &eapi.YunbeiAdRightsState{GainFlag: videoPtr(true)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := activityTestSign()
			f := &yunbeiVideoFixture{stages: []*eapi.YunbeiStageInfoResp{videoStage(1, 15, 20), videoStage(1, 15, 20)}, rights: []*eapi.YunbeiAdRightsGainResp{tc.gain}}
			if err := c.runYunbeiVideoStages(t.Context(), f, "account-a", "test-token", immediateVideoWait(t)); err != nil {
				t.Fatal(err)
			}
			if len(f.writes) != 1 {
				t.Fatalf("未确认状态继续提交：%d", len(f.writes))
			}
			if _, _, unknown := c.root.Report.OutcomeCounts(); unknown != 1 {
				t.Fatalf("未确认阶段被错误标记：%+v", c.root.Report.Items())
			}
		})
	}
}

func TestYunbeiVideoStagePayloadAndBoundedProgress(t *testing.T) {
	c, _ := activityTestSign()
	f := &yunbeiVideoFixture{}
	for i := 0; i <= 15; i++ {
		f.stages = append(f.stages, videoStage(i, 100, 17))
	}
	started := time.Now().UnixMilli()
	if err := c.runYunbeiVideoStages(t.Context(), f, "account-a", "test-token", immediateVideoWait(t)); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 15 || f.stageCalls != 16 {
		t.Fatalf("阶段预算未生效：writes=%d stages=%d", len(f.writes), f.stageCalls)
	}
	request := f.writes[0].ReqParam
	if request["source"] != 58 || request["creativeType"] != 2 || request["rightsGainMethod"] != 4 || request["extraRightsGainMethod"] != 0 || !reflect.DeepEqual(request["generalRightsInfo"], map[string]any{"stayTime": 6, "clickStayTime": 6, "source": 58, "rightType": 6}) {
		t.Fatalf("阶段领取参数不匹配：%+v", request)
	}
	var ext struct {
		PreRights struct {
			GenTime  int64 `json:"genTime"`
			PopupNum int   `json:"popupNum"`
			Resource struct {
				Time  int64 `json:"time"`
				Worth int64 `json:"worth"`
			} `json:"resource"`
			RightType int `json:"rightType"`
			Source    int `json:"source"`
		} `json:"preRights"`
	}
	encoded, ok := request["rightsExtJson"].(string)
	if !ok || json.Unmarshal([]byte(encoded), &ext) != nil || ext.PreRights.GenTime < started || ext.PreRights.GenTime > time.Now().UnixMilli() || ext.PreRights.PopupNum != 1 || ext.PreRights.Resource.Time != 0 || ext.PreRights.Resource.Worth != 17 || ext.PreRights.RightType != 6 || ext.PreRights.Source != 58 {
		t.Fatalf("阶段扩展参数不匹配：%+v", request)
	}
}

func TestYunbeiVideoStageInvalidOrCompleteStateDoesNotWrite(t *testing.T) {
	for _, state := range []*eapi.YunbeiStageInfoResp{nil, {Code: 200}, {Code: 200, Data: &eapi.YunbeiStageState{}}, videoStage(-1, 15, 10), videoStage(0, 0, 10), videoStage(15, 15, 0), videoStage(1, 15, 0)} {
		c, _ := activityTestSign()
		f := &yunbeiVideoFixture{stages: []*eapi.YunbeiStageInfoResp{state}}
		_ = c.runYunbeiVideoStages(t.Context(), f, "account-a", "test-token", immediateVideoWait(t))
		if len(f.ads) != 0 || len(f.writes) != 0 {
			t.Fatalf("状态缺失、无效或完成时不得提交：%+v", state)
		}
	}
}

func TestYunbeiVideoSecKillUsesServerSourceAndConfirmsConsumption(t *testing.T) {
	c, output := activityTestSign()
	f := &yunbeiVideoFixture{secKills: []*eapi.YunbeiSecKillRecommendResp{videoSecKill(false, 128), videoSecKill(true, 0)}}
	f.chances = map[int64][]*eapi.YunbeiLotteryChanceGetResp{}
	for _, lottery := range yunbeiLotteries {
		f.chances[lottery.id] = []*eapi.YunbeiLotteryChanceGetResp{videoChances(1, 0, 1), videoChances(2, 0, 2)}
	}
	if err := c.runYunbeiLotteryVideos(t.Context(), f, "account-a", "test-token", immediateVideoWait(t)); err != nil {
		t.Fatal(err)
	}
	// 每个活动的增次已逐项确认，最后一项才是额外首页权益。
	last := f.writes[len(f.writes)-1]
	if last.ReqParam["source"] != 86 || last.ReqParam["generalRightsInfo"] != nil || f.secKillCalls != 2 {
		t.Fatalf("额外首页权益来源或参数不匹配：%+v", last)
	}
	if strings.Contains(output.String(), "获得 128") {
		t.Fatal("不能用页面预告金额伪造实际入账")
	}
}

func TestYunbeiVideoCancellationAfterWaitAndReadPreventsWrite(t *testing.T) {
	for _, cancelAtRead := range []bool{false, true} {
		c, _ := activityTestSign()
		ctx, cancel := context.WithCancel(t.Context())
		f := &yunbeiVideoFixture{stages: []*eapi.YunbeiStageInfoResp{videoStage(0, 15, 10)}}
		if cancelAtRead {
			f.cancelOnStage = cancel
		}
		wait := func(context.Context, time.Duration) error { cancel(); return nil }
		if err := c.runYunbeiVideoStages(ctx, f, "account-a", "test-token", wait); !errors.Is(err, context.Canceled) || len(f.writes) != 0 {
			t.Fatalf("取消后仍写入或吞取消：err=%v writes=%d", err, len(f.writes))
		}
	}
}

func TestYunbeiVideoMissingAndEmptyAdsNeverSubmitRights(t *testing.T) {
	for _, empty := range []bool{false, true} {
		c, _ := activityTestSign()
		ad := &eapi.YunbeiAdGetResp{Code: 200}
		if empty {
			ad.Ads = map[string]any{}
		}
		f := &yunbeiVideoFixture{stages: []*eapi.YunbeiStageInfoResp{videoStage(0, 15, 10)}, adResponse: ad}
		if err := c.runYunbeiVideoStages(t.Context(), f, "account-a", "test-token", immediateVideoWait(t)); err != nil {
			t.Fatal(err)
		}
		failed, skipped, unknown := c.root.Report.OutcomeCounts()
		if len(f.writes) != 0 || unknown != 0 || (empty && (failed != 0 || skipped != 1)) || (!empty && (failed != 1 || skipped != 0)) {
			t.Fatalf("无素材误写或混淆字段缺失与空素材：empty=%t writes=%d report=%+v", empty, len(f.writes), c.root.Report.Items())
		}
	}
}

func TestYunbeiLotteryVideoStopsWhenOfficialChanceDoesNotIncrease(t *testing.T) {
	c, _ := activityTestSign()
	f := &yunbeiVideoFixture{chances: map[int64][]*eapi.YunbeiLotteryChanceGetResp{4274502: {videoChances(1, 0, 1), videoChances(1, 0, 1)}}}
	if err := c.runYunbeiLotteryVideos(t.Context(), f, "account-a", "test-token", immediateVideoWait(t)); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 1 || f.secKillCalls != 0 {
		t.Fatalf("增次未确认却继续写入其他视频任务：%+v", f)
	}
	if _, _, unknown := c.root.Report.OutcomeCounts(); unknown != 1 {
		t.Fatalf("gainFlag=true不能替代官方次数增加：%+v", c.root.Report.Items())
	}
}

func TestYunbeiVideoBusinessRejectionRemainsFailure(t *testing.T) {
	for _, code := range []int{403, 405} {
		c, _ := activityTestSign()
		f := &yunbeiVideoFixture{stages: []*eapi.YunbeiStageInfoResp{videoStage(0, 15, 10)}, rights: []*eapi.YunbeiAdRightsGainResp{{Code: code}}}
		err := c.runYunbeiVideoStages(t.Context(), f, "account-a", "test-token", immediateVideoWait(t))
		if (err != nil) != (code == 403) || len(f.writes) != 1 {
			t.Fatalf("鉴权拦截或业务拒绝处理不正确：code=%d err=%v writes=%d", code, err, len(f.writes))
		}
		if failed, skipped, unknown := c.root.Report.OutcomeCounts(); failed != 1 || skipped != 0 || unknown != 0 {
			t.Fatalf("视频权益拒绝不能当作正常日限额：%+v", c.root.Report.Items())
		}
	}
}

func TestYunbeiVideoFinalProgressReadPreservesCancellation(t *testing.T) {
	c, _ := activityTestSign()
	ctx, cancel := context.WithCancel(t.Context())
	f := &yunbeiVideoFixture{stages: []*eapi.YunbeiStageInfoResp{videoStage(14, 15, 10), videoStage(15, 15, 0)}, cancelOnStage: cancel, cancelAtStage: 2}
	if err := c.runYunbeiVideoStages(ctx, f, "account-a", "test-token", immediateVideoWait(t)); !errors.Is(err, context.Canceled) || len(f.writes) != 1 {
		t.Fatalf("最后一次确认查询吞掉取消：err=%v writes=%d", err, len(f.writes))
	}
}

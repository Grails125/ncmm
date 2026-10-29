package ncmm

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/types"
	"github.com/3899/ncmm/api/weapi"
)

func TestMusicPartnerDiagnosticsKeepStageAndCodeWithoutSecrets(t *testing.T) {
	const secret = "MUSIC_U=private-session; csrf_token=private-token"
	for _, code := range []int64{301, 403, 405, 429, 460, 500, 703} {
		err := musicPartnerResponseError("作品评分", code, "操作频繁 "+secret)
		if !strings.Contains(err.Error(), "作品评分") || !strings.Contains(err.Error(), fmt.Sprint(code)) {
			t.Errorf("missing stage/code: %v", err)
		}
		if strings.Contains(err.Error(), "private-") {
			t.Fatal("server message leaked")
		}
	}
	command, cfg := partnerTestCommand(t)
	var output bytes.Buffer
	command.Command().SetOut(&output)
	fake := &partnerFake{daily: func(context.Context) (*weapi.PartnerTaskResp, error) { return partnerTestDaily(true), nil }, extra: func(context.Context) (*weapi.PartnerExtraTaskResp, error) { return partnerTestExtra(1, 0), nil }}
	fake.evaluate = func(context.Context, *weapi.PartnerEvaluateReq) (*weapi.PartnerEvaluateResp, error) {
		return &weapi.PartnerEvaluateResp{RespCommon: types.RespCommon[weapi.PartnerEvaluateRespData]{Code: 405, Message: "操作频繁 " + secret}}, nil
	}
	cfg.ExtraTasks = true
	if err := command.run(context.Background(), fake, cfg, "account.json"); err == nil {
		t.Fatal("rejection accepted")
	}
	for _, want := range []string{"阶段=播放结束上报", "阶段=评分提交", "作品=100", "服务码=405", "分类=频率限制"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("missing diagnostic %q: %s", want, output.String())
		}
	}
	if strings.Contains(output.String(), "private-") || len(fake.evaluations) != 1 {
		t.Fatal("unsafe diagnostics or repeated write")
	}
}

func TestMusicPartnerDiagnosticsIdentifyConfirmationFailure(t *testing.T) {
	command, cfg := partnerTestCommand(t)
	var output bytes.Buffer
	command.Command().SetOut(&output)
	reads := 0
	fake := &partnerFake{daily: func(context.Context) (*weapi.PartnerTaskResp, error) {
		reads++
		r := partnerTestDaily(false)
		if reads > 1 {
			r.Code = 460
		}
		return r, nil
	}, evaluate: func(context.Context, *weapi.PartnerEvaluateReq) (*weapi.PartnerEvaluateResp, error) {
		return &weapi.PartnerEvaluateResp{RespCommon: types.RespCommon[weapi.PartnerEvaluateRespData]{Code: 200}}, nil
	}}
	if err := command.run(context.Background(), fake, cfg, "account.json"); err == nil {
		t.Fatal("unconfirmed score accepted")
	}
	for _, want := range []string{"服务码=200", "评分成功标记=false", "阶段=评分结果核对", "阶段=读取每日任务", "服务码=460"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("missing %q: %s", want, output.String())
		}
	}
	if reads != 2 || len(fake.evaluations) != 1 {
		t.Fatal("unexpected replay")
	}
}

func TestMusicPartnerHTTPDiagnosticsKeepStatus(t *testing.T) {
	command, cfg := partnerTestCommand(t)
	var output bytes.Buffer
	command.Command().SetOut(&output)
	fake := &partnerFake{daily: func(context.Context) (*weapi.PartnerTaskResp, error) { return partnerTestDaily(false), nil }, evaluate: func(context.Context, *weapi.PartnerEvaluateReq) (*weapi.PartnerEvaluateResp, error) {
		return nil, &api.HTTPStatusError{StatusCode: http.StatusTooManyRequests}
	}}
	if err := command.run(context.Background(), fake, cfg, "account.json"); err == nil {
		t.Fatal("HTTP rejection accepted")
	}
	if !strings.Contains(output.String(), "阶段=评分提交") || !strings.Contains(output.String(), "HTTP=429") {
		t.Fatalf("missing HTTP diagnosis: %s", output.String())
	}
}

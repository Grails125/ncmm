package ncmm

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/3899/ncmm/api/types"
	"github.com/3899/ncmm/api/weapi"
)

func TestMusicPartnerSafeText(t *testing.T) {
	input := "今日没有更多可评定歌曲啦\r\n歌名\x1b[31m 作者\u2028" +
		` csrf_token=test-csrf-only MUSIC_U=test-cookie-only secret="test secret value" ` +
		`{"access_token":"test-token-only"} Authorization: Bearer test-bearer-only ` +
		`https://example.invalid/result?csrf_token=test-url-only&unknown=test-query-only#test-fragment-only`
	got := musicPartnerSafeText(input)
	if !strings.Contains(got, "今日没有更多可评定歌曲啦") || !strings.Contains(got, "歌名") || !strings.Contains(got, "作者") {
		t.Fatalf("human-readable context lost: %q", got)
	}
	for _, secret := range []string{"test-csrf-only", "test-cookie-only", "test secret value", "test-token-only", "test-bearer-only", "test-url-only", "test-query-only", "test-fragment-only"} {
		if strings.Contains(got, secret) {
			t.Fatalf("synthetic credential was not redacted: %q", secret)
		}
	}
	for _, r := range got {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			t.Fatalf("log control character retained: %U", r)
		}
	}
	long := musicPartnerSafeText(strings.Repeat("歌曲", 500))
	if !utf8.ValidString(long) || utf8.RuneCountInString(long) > 240 {
		t.Fatalf("text is not bounded UTF-8: %d runes", utf8.RuneCountInString(long))
	}
	if got := musicPartnerSafeText("csrf\u200b_token='test-hidden-key'"); strings.Contains(got, "test-hidden-key") {
		t.Fatal("format character bypassed credential redaction")
	}
}

func TestMusicPartnerSafeTextUnterminatedCredential(t *testing.T) {
	for _, input := range []string{`secret="test secret unfinished`, `password='test password unfinished`} {
		got := musicPartnerSafeText(input)
		if strings.Contains(got, "test") || strings.Contains(got, "unfinished") {
			t.Fatalf("unterminated credential retained: %q", got)
		}
	}
}

func TestMusicPartnerWorkLabelIsSafe(t *testing.T) {
	got := musicPartnerWorkLabel(weapi.PartnerWork{Id: 42, Name: "歌曲\r\n伪造日志 token=test-title-token", AuthorName: "作者\u202e"})
	if !strings.Contains(got, "歌曲") || !strings.Contains(got, "作者") || !strings.Contains(got, "作品ID 42") || strings.Contains(got, "test-title-token") || strings.ContainsAny(got, "\r\n\u202e") {
		t.Fatalf("unsafe work label: %q", got)
	}
}

func TestMusicPartnerEvaluateResponseJSONPreservesReasonAndResults(t *testing.T) {
	response := &weapi.PartnerEvaluateResp{RespCommon: types.RespCommon[weapi.PartnerEvaluateRespData]{
		Code: 405, Message: "今日没有更多可评定歌曲啦\ncsrf_token=test-response-token", Msg: "请求结束",
		Data: weapi.PartnerEvaluateRespData{EvaluateRes: false, TodayExtendEvaNum: 7, CurScore: 3, ExtraScore: map[string]float64{"1": 3, "2": 4}},
	}}
	got := musicPartnerEvaluateResponseJSON(response)
	var decoded struct {
		Code    int64                         `json:"code"`
		Message string                        `json:"message"`
		Msg     string                        `json:"msg"`
		Data    weapi.PartnerEvaluateRespData `json:"data"`
	}
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Code != 405 || !strings.Contains(decoded.Message, "今日没有更多可评定歌曲啦") || decoded.Msg != "请求结束" || decoded.Data.EvaluateRes || decoded.Data.TodayExtendEvaNum != 7 || decoded.Data.ExtraScore["2"] != 4 {
		t.Fatalf("structured result lost: %s", got)
	}
	if strings.Contains(got, "test-response-token") || strings.ContainsAny(got, "\r\n") {
		t.Fatal("response summary leaked sensitive content")
	}
	if !strings.Contains(response.Message, "test-response-token") {
		t.Fatal("logging mutated the response")
	}
}

func TestMusicPartnerReportResponseJSONOmitsUnknownData(t *testing.T) {
	for _, reason := range []any{"播放未完成\nMUSIC_U=test-report-cookie", map[string]any{"debugInfo": "test-debug-info", "token": "test-unknown-token"}} {
		response := &weapi.PartnerExtraReportResp{RespCommon: types.RespCommon[weapi.PartnerExtraReportRespData]{Code: 200, Data: weapi.PartnerExtraReportRespData{InteractResult: false, FailedReason: reason}}}
		got := musicPartnerReportResponseJSON(response)
		if !json.Valid([]byte(got)) || !strings.Contains(got, `"interactResult":false`) {
			t.Fatalf("invalid report summary: %s", got)
		}
		for _, forbidden := range []string{"test-report-cookie", "test-debug-info", "test-unknown-token", "debugInfo"} {
			if strings.Contains(got, forbidden) {
				t.Fatalf("unknown or sensitive response retained: %q", forbidden)
			}
		}
	}
}

func TestMusicPartnerResponseJSONPreservesNullData(t *testing.T) {
	for _, input := range []string{`{"code":405,"message":"今日没有更多可评定歌曲啦","data":null}`, `{"code":405,"message":"今日没有更多可评定歌曲啦"}`} {
		var evaluate weapi.PartnerEvaluateResp
		var report weapi.PartnerExtraReportResp
		if err := json.Unmarshal([]byte(input), &evaluate); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(input), &report); err != nil {
			t.Fatal(err)
		}
		for _, got := range []string{musicPartnerEvaluateResponseJSON(&evaluate), musicPartnerReportResponseJSON(&report)} {
			if !strings.Contains(got, `"data":null`) || !strings.Contains(got, "今日没有更多可评定歌曲啦") {
				t.Fatalf("missing/null data became a synthetic result: %s", got)
			}
		}
	}
	var evaluate weapi.PartnerEvaluateResp
	var report weapi.PartnerExtraReportResp
	for _, data := range []string{`{"evaluateRes":true,"interactResult":true}`, `null`, `{}`} {
		input := `{"code":200,"data":` + data + `}`
		if err := json.Unmarshal([]byte(input), &evaluate); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(input), &report); err != nil {
			t.Fatal(err)
		}
		for _, got := range []string{musicPartnerEvaluateResponseJSON(&evaluate), musicPartnerReportResponseJSON(&report)} {
			if strings.Contains(got, `"data":null`) != (data == `null`) {
				t.Fatalf("null marker leaked between decoded responses: %s", got)
			}
		}
		if data == `null` && (evaluate.Data.EvaluateRes || report.Data.InteractResult) {
			t.Fatal("null response retained previous result")
		}
	}
}

func TestMusicPartnerEvaluateRequestJSONMasksCredentialsWithoutMutation(t *testing.T) {
	request := &weapi.PartnerEvaluateReq{ReqCommon: types.ReqCommon{CSRFToken: "test-request-csrf"}, TaskId: "10", WorkId: "42", Score: "3", Tags: "3-A-1", CustomTags: "[]", Comment: "中文评论\nsecret=test-comment-secret https://example.invalid/?token=test-comment-token", ExtraResource: true}
	got := musicPartnerEvaluateRequestJSON(request)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["taskId"] != float64(10) || decoded["workId"] != float64(42) || decoded["extraResource"] != "true" || decoded["csrf_token"] != "[REDACTED]" {
		t.Fatalf("request summary changed protocol fields: %s", got)
	}
	for _, secret := range []string{"test-request-csrf", "test-comment-secret", "test-comment-token"} {
		if strings.Contains(got, secret) {
			t.Fatalf("request summary leaked synthetic credential: %q", secret)
		}
	}
	if request.CSRFToken != "test-request-csrf" || !strings.Contains(request.Comment, "test-comment-secret") {
		t.Fatal("logging mutated original request")
	}
	if musicPartnerEvaluateRequestJSON(nil) != "null" || musicPartnerEvaluateResponseJSON(nil) != "null" || musicPartnerReportResponseJSON(nil) != "null" {
		t.Fatal("nil summaries must be null")
	}
}

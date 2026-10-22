package ncmm

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"

	"github.com/3899/ncmm/api/weapi"
)

const musicPartnerLogTextLimit = 240

var (
	musicPartnerLogURL    = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)
	musicPartnerLogAuth   = regexp.MustCompile(`(?i)\b(?:Bearer|Basic)\s+[^\s,;"'}\]]+`)
	musicPartnerLogSecret = regexp.MustCompile(`(?i)(["']?\b(?:(?:[a-z][a-z0-9_-]*)?(?:token|secret|password|passwd)|csrf|__csrf|music_u|music_r_u|cookie|authorization|sessionid|session_id|api[_-]?key|encseckey)\b["']?\s*[:=]\s*)(?:"(?:\\.|[^"\\])*(?:"|$)|'(?:\\.|[^'\\])*(?:'|$)|[^\s,;}\]]+)`)
)

// musicPartnerSafeText 保留可读原因，过滤日志控制字符、链接和显式凭据。
func musicPartnerSafeText(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, value)
	value = musicPartnerLogURL.ReplaceAllString(value, "[链接已脱敏]")
	value = musicPartnerLogAuth.ReplaceAllString(value, "[REDACTED]")
	value = musicPartnerLogSecret.ReplaceAllString(value, "${1}[REDACTED]")
	value = strings.Join(strings.Fields(value), " ")
	characters := []rune(value)
	if len(characters) > musicPartnerLogTextLimit {
		value = string(characters[:musicPartnerLogTextLimit-1]) + "…"
	}
	return value
}

func musicPartnerWorkLabel(work weapi.PartnerWork) string {
	name, author := musicPartnerSafeText(work.Name), musicPartnerSafeText(work.AuthorName)
	if name == "" {
		name = "未命名作品"
	}
	if author == "" {
		author = "未知作者"
	}
	return fmt.Sprintf("《%s》 / %s（作品ID %d）", name, author, work.Id)
}

// musicPartnerEvaluateRequestJSON 只修改副本，凭据始终使用固定占位符。
func musicPartnerEvaluateRequestJSON(request *weapi.PartnerEvaluateReq) string {
	if request == nil {
		return "null"
	}
	summary := *request
	summary.CSRFToken = "[REDACTED]"
	summary.Score = musicPartnerSafeText(summary.Score)
	summary.Tags = weapi.PartnerTags(musicPartnerSafeText(string(summary.Tags)))
	summary.CustomTags = musicPartnerSafeText(summary.CustomTags)
	summary.Comment = musicPartnerSafeText(summary.Comment)
	summary.Source = musicPartnerSafeText(summary.Source)
	summary.ExtraScore = musicPartnerSafeText(summary.ExtraScore)
	return musicPartnerLogJSON(summary)
}

func musicPartnerEvaluateResponseJSON(response *weapi.PartnerEvaluateResp) string {
	if response == nil {
		return "null"
	}
	type evaluationData struct {
		EvaluateRes       bool               `json:"evaluateRes"`
		TodayExtendEvaNum int64              `json:"todayExtendEvaNum"`
		CurScore          int64              `json:"curScore"`
		ExtraScore        map[string]float64 `json:"extraScore,omitempty"`
	}
	summary := struct {
		Code    int64           `json:"code"`
		Message string          `json:"message,omitempty"`
		Msg     string          `json:"msg,omitempty"`
		Data    *evaluationData `json:"data"`
	}{Code: response.Code, Message: musicPartnerSafeText(response.Message), Msg: musicPartnerSafeText(response.Msg)}
	if !response.DataNull {
		summary.Data = &evaluationData{EvaluateRes: response.Data.EvaluateRes, TodayExtendEvaNum: response.Data.TodayExtendEvaNum, CurScore: response.Data.CurScore}
		// 仅记录歌词、旋律、演唱三个已定义维度，不输出未知服务端键。
		for _, key := range []string{"1", "2", "3"} {
			if score, ok := response.Data.ExtraScore[key]; ok && !math.IsNaN(score) && !math.IsInf(score, 0) {
				if summary.Data.ExtraScore == nil {
					summary.Data.ExtraScore = make(map[string]float64, 3)
				}
				summary.Data.ExtraScore[key] = score
			}
		}
	}
	return musicPartnerLogJSON(summary)
}

func musicPartnerReportResponseJSON(response *weapi.PartnerExtraReportResp) string {
	if response == nil {
		return "null"
	}
	type reportData struct {
		InteractResult bool   `json:"interactResult"`
		FailedReason   string `json:"failedReason,omitempty"`
	}
	summary := struct {
		Code    int64       `json:"code"`
		Message string      `json:"message,omitempty"`
		Msg     string      `json:"msg,omitempty"`
		Data    *reportData `json:"data"`
	}{Code: response.Code, Message: musicPartnerSafeText(response.Message), Msg: musicPartnerSafeText(response.Msg)}
	if !response.DataNull {
		summary.Data = &reportData{InteractResult: response.Data.InteractResult}
		if response.Data.FailedReason != nil {
			if reason, ok := response.Data.FailedReason.(string); ok {
				summary.Data.FailedReason = musicPartnerSafeText(reason)
			} else {
				summary.Data.FailedReason = "[非文本原因已省略]"
			}
		}
	}
	return musicPartnerLogJSON(summary)
}

func musicPartnerLogJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `{"error":"日志摘要无法编码"}`
	}
	return string(encoded)
}

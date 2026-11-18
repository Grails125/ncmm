package eapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/3899/ncmm/api"
)

// 端点和字段核对自上游 v1.2.10 Linux 发布包的 Go 类型信息与调用点。
// Data、计数和确认标志保留空值，不能将字段缺失解释为已完成或奖励到账。
type YunbeiRedPacketInfoReq struct {
	UserId int64 `json:"userId"`
}
type YunbeiRedPacketPeriod struct {
	ActivityId int      `json:"activityId"`
	TimePeriod []string `json:"timePeriod"`
	Count      int      `json:"count"`
	UsedCount  int      `json:"usedCount"`
}
type YunbeiRedPacketState struct {
	CurrentActivity *int                    `json:"currentActivity"`
	HasChance       *bool                   `json:"hasChance"`
	RemainingChance *int                    `json:"remainingChance"`
	ActivityInfo    []YunbeiRedPacketPeriod `json:"activityInfo"`
}
type YunbeiRedPacketInfoResp struct {
	Code    int                   `json:"code"`
	Message string                `json:"message"`
	Data    *YunbeiRedPacketState `json:"data"`
}
type YunbeiRedPacketPlayReq struct {
	UserId     int64 `json:"userId"`
	ActivityId int   `json:"activityId"`
}
type YunbeiRedPacketPlayResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data"`
}
type YunbeiRedPacketBizParams struct {
	Rank int `json:"rank"`
}
type YunbeiAdRightDirectGainReq struct {
	ReqUid    string                   `json:"reqUid"`
	UserId    int64                    `json:"userId"`
	Source    int                      `json:"source"`
	RightType int                      `json:"rightType"`
	BizParams YunbeiRedPacketBizParams `json:"bizParams"`
}
type YunbeiDirectGainState struct {
	ShowContent string `json:"showContent"`
}
type YunbeiAdRightDirectGainResp struct {
	Code    int                    `json:"code"`
	Message string                 `json:"message"`
	Success *bool                  `json:"success"`
	Data    *YunbeiDirectGainState `json:"data"`
}

type YunbeiLotteryChanceGetReq struct {
	PlayId int64 `json:"playId"`
}
type YunbeiLotteryChanceState struct {
	TotalChance  *int `json:"totalChance"`
	UsedChance   *int `json:"usedChance"`
	RemainChance *int `json:"remainChance"`
}
type YunbeiLotteryChanceGetResp struct {
	Code    int                       `json:"code"`
	Message string                    `json:"message"`
	Data    *YunbeiLotteryChanceState `json:"data"`
}
type YunbeiLotteryDoReq struct {
	ActivityId int64          `json:"activityId"`
	DrawCount  int            `json:"drawCount"`
	Ext        map[string]any `json:"ext"`
	CheckToken string         `json:"checkToken,omitempty"`
}
type YunbeiLotteryPrize struct {
	PrizeName string `json:"prizeName"`
	YunBeiNum int64  `json:"yunBeiNum"`
}
type YunbeiLotteryDrawPrize struct {
	Order    int   `json:"order"`
	WinCount int64 `json:"winCount"`
}
type YunbeiLotteryState struct {
	UserId             int64                         `json:"userId"`
	ActivityId         int64                         `json:"activityId"`
	DrawPrizeInfoList  []YunbeiLotteryDrawPrize      `json:"drawPrizeInfoList"`
	PrizeDetailInfoMap map[string]YunbeiLotteryPrize `json:"prizeDetailInfoMap"`
	RestChance         *int                          `json:"restChance"`
}
type YunbeiLotteryDoResp struct {
	Code    int                 `json:"code"`
	Message string              `json:"message"`
	Data    *YunbeiLotteryState `json:"data"`
}

// Ads / ReqParam / Ext 属于活动侧可变 JSON 边界，不能把未经确认的响应字段当作领取回执。
type YunbeiAdGetReq struct {
	AdExtJson map[string]any `json:"adextjson"`
}
type YunbeiAdGetResp struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Ads     map[string]any `json:"ads"`
}
type YunbeiAdRightsGainReq struct {
	ReqParam   map[string]any `json:"reqParam"`
	CheckToken string         `json:"checkToken,omitempty"`
}
type YunbeiAdRightsState struct {
	GainFlag    *bool  `json:"gainFlag"`
	ShowContent string `json:"showContent"`
}
type YunbeiAdRightsGainResp struct {
	Code    int                  `json:"code"`
	Message string               `json:"message"`
	Data    *YunbeiAdRightsState `json:"data"`
}
type YunbeiSecKillRecommendReq struct {
	FromPage string `json:"fromPage"`
}
type YunbeiSecKillAdInfo struct {
	Limit       *bool  `json:"limit"`
	BaseSource  *int   `json:"baseSource"`
	BonusSource *int   `json:"bonusSource"`
	Amount      *int   `json:"amount"`
	ActionUrl   string `json:"actionUrl"`
}
type YunbeiSecKillState struct {
	SecKillAdInfo *YunbeiSecKillAdInfo `json:"secKillAdInfo"`
}
type YunbeiSecKillRecommendResp struct {
	Code int                 `json:"code"`
	Data *YunbeiSecKillState `json:"data"`
}
type YunbeiStageInfoReq struct {
	EntranceType string `json:"entranceType"`
}
type YunbeiStageState struct {
	MaximumAmount *int64 `json:"maximumAmount"`
	TotalStage    *int   `json:"totalStage"`
	CurrentIndex  *int   `json:"currentIndex"`
	CurrentAmount *int64 `json:"currentAmount"`
	ActionUrl     string `json:"actionUrl"`
	BehaviorSeqNo string `json:"behaviorSeqNo"`
	HiddenWorth   *bool  `json:"hiddenWorth"`
}
type YunbeiStageInfoResp struct {
	Code    int               `json:"code"`
	Message string            `json:"message"`
	Data    *YunbeiStageState `json:"data"`
}

// 所有活动请求禁用自动重发；包括可能分配请求凭证的查询。敏感响应不进入调试日志。
func (a *Api) yunbeiActivityRequest(ctx context.Context, endpoint string, req, response any, token string) error {
	opts := api.NewOptions()
	opts.CryptoMode = api.CryptoModeXEAPI
	opts.Sensitive = true
	opts.NoRetry = true
	ua := strings.TrimSpace(a.client.UserAgent(api.CryptoModeXEAPI))
	appVersion, buildVersion, osVersion, ok := yunbeiAndroidIdentity(ua)
	if !ok {
		return fmt.Errorf("云贝活动需要有效 Android 客户端 UA")
	}
	opts.SetHeaders(map[string]string{"x-os": "android", "x-osver": osVersion, "x-appver": appVersion, "x-buildver": buildVersion})
	opts.SetCookies(&http.Cookie{Name: "os", Value: "android"}, &http.Cookie{Name: "osver", Value: osVersion}, &http.Cookie{Name: "appver", Value: appVersion}, &http.Cookie{Name: "buildver", Value: buildVersion})
	opts.IsolatedCookieOverrides = true
	if token = strings.TrimSpace(token); token != "" {
		opts.SetHeader("X-antiCheatToken", token)
	}
	_, err := a.client.Request(ctx, "https://interface3.music.163.com/xeapi/"+endpoint, req, response, opts)
	if err != nil {
		// 密钥协商也可能在传输层之前失败，不能将其中的响应正文或地址带入任务日志。
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		var status *api.HTTPStatusError
		if errors.As(err, &status) {
			return &api.HTTPStatusError{StatusCode: status.StatusCode, ServiceCode: status.ServiceCode}
		}
		return fmt.Errorf("云贝活动请求失败（错误类型 %T）", err)
	}
	return nil
}

func yunbeiAndroidIdentity(ua string) (string, string, string, bool) {
	const appPrefix, osPrefix = "NeteaseMusic/", "Android "
	appStart, osStart := strings.Index(ua, appPrefix), strings.Index(ua, osPrefix)
	if appStart < 0 || osStart < 0 {
		return "", "", "", false
	}
	version := ua[appStart+len(appPrefix):]
	if cut := strings.IndexAny(version, "(; "); cut >= 0 {
		version = version[:cut]
	}
	parts := strings.Split(version, ".")
	if len(parts) < 3 || len(parts) > 4 {
		return "", "", "", false
	}
	build := ""
	if len(parts) == 4 {
		build = parts[3]
	}
	osVersion := ua[osStart+len(osPrefix):]
	if cut := strings.IndexAny(osVersion, ";) "); cut >= 0 {
		osVersion = osVersion[:cut]
	}
	for _, field := range append(parts, strings.Split(osVersion, ".")...) {
		if field == "" {
			return "", "", "", false
		}
		for _, digit := range field {
			if digit < '0' || digit > '9' {
				return "", "", "", false
			}
		}
	}
	return strings.Join(parts[:3], "."), build, osVersion, true
}

func (a *Api) YunbeiRedPacketInfo(ctx context.Context, req *YunbeiRedPacketInfoReq) (*YunbeiRedPacketInfoResp, error) {
	if req == nil {
		req = &YunbeiRedPacketInfoReq{UserId: -1}
	}
	payload := *req
	if payload.UserId == 0 {
		payload.UserId = -1
	}
	var reply YunbeiRedPacketInfoResp
	err := a.yunbeiActivityRequest(ctx, "ad/listening/coin/center/red/packet/info", &payload, &reply, "")
	return &reply, err
}

func (a *Api) YunbeiRedPacketPlay(ctx context.Context, req *YunbeiRedPacketPlayReq) (*YunbeiRedPacketPlayResp, error) {
	if req == nil || req.ActivityId < 0 {
		return nil, fmt.Errorf("红包雨活动信息无效")
	}
	payload := *req
	if payload.UserId == 0 {
		payload.UserId = -1
	}
	var reply YunbeiRedPacketPlayResp
	err := a.yunbeiActivityRequest(ctx, "ad/listening/coin/center/red/packet/play", &payload, &reply, "")
	return &reply, err
}

func (a *Api) YunbeiAdRightDirectGain(ctx context.Context, req *YunbeiAdRightDirectGainReq) (*YunbeiAdRightDirectGainResp, error) {
	if req == nil || strings.TrimSpace(req.ReqUid) == "" || req.Source != 93 || req.RightType != 6 || req.BizParams.Rank < 1 || req.BizParams.Rank > 3 {
		return nil, fmt.Errorf("红包雨领取参数无效")
	}
	payload := *req
	if payload.UserId == 0 {
		payload.UserId = -1
	}
	var reply YunbeiAdRightDirectGainResp
	err := a.yunbeiActivityRequest(ctx, "ad/right/direct/gain/form", &payload, &reply, "")
	return &reply, err
}

func (a *Api) YunbeiLotteryChanceGet(ctx context.Context, req *YunbeiLotteryChanceGetReq) (*YunbeiLotteryChanceGetResp, error) {
	if req == nil || req.PlayId <= 0 {
		return nil, fmt.Errorf("云贝抽奖活动编号无效")
	}
	var reply YunbeiLotteryChanceGetResp
	err := a.yunbeiActivityRequest(ctx, "middle/play/lottery/day/chance/get", req, &reply, "")
	return &reply, err
}

func (a *Api) YunbeiLotteryDo(ctx context.Context, req *YunbeiLotteryDoReq) (*YunbeiLotteryDoResp, error) {
	if req == nil || req.ActivityId <= 0 || req.DrawCount != 1 || len(req.Ext) == 0 {
		return nil, fmt.Errorf("云贝抽奖参数无效，仅支持单次免费机会")
	}
	var reply YunbeiLotteryDoResp
	err := a.yunbeiActivityRequest(ctx, "middle/play/do/lottery", req, &reply, req.CheckToken)
	return &reply, err
}

func (a *Api) YunbeiAdGet(ctx context.Context, req *YunbeiAdGetReq) (*YunbeiAdGetResp, error) {
	if req == nil || len(req.AdExtJson) == 0 {
		return nil, fmt.Errorf("云贝广告场景缺失")
	}
	var reply YunbeiAdGetResp
	err := a.yunbeiActivityRequest(ctx, "ad/get?type_ids=%5B%22400002_0%22%5D", req, &reply, "")
	return &reply, err
}

func (a *Api) YunbeiAdRightsGain(ctx context.Context, req *YunbeiAdRightsGainReq) (*YunbeiAdRightsGainResp, error) {
	if req == nil || len(req.ReqParam) == 0 || strings.TrimSpace(req.CheckToken) == "" {
		return nil, fmt.Errorf("云贝视频权益缺少场景或当前账号 Token")
	}
	var reply YunbeiAdRightsGainResp
	err := a.yunbeiActivityRequest(ctx, "ad/listening/rights/gain", req, &reply, req.CheckToken)
	return &reply, err
}

func (a *Api) YunbeiSecKillRecommend(ctx context.Context, req *YunbeiSecKillRecommendReq) (*YunbeiSecKillRecommendResp, error) {
	if req == nil || req.FromPage != "home" {
		return nil, fmt.Errorf("云贝首页活动来源无效")
	}
	var reply YunbeiSecKillRecommendResp
	err := a.yunbeiActivityRequest(ctx, "pointmall/product/seckill/recommend?fromPage=home", req, &reply, "")
	return &reply, err
}

func (a *Api) YunbeiStageInfo(ctx context.Context, req *YunbeiStageInfoReq) (*YunbeiStageInfoResp, error) {
	if req == nil || req.EntranceType != "YUNBEI_CENTER" {
		return nil, fmt.Errorf("云贝阶段入口无效")
	}
	var reply YunbeiStageInfoResp
	err := a.yunbeiActivityRequest(ctx, "ad/listening/new/yunbei/homepage/stage/info", req, &reply, "")
	return &reply, err
}

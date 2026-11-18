package eapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/pkg/cookie"
)

func TestYunbeiActivityMissingStateIsNotZeroProgress(t *testing.T) {
	var packet YunbeiRedPacketInfoResp
	if err := json.Unmarshal([]byte(`{"code":200,"data":{"hasChance":true}}`), &packet); err != nil {
		t.Fatal(err)
	}
	if packet.Data == nil || packet.Data.RemainingChance != nil || packet.Data.CurrentActivity != nil || packet.Data.HasChance == nil || !*packet.Data.HasChance {
		t.Fatal("缺失的次数和活动编号不能被当成已用尽或有效编号")
	}
	var stage YunbeiStageInfoResp
	if err := json.Unmarshal([]byte(`{"code":200,"data":{"currentIndex":0,"currentAmount":128}}`), &stage); err != nil {
		t.Fatal(err)
	}
	if stage.Data == nil || stage.Data.CurrentIndex == nil || *stage.Data.CurrentIndex != 0 || stage.Data.TotalStage != nil {
		t.Fatal("阶段0和未知阶段必须可区分")
	}
	var rights YunbeiAdRightsGainResp
	if err := json.Unmarshal([]byte(`{"code":200,"data":{"showContent":"已接受"}}`), &rights); err != nil {
		t.Fatal(err)
	}
	if rights.Data == nil || rights.Data.GainFlag != nil {
		t.Fatal("服务码200不应自动补出到账标志")
	}
}

func TestYunbeiActivityAndroidIdentityIsRequestLocal(t *testing.T) {
	for _, originalOS := range []string{"pc", "iPhone OS"} {
		t.Run(originalOS, func(t *testing.T) {
			cookiePath := filepath.Join(t.TempDir(), "anonymous.json")
			if err := os.WriteFile(cookiePath, []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			client, err := api.NewClient(&api.Config{UserAgent: api.UserAgentConfig{XEAPI: api.DefaultXEAPIUserAgent}, Cookie: cookie.Config{Filepath: cookiePath, ReadOnly: true}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })
			u, _ := url.Parse("https://music.163.com/")
			original := []*http.Cookie{{Name: "os", Value: originalOS}, {Name: "osver", Value: "synthetic-old-os"}, {Name: "appver", Value: "synthetic-old-app"}, {Name: "buildver", Value: "synthetic-old-build"}, {Name: "MUSIC_U", Value: "synthetic-account-session"}}
			client.SetCookies(u, original)
			calls := 0
			client.GetClient().Transport = nosTestTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/gorilla/anti/crawler/security/key/get" {
					response := partnerCommentTestKey(t, req)
					response.Header.Add("Set-Cookie", "os=key-server-changed; Domain=music.163.com; Path=/")
					return response, nil
				}
				calls++
				if req.UserAgent() != api.DefaultXEAPIUserAgent || req.Header.Get("x-os") != "android" || req.Header.Get("x-osver") != "14" || req.Header.Get("x-appver") != "9.6.05" || req.Header.Get("x-buildver") != "260923162310" {
					t.Fatal("活动Android身份不能混入PC/iOS版本")
				}
				for name, want := range map[string]string{"os": "android", "osver": "14", "appver": "9.6.05", "buildver": "260923162310"} {
					count := 0
					for _, c := range req.Cookies() {
						if c.Name == name {
							count++
							if c.Value != want {
								t.Fatalf("发送的%s身份不一致", name)
							}
						}
					}
					if count != 1 {
						t.Fatalf("不能发送重复身份Cookie %s: %d", name, count)
					}
				}
				if c, err := req.Cookie("MUSIC_U"); err != nil || c.Value != "synthetic-account-session" {
					t.Fatal("活动请求丢失原登录凭据")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}, "Set-Cookie": {"os=server-changed; Domain=music.163.com; Path=/", "MUSIC_U=server-changed; Domain=music.163.com; Path=/"}}, Body: io.NopCloser(strings.NewReader(`{"code":200,"data":{"hasChance":false,"remainingChance":0}}`)), Request: req}, nil
			})
			if _, err := New(client).YunbeiRedPacketInfo(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatal("活动请求次数不符")
			}
			current := map[string]string{}
			for _, c := range client.GetCookies(u) {
				current[c.Name] = c.Value
			}
			for _, c := range original {
				if current[c.Name] != c.Value {
					t.Fatalf("活动请求或响应修改了共享%s Cookie", c.Name)
				}
			}
			if client.UserAgent(api.CryptoModeEAPI) != api.DefaultEAPIUserAgent || client.UserAgent(api.CryptoModeWEAPI) != api.DefaultWEAPIUserAgent {
				t.Fatal("活动请求不能改变其他协议身份")
			}
			if content, err := os.ReadFile(cookiePath); err != nil || string(content) != `{}` {
				t.Fatal("活动请求不能写回原Cookie文件")
			}
		})
	}
}

func TestYunbeiAndroidIdentityRequiresNumericPlatformVersions(t *testing.T) {
	for _, ua := range []string{"", api.DefaultEAPIUserAgent, api.DefaultWEAPIUserAgent, "NeteaseMusic/9.6.05.bad (Android 14)", "NeteaseMusic/9.6.05 (Android )"} {
		if _, _, _, ok := yunbeiAndroidIdentity(ua); ok {
			t.Fatal("无效或非Android UA不可推测平台版本")
		}
	}
}

func TestYunbeiActivityReceiptsPreserveServerState(t *testing.T) {
	client, err := api.NewClient(&api.Config{UserAgent: api.UserAgentConfig{XEAPI: api.DefaultXEAPIUserAgent}, Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "anonymous.json")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	fixtures := map[string]string{
		"/xeapi/ad/listening/coin/center/red/packet/info":    `{"code":200,"data":{"currentActivity":2,"hasChance":true,"remainingChance":1,"activityInfo":[{"activityId":2,"timePeriod":["10:00","14:00"],"count":6,"usedCount":5}]}}`,
		"/xeapi/ad/right/direct/gain/form":                   `{"code":200,"success":false,"data":{"showContent":"请稍后查询"}}`,
		"/xeapi/middle/play/lottery/day/chance/get":          `{"code":200,"data":{"totalChance":4,"usedChance":4,"remainChance":0}}`,
		"/xeapi/middle/play/do/lottery":                      `{"code":200,"data":{"activityId":4274502,"restChance":0,"drawPrizeInfoList":[{"order":2,"winCount":1}],"prizeDetailInfoMap":{"2":{"prizeName":"云贝","yunBeiNum":128}}}}`,
		"/xeapi/ad/listening/rights/gain":                    `{"code":200,"data":{"gainFlag":false,"showContent":"未完成"}}`,
		"/xeapi/pointmall/product/seckill/recommend":         `{"code":200,"data":{"secKillAdInfo":{"limit":false,"baseSource":68,"bonusSource":69,"amount":128,"actionUrl":"orpheus://example"}}}`,
		"/xeapi/ad/listening/new/yunbei/homepage/stage/info": `{"code":200,"data":{"maximumAmount":28320,"totalStage":15,"currentIndex":3,"currentAmount":512,"behaviorSeqNo":"synthetic-sequence","hiddenWorth":false}}`,
	}
	client.GetClient().Transport = nosTestTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/api/gorilla/anti/crawler/security/key/get" {
			return partnerCommentTestKey(t, req), nil
		}
		body, ok := fixtures[req.URL.Path]
		if !ok {
			t.Fatalf("意外访问路径 %s", req.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	a := New(client)
	packet, err := a.YunbeiRedPacketInfo(t.Context(), &YunbeiRedPacketInfoReq{UserId: -1})
	if err != nil || packet.Data == nil || packet.Data.RemainingChance == nil || *packet.Data.RemainingChance != 1 || len(packet.Data.ActivityInfo) != 1 || packet.Data.ActivityInfo[0].UsedCount != 5 {
		t.Fatalf("红包雨实际次数丢失: %v", err)
	}
	gain, err := a.YunbeiAdRightDirectGain(t.Context(), &YunbeiAdRightDirectGainReq{ReqUid: "synthetic", Source: 93, RightType: 6, BizParams: YunbeiRedPacketBizParams{Rank: 1}})
	if err != nil || gain.Success == nil || *gain.Success {
		t.Fatal("code=200必须保留领取未确认/拒绝标志")
	}
	chance, err := a.YunbeiLotteryChanceGet(t.Context(), &YunbeiLotteryChanceGetReq{PlayId: 4274502})
	if err != nil || chance.Data == nil || chance.Data.RemainChance == nil || *chance.Data.RemainChance != 0 {
		t.Fatal("明确0次数应与缺失值区分")
	}
	draw, err := a.YunbeiLotteryDo(t.Context(), &YunbeiLotteryDoReq{ActivityId: 4274502, DrawCount: 1, Ext: map[string]any{"source": "synthetic"}})
	if err != nil || draw.Data == nil || draw.Data.PrizeDetailInfoMap["2"].YunBeiNum != 128 || draw.Data.RestChance == nil || *draw.Data.RestChance != 0 {
		t.Fatal("抽奖响应应保留实际奖项和剩余次数")
	}
	rights, err := a.YunbeiAdRightsGain(t.Context(), &YunbeiAdRightsGainReq{ReqParam: map[string]any{"source": 58}, CheckToken: "synthetic"})
	if err != nil || rights.Data == nil || rights.Data.GainFlag == nil || *rights.Data.GainFlag {
		t.Fatal("不能因code=200把拒绝领取转成成功")
	}
	extra, err := a.YunbeiSecKillRecommend(t.Context(), &YunbeiSecKillRecommendReq{FromPage: "home"})
	if err != nil || extra.Data == nil || extra.Data.SecKillAdInfo == nil || extra.Data.SecKillAdInfo.BaseSource == nil || *extra.Data.SecKillAdInfo.BaseSource != 68 {
		t.Fatal("首页活动必须保留动态source")
	}
	stage, err := a.YunbeiStageInfo(t.Context(), &YunbeiStageInfoReq{EntranceType: "YUNBEI_CENTER"})
	if err != nil || stage.Data == nil || stage.Data.CurrentIndex == nil || *stage.Data.CurrentIndex != 3 || stage.Data.CurrentAmount == nil || *stage.Data.CurrentAmount != 512 {
		t.Fatal("阶段响应必须使用服务器值")
	}
}

func TestYunbeiActivityPreflightAndKeyFailureStayPrivate(t *testing.T) {
	client, err := api.NewClient(&api.Config{Retry: 4, UserAgent: api.UserAgentConfig{XEAPI: api.DefaultXEAPIUserAgent}, Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "anonymous.json")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	calls := 0
	client.GetClient().Transport = nosTestTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("private-key-exchange-network-url")
	})
	a := New(client)
	invalid := []func() error{
		func() error { _, e := a.YunbeiRedPacketPlay(t.Context(), nil); return e },
		func() error {
			_, e := a.YunbeiAdRightDirectGain(t.Context(), &YunbeiAdRightDirectGainReq{ReqUid: "private-req"})
			return e
		},
		func() error { _, e := a.YunbeiLotteryChanceGet(t.Context(), nil); return e },
		func() error {
			_, e := a.YunbeiLotteryDo(t.Context(), &YunbeiLotteryDoReq{ActivityId: 4274502, DrawCount: 2})
			return e
		},
		func() error { _, e := a.YunbeiAdGet(t.Context(), nil); return e },
		func() error {
			_, e := a.YunbeiAdRightsGain(t.Context(), &YunbeiAdRightsGainReq{ReqParam: map[string]any{"source": 58}})
			return e
		},
		func() error { _, e := a.YunbeiSecKillRecommend(t.Context(), nil); return e },
		func() error { _, e := a.YunbeiStageInfo(t.Context(), nil); return e },
	}
	for _, call := range invalid {
		if call() == nil {
			t.Fatal("无效参数应在请求前拒绝")
		}
	}
	if calls != 0 {
		t.Fatal("无效参数发生了网络写入")
	}
	_, err = a.YunbeiRedPacketInfo(t.Context(), nil)
	if err == nil || strings.Contains(err.Error(), "private-key-exchange-network-url") || calls != 1 {
		t.Fatalf("密钥协商错误应脱敏且不重试: %v calls=%d", err, calls)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = a.YunbeiRedPacketInfo(ctx, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("必须保留取消语义")
	}
}

func TestYunbeiActivityXEAPIEndpointsDoNotReplayWrites(t *testing.T) {
	cases := []struct {
		name, path, query string
		call              func(*Api) error
	}{
		{"packet_info", "/xeapi/ad/listening/coin/center/red/packet/info", "", func(a *Api) error { _, e := a.YunbeiRedPacketInfo(t.Context(), nil); return e }},
		{"packet_play", "/xeapi/ad/listening/coin/center/red/packet/play", "", func(a *Api) error {
			_, e := a.YunbeiRedPacketPlay(t.Context(), &YunbeiRedPacketPlayReq{UserId: -1, ActivityId: 2})
			return e
		}},
		{"packet_gain", "/xeapi/ad/right/direct/gain/form", "", func(a *Api) error {
			_, e := a.YunbeiAdRightDirectGain(t.Context(), &YunbeiAdRightDirectGainReq{ReqUid: "private-request-id", UserId: -1, Source: 93, RightType: 6, BizParams: YunbeiRedPacketBizParams{Rank: 1}})
			return e
		}},
		{"lottery_chance", "/xeapi/middle/play/lottery/day/chance/get", "", func(a *Api) error {
			_, e := a.YunbeiLotteryChanceGet(t.Context(), &YunbeiLotteryChanceGetReq{PlayId: 4274502})
			return e
		}},
		{"lottery_draw", "/xeapi/middle/play/do/lottery", "", func(a *Api) error {
			_, e := a.YunbeiLotteryDo(t.Context(), &YunbeiLotteryDoReq{ActivityId: 4274502, DrawCount: 1, Ext: map[string]any{"AD_COIN_CENTER_SOURCE": map[string]any{"sourceCode": "AD_INCOME_SCRATCH"}}, CheckToken: "private-token"})
			return e
		}},
		{"ad_get", "/xeapi/ad/get", "type_ids=%5B%22400002_0%22%5D", func(a *Api) error {
			_, e := a.YunbeiAdGet(t.Context(), &YunbeiAdGetReq{AdExtJson: map[string]any{"source": 58}})
			return e
		}},
		{"ad_rights", "/xeapi/ad/listening/rights/gain", "", func(a *Api) error {
			_, e := a.YunbeiAdRightsGain(t.Context(), &YunbeiAdRightsGainReq{ReqParam: map[string]any{"source": 58}, CheckToken: "private-token"})
			return e
		}},
		{"extra_128", "/xeapi/pointmall/product/seckill/recommend", "fromPage=home", func(a *Api) error {
			_, e := a.YunbeiSecKillRecommend(t.Context(), &YunbeiSecKillRecommendReq{FromPage: "home"})
			return e
		}},
		{"stage_info", "/xeapi/ad/listening/new/yunbei/homepage/stage/info", "", func(a *Api) error {
			_, e := a.YunbeiStageInfo(t.Context(), &YunbeiStageInfoReq{EntranceType: "YUNBEI_CENTER"})
			return e
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := api.NewClient(&api.Config{Retry: 3, Debug: true, UserAgent: api.UserAgentConfig{XEAPI: "NeteaseMusic/9.4.95 (Android 13)"}, Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "anonymous.json")}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })
			calls := 0
			client.GetClient().Transport = nosTestTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/gorilla/anti/crawler/security/key/get" {
					return partnerCommentTestKey(t, req), nil
				}
				calls++
				if req.URL.Scheme != "https" || req.URL.Host != "interface3.music.163.com" || req.URL.Path != tc.path || req.URL.RawQuery != tc.query || req.Method != http.MethodPost {
					t.Fatalf("活动协议不匹配: %s %s", req.Method, req.URL)
				}
				if tc.name == "ad_rights" || tc.name == "lottery_draw" {
					if req.Header.Get("X-antiCheatToken") != "private-token" {
						t.Fatal("缺少对应账号Token")
					}
				}
				if err := req.ParseForm(); err != nil {
					t.Fatal(err)
				}
				if req.PostForm.Get("B") == "" || req.PostForm.Get("S") == "" || req.PostForm.Get("R") == "" {
					t.Fatal("必须复用XEAPI加密")
				}
				return &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":503,"message":"private-response-marker"}`)), Request: req}, nil
			})
			err = tc.call(New(client))
			if err == nil || calls != 1 {
				t.Fatalf("活动请求失败不能自动重投: calls=%d err=%v", calls, err)
			}
			for _, private := range []string{"private-token", "private-request-id", "private-response-marker"} {
				if strings.Contains(err.Error(), private) {
					t.Fatal("活动错误泄露凭据或响应")
				}
			}
		})
	}
}

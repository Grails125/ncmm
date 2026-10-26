package eapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/pkg/crypto"
)

func TestVipLotteryFreeClickUsesCurrentPrizeAndGuardianToken(t *testing.T) {
	cfg := &api.Config{Retry: 3}
	cfg.Cookie.Filepath = filepath.Join(t.TempDir(), "cookie.json")
	client, err := api.NewClient(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	calls := 0
	client.GetClient().Transport = partnerNoteTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodPost || req.URL.Host != "interface3.music.163.com" || req.URL.Path != "/eapi/vipnewcenter/app/lottery/free/click" {
			t.Fatal("错误的免费许愿路由")
		}
		if req.Header.Get("X-antiCheatToken") != "fixture-mobile-token" {
			t.Fatal("缺少匹配的移动端头")
		}
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		plain, err := crypto.EApiDecrypt(req.PostForm.Get("params"), "hex")
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(string(plain), "-36cd479b6b5-")
		if len(parts) != 3 {
			t.Fatal("无效EAPI正文")
		}
		var body map[string]any
		if json.Unmarshal([]byte(parts[1]), &body) != nil {
			t.Fatal("无效业务正文")
		}
		if body["prizeTableId"] != float64(1001) || body["token"] != "fixture-mobile-token" {
			t.Fatal("未使用当前活动和GuardianToken字段")
		}
		for _, key := range []string{"checkToken", "needsGuardianToken", "tokenKey", "activityId", "drawCount", "coinType", "coinCount"} {
			if _, ok := body[key]; ok {
				t.Fatalf("不应加入未确认业务参数 %s", key)
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":200,"data":{"success":true,"privilege":true,"count":1}}`)), Request: req}, nil
	})
	r := &VipLotteryFreeClickReq{PrizeTableID: 1001, Token: " fixture-mobile-token ", CryptoMode: api.CryptoModeEAPI}
	result, err := New(client).VipLotteryFreeClick(t.Context(), r)
	if err != nil || calls != 1 || result == nil || result.Data == nil || result.Data.Success == nil || !*result.Data.Success || result.Data.Count == nil || *result.Data.Count != 1 {
		t.Fatalf("回执无效: %#v %v", result, err)
	}
	if r.Token != " fixture-mobile-token " {
		t.Fatal("修改了调用方Token")
	}
}

func TestVipLotteryFreeClickRejectsInvalidInput(t *testing.T) {
	for _, req := range []*VipLotteryFreeClickReq{nil, {}, {PrizeTableID: 1, Token: " "}, {PrizeTableID: -1, Token: "fixture", CryptoMode: api.CryptoModeEAPI}, {PrizeTableID: 1, Token: "fixture", CryptoMode: api.CryptoModeWEAPI}} {
		if _, err := (&Api{}).VipLotteryFreeClick(t.Context(), req); err == nil {
			t.Fatal("无效参数不得发送")
		}
	}
}

func TestVipLotteryFreeClickDoesNotRetryTransportOrServiceFailure(t *testing.T) {
	for _, transportFailure := range []bool{true, false} {
		t.Run(fmt.Sprint(transportFailure), func(t *testing.T) {
			cfg := &api.Config{Retry: 3}
			cfg.Cookie.Filepath = filepath.Join(t.TempDir(), "cookie.json")
			client, err := api.NewClient(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close(context.Background())
			calls := 0
			client.GetClient().Transport = partnerNoteTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if transportFailure {
					return nil, fmt.Errorf("模拟响应丢失")
				}
				return &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":503}`)), Request: req}, nil
			})
			_, err = New(client).VipLotteryFreeClick(t.Context(), &VipLotteryFreeClickReq{PrizeTableID: 1001, Token: "fixture", CryptoMode: api.CryptoModeEAPI})
			if calls != 1 || err == nil {
				t.Fatalf("失败不能重试: calls=%d err=%v", calls, err)
			}
		})
	}
}

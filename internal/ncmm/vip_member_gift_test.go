package ncmm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/config"
	cookiepkg "github.com/3899/ncmm/pkg/cookie"
	"github.com/3899/ncmm/pkg/database"
	"github.com/3899/ncmm/pkg/log"
)

type vipMemberGiftTransport func(*http.Request) (*http.Response, error)

func (f vipMemberGiftTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestVipMemberGiftExecuteClaimsWithoutTokenAndProtectsSecondaries(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		main                 bool
		explicit             bool
		giftEnabled          bool
		secondaryGiftEnabled bool
		wantGiftCalls        int
	}{
		{"main_gives", true, false, true, false, 1},
		{"secondary_claims_only", false, false, true, false, 0},
		{"secondary_gives_when_allowed", false, false, true, true, 1},
		{"gift_master_switch_off", false, false, false, true, 0},
		{"explicit_cookie_is_main", false, true, true, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cloudReads, cloudSuccesses atomic.Int32
			cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch request.URL.Path {
				case "/tokens/available":
					cloudReads.Add(1)
					_, _ = io.WriteString(w, `{"ok":true,"data":{"token":{"token":"invitation","token_hash":"hash-a"}}}`)
				case "/claims/success":
					cloudSuccesses.Add(1)
					var claim struct {
						Duration    int    `json:"duration"`
						ReceiverUID string `json:"receiverUid"`
					}
					if err := json.NewDecoder(request.Body).Decode(&claim); err != nil {
						t.Error(err)
					}
					if claim.Duration != 7 || claim.ReceiverUID != "42" {
						t.Errorf("incorrect claim: %+v", claim)
					}
					_, _ = io.WriteString(w, `{"ok":true,"data":{"claim":{"duration":7}}}`)
				default:
					t.Errorf("unexpected cloud request: %s", request.URL.Path)
					http.Error(w, "unexpected", http.StatusBadRequest)
				}
			}))
			t.Cleanup(cloud.Close)
			mainPath := filepath.Join(t.TempDir(), "main.json")
			accountPath := filepath.Join(filepath.Dir(mainPath), "fan.json")
			if tc.main {
				accountPath = mainPath
			}
			cfg := &config.Config{
				Accounts:      &config.AccountsConf{Main: mainPath, Secondary: []string{filepath.Join(filepath.Dir(mainPath), "fan.json")}},
				Network:       &api.Config{UserAgent: api.UserAgentConfig{EAPI: api.DefaultEAPIUserAgent}},
				Database:      &database.Config{Driver: "test-disabled"},
				VipMemberGift: &config.VipMemberGiftConf{EnableGift: tc.giftEnabled, EnableGiftSecondaries: tc.secondaryGiftEnabled, EnableClaim: true, Cloud: config.VipMemberGiftCloudConf{BaseURL: cloud.URL}},
			}
			command := NewVipMemberGift(&Root{Cfg: cfg}, log.Default)
			if tc.explicit {
				command.opts.CookieFile = accountPath
				cfg.Accounts = nil
			}
			var output bytes.Buffer
			command.Command().SetOut(&output)
			giftCalls, acceptCalls := 0, 0
			command.newClient = func(network *api.Config, l *log.Logger) (*api.Client, error) {
				client, err := api.NewClient(network, l)
				if err != nil {
					return nil, err
				}
				client.GetClient().Transport = vipMemberGiftTransport(func(request *http.Request) (*http.Response, error) {
					if request.Header.Get("X-antiCheatToken") != "" {
						t.Error("unexpected gift anti-cheat token")
					}
					switch request.URL.Path {
					case "/weapi/w/nuser/account/get":
						return giftJSONResponse(request, `{"code":200,"account":{"id":42},"profile":{"nickname":"测试用户"}}`), nil
					case "/weapi/artist/sub":
						return giftJSONResponse(request, `{"code":200}`), nil
					case "/eapi/vipactivity/app/vip/invitation/page/info":
						giftCalls++
						return giftJSONResponse(request, `{"code":200,"data":{"canSendVip":0}}`), nil
					case "/eapi/vipactivity/app/vip/invitation/detail/info/get":
						return giftJSONResponse(request, `{"code":200,"data":{}}`), nil
					case "/eapi/vipactivity/app/vip/invitation/accept":
						acceptCalls++
						return giftJSONResponse(request, `{"code":200,"data":{"recordId":1,"duration":7}}`), nil
					default:
						t.Fatalf("unexpected API request: %s", request.URL.Path)
						return nil, nil
					}
				})
				return client, nil
			}
			if err := command.ExecuteForCookie(context.Background(), accountPath); err != nil {
				t.Fatal(err)
			}
			if giftCalls != tc.wantGiftCalls || acceptCalls != 1 || cloudReads.Load() != 1 || cloudSuccesses.Load() != 1 {
				t.Fatalf("gift=%d want=%d accept=%d cloud_reads=%d success=%d log=%s", giftCalls, tc.wantGiftCalls, acceptCalls, cloudReads.Load(), cloudSuccesses.Load(), output.String())
			}
			if strings.Contains(output.String(), "未配置") || !strings.Contains(output.String(), "领取云端VIP成功") {
				t.Fatalf("wrong claim output: %s", output.String())
			}
		})
	}
}

func giftJSONResponse(request *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func TestVipMemberGiftClaimRejectsUnusableCards(t *testing.T) {
	for _, tc := range []struct {
		name       string
		response   string
		reason     string
		detail     bool
		failReport bool
	}{
		{name: "invalid_invitation", response: `{"code":12200,"message":"邀请信息不可用"}`, reason: "invalid"},
		{name: "invalid_donor", response: `{"code":12206,"message":"赠送资格无效"}`, reason: "invalid"},
		{name: "expired", response: `{"code":400,"message":"邀请已过期"}`, reason: "expired"},
		{name: "empty_duration", response: `{"code":200,"data":{"recordId":1}}`, reason: "invalid"},
		{name: "zero_duration", response: `{"code":200,"data":{"recordId":1,"duration":0}}`, reason: "invalid"},
		{name: "detail_invalid_invitation", response: `{"code":12200,"message":"邀请信息不可用"}`, reason: "invalid", detail: true},
		{name: "cloud_cannot_invalidate", response: `{"code":200,"data":{"recordId":1,"duration":0}}`, reason: "invalid", failReport: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := NewVipMemberGift(&Root{Cfg: &config.Config{}}, log.Default)
			var output bytes.Buffer
			command.Command().SetOut(&output)
			client, err := api.NewClient(&api.Config{Cookie: cookiepkg.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, log.Default)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })
			accepts, failures, successes, availableCalls := 0, 0, 0, 0
			client.GetClient().Transport = vipMemberGiftTransport(func(request *http.Request) (*http.Response, error) {
				if request.Header.Get("X-antiCheatToken") != "" {
					t.Error("gift claim must not require anti-cheat header")
				}
				switch request.URL.Path {
				case "/eapi/vipactivity/app/vip/invitation/detail/info/get":
					if tc.detail {
						return giftJSONResponse(request, tc.response), nil
					}
					return giftJSONResponse(request, `{"code":200,"data":{}}`), nil
				case "/eapi/vipactivity/app/vip/invitation/accept":
					accepts++
					return giftJSONResponse(request, tc.response), nil
				default:
					t.Fatalf("unexpected API path: %s", request.URL.Path)
					return nil, nil
				}
			})
			cloud := newVipMemberGiftCloudClient("https://cloud.example.test", "")
			cloud.httpClient.Transport = vipMemberGiftTransport(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/tokens/available":
					availableCalls++
					if availableCalls == 1 {
						return giftJSONResponse(request, `{"ok":true,"data":{"token":{"token":"invitation","token_hash":"hash-a"}}}`), nil
					}
					return giftJSONResponse(request, `{"ok":true,"data":{}}`), nil
				case "/tokens/fail":
					failures++
					var report struct {
						Reason    string `json:"reason"`
						TokenHash string `json:"tokenHash"`
					}
					if err := json.NewDecoder(request.Body).Decode(&report); err != nil {
						t.Fatal(err)
					}
					if report.Reason != tc.reason || report.TokenHash != "hash-a" {
						t.Errorf("wrong failure report: %+v", report)
					}
					if tc.failReport {
						return giftJSONResponse(request, `{"ok":false,"error":"fixture cloud error"}`), nil
					}
					return giftJSONResponse(request, `{"ok":true,"data":{}}`), nil
				case "/claims/success":
					successes++
					return giftJSONResponse(request, `{"ok":true,"data":{}}`), nil
				default:
					t.Fatalf("unexpected cloud path: %s", request.URL.Path)
					return nil, nil
				}
			})
			ok, err := command.runClaim(context.Background(), eapi.New(client), cloud, &config.VipMemberGiftConf{}, 42, vipMemberGiftProtocol{Mode: api.CryptoModeEAPI})
			wantAccepts, wantAvailable := 1, 2
			if tc.detail {
				wantAccepts = 0
			}
			if tc.failReport {
				wantAvailable = 1
			}
			if ok || err == nil || accepts != wantAccepts || failures != 1 || successes != 0 || availableCalls != wantAvailable {
				t.Fatalf("unusable card result: ok=%t err=%v accepts=%d failures=%d successes=%d available=%d", ok, err, accepts, failures, successes, availableCalls)
			}
		})
	}
}

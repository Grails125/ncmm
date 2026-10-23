package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/internal/partnerpoints"
	"github.com/3899/ncmm/pkg/cookie"
)

func TestAccountDataWhitelistAndPartialFailure(t *testing.T) {
	s := pointsTestServer(t)
	var logs strings.Builder
	s.opts.Output = func(format string, args ...any) { fmt.Fprintf(&logs, format, args...) }
	calls := 0
	s.readAccountData = func(ctx context.Context, cfg *api.Config, path string) accountDataResult {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 15*time.Second || path != filepath.Join(s.opts.Home, "fan.json") {
			t.Fatalf("invalid context/path: %s", path)
		}
		zero, level := int64(0), int64(2)
		return accountDataResult{GrowthTotal: &zero, VIPLevel: &level, YunbeiBalance: &zero, PartnerError: errors.New("MUSIC_U=unit-secret csrf_token=unit-secret")}
	}
	before, err := os.ReadFile(s.config.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"../../secret", "C:/secret.json", "cookie.json"} {
		w := httptest.NewRecorder()
		s.handleAccountDataGet(w, httptest.NewRequest(http.MethodGet, "/api/v1/accounts/data?account="+url.QueryEscape(value), nil))
		if w.Code != http.StatusBadRequest || calls != 0 {
			t.Fatalf("unconfigured account allowed: %d", w.Code)
		}
	}
	w := httptest.NewRecorder()
	s.handleAccountDataGet(w, httptest.NewRequest(http.MethodGet, "/api/v1/accounts/data?account=fan.json", nil))
	var data accountDataResponse
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || calls != 1 || data.Account != "fan.json" || data.Partner != nil || data.GrowthTotal == nil || *data.GrowthTotal != 0 || data.VIPLevel == nil || *data.VIPLevel != 2 || data.YunbeiBalance == nil || *data.YunbeiBalance != 0 || data.Errors.Partner == "" || data.Errors.Growth != "" || data.Errors.Yunbei != "" || data.UpdatedAt.IsZero() || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("partial failure: %s", w.Body)
	}
	if strings.Contains(w.Body.String()+logs.String(), "unit-secret") {
		t.Fatal("response leaked credentials")
	}
	after, err := os.ReadFile(s.config.path)
	if err != nil || string(before) != string(after) {
		t.Fatal("query changed config")
	}
}

func TestAccountDataDefaultsAndAuthentication(t *testing.T) {
	s := pointsTestServer(t)
	s.readAccountData = func(_ context.Context, _ *api.Config, path string) accountDataResult {
		if path != filepath.Join(s.opts.Home, "cookie.json") {
			t.Fatal("wrong default account")
		}
		return accountDataResult{}
	}
	w := httptest.NewRecorder()
	s.handleAccountDataGet(w, httptest.NewRequest(http.MethodGet, "/api/v1/accounts/data", nil))
	var data accountDataResponse
	_ = json.Unmarshal(w.Body.Bytes(), &data)
	if data.Account != "./cookie.json" || data.Partner != nil || data.GrowthTotal != nil || data.VIPLevel != nil || data.YunbeiBalance != nil || data.Errors.Partner == "" || data.Errors.Growth == "" || data.Errors.Yunbei == "" {
		t.Fatalf("unavailable must remain null: %s", w.Body)
	}
	auth := newAuthTestServer(t, t.TempDir())
	auth.readAccountData = func(context.Context, *api.Config, string) accountDataResult {
		t.Fatal("unauthenticated reader called")
		return accountDataResult{}
	}
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/data", nil)
	req.Host = "127.0.0.1:3899"
	auth.routes().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", w.Code)
	}
}

type accountDataTransport func(*http.Request) (*http.Response, error)

func (f accountDataTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAccountDataOnlyUsesReadEndpointsAndNeverWritesCookie(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookie.json")
	jar, err := cookie.NewCookie(cookie.WithFilePath(path), cookie.WithSyncInterval(0))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("https://music.163.com/")
	jar.SetCookies(u, []*http.Cookie{{Name: "MUSIC_U", Value: "unit-session", Path: "/"}})
	if err := jar.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)
	for _, missing := range []bool{false, true} {
		client, err := partnerpoints.NewReadClient(&api.Config{Debug: true, Retry: 3}, path)
		if err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		paths := make(map[string]int)
		client.GetClient().Transport = accountDataTransport(func(req *http.Request) (*http.Response, error) {
			mu.Lock()
			paths[req.URL.Path]++
			mu.Unlock()
			body := ""
			switch req.URL.Path {
			case "/weapi/music/partner/home/get":
				body = `{"code":703}`
			case "/eapi/vipnewcenter/app/level/growhpoint/basic":
				body = `{"code":200,"data":{"userLevel":{"growthPoint":0,"level":0}}}`
				if missing {
					body = `{"code":200,"data":{"userLevel":{}}}`
				}
			case "/weapi/v1/user/info":
				body = `{"code":200,"userPoint":{"balance":0}}`
				if missing {
					body = `{"code":200,"userPoint":{}}`
				}
			default:
				return nil, fmt.Errorf("unexpected endpoint: %s", req.URL.Path)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}, "Set-Cookie": {"MUSIC_U=updated-memory-only; Path=/"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
		})
		result := readAccountDataClient(t.Context(), client)
		if err := client.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(paths) != 3 || result.Partner != nil || result.PartnerError == nil {
			t.Fatalf("queries=%v result=%+v", paths, result)
		}
		for _, count := range paths {
			if count != 1 {
				t.Fatal("query retried")
			}
		}
		if missing {
			if result.GrowthTotal != nil || result.VIPLevel != nil || result.YunbeiBalance != nil || result.GrowthError == nil || result.YunbeiError == nil {
				t.Fatalf("missing values treated as zero: %+v", result)
			}
		} else if result.GrowthTotal == nil || *result.GrowthTotal != 0 || result.VIPLevel == nil || *result.VIPLevel != 0 || result.YunbeiBalance == nil || *result.YunbeiBalance != 0 || result.GrowthError != nil || result.YunbeiError != nil {
			t.Fatalf("zero values lost: %+v", result)
		}
		current, _ := os.ReadFile(path)
		if string(current) != string(original) {
			t.Fatal("account data persisted cookie updates")
		}
	}
}

func TestAccountDataYunbeiUsesUserPointBalance(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int64
		known      bool
	}{
		{"非零", `{"code":200,"userPoint":{"balance":24680},"data":{"balance":0}}`, 24680, true},
		{"真实零", `{"code":200,"userPoint":{"balance":0}}`, 0, true},
		{"缺失", `{"code":200,"userPoint":{}}`, 0, false},
		{"失败", `{"code":301}`, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := api.NewClient(&api.Config{Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close(context.Background())
			calls := 0
			client.GetClient().Transport = accountDataTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Path != "/weapi/v1/user/info" {
					t.Fatalf("仪表盘查询使用了错误余额来源: %s", req.URL.Path)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: req}, nil
			})
			got, err := readAccountYunbei(t.Context(), client)
			if (err == nil) != tc.known || (got != nil) != tc.known || calls != 1 {
				t.Fatalf("余额是否已知=%t，error=%v，请求次数=%d", got != nil, err, calls)
			}
			if got != nil && *got != tc.want {
				t.Fatalf("余额=%d，预期=%d", *got, tc.want)
			}
		})
	}
}

func TestAccountDataYunbeiRefreshDoesNotRetainStaleBalance(t *testing.T) {
	s := pointsTestServer(t)
	stale, zero := int64(81454), int64(0)
	results := []accountDataResult{
		{YunbeiBalance: &stale},
		{YunbeiBalance: &stale, YunbeiError: errors.New("账号会话已失效")},
		{},
		{YunbeiBalance: &zero},
	}
	calls := 0
	s.readAccountData = func(_ context.Context, _ *api.Config, path string) accountDataResult {
		if path != filepath.Join(s.opts.Home, "fan.json") {
			t.Fatal("账号余额请求使用了错误的账号")
		}
		result := results[calls]
		calls++
		return result
	}
	configBefore, err := os.ReadFile(s.config.path)
	if err != nil {
		t.Fatal(err)
	}
	for i, expected := range []*int64{&stale, nil, nil, &zero} {
		w := httptest.NewRecorder()
		s.handleAccountDataGet(w, httptest.NewRequest(http.MethodGet, "/api/v1/accounts/data?account=fan.json", nil))
		var body accountDataResponse
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || body.Account != "fan.json" || body.UpdatedAt.IsZero() {
			t.Fatalf("第 %d 次余额刷新响应无效：%s", i+1, w.Body)
		}
		if expected == nil {
			if body.YunbeiBalance != nil || body.Errors.Yunbei == "" || !strings.Contains(w.Body.String(), `"yunbeiBalance":null`) {
				t.Fatalf("失效或缺失的余额不能保留旧值或变成零：%s", w.Body)
			}
		} else if body.YunbeiBalance == nil || *body.YunbeiBalance != *expected || body.Errors.Yunbei != "" {
			t.Fatalf("真实余额（包括零）应保持已知：%s", w.Body)
		}
	}
	configAfter, err := os.ReadFile(s.config.path)
	if err != nil || string(configBefore) != string(configAfter) || calls != len(results) {
		t.Fatal("余额刷新必须每次查询且不能改写账号配置")
	}
}

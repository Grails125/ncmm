package weapi

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/pkg/cookie"
)

func TestYunBeiAvailableBalanceUsesUserPointAndDistinguishesUnknown(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       int64
		wantErr    bool
	}{
		{"非零余额", `{"code":200,"userPoint":{"balance":24680},"data":{"balance":0}}`, 200, 24680, false},
		{"真实零余额", `{"code":200,"userPoint":{"balance":0}}`, 200, 0, false},
		{"字段缺失", `{"code":200,"userPoint":{}}`, 200, 0, true},
		{"账户缺失", `{"code":200}`, 200, 0, true},
		{"空余额", `{"code":200,"userPoint":{"balance":null}}`, 200, 0, true},
		{"不回退商城余额", `{"code":200,"data":{"balance":99}}`, 200, 0, true},
		{"服务失败", `{"code":301,"message":"unit-private","userPoint":{"balance":123}}`, 200, 0, true},
		{"HTTP失败", `{"code":500,"message":"unit-private"}`, 500, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := api.NewClient(&api.Config{Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close(context.Background())
			calls := 0
			client.GetClient().Transport = partnerTestTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != http.MethodPost || req.URL.Host != "music.163.com" || req.URL.Path != "/weapi/v1/user/info" {
					t.Fatalf("余额请求使用了错误来源: %s %s", req.Method, req.URL.Path)
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: req}, nil
			})
			got, err := New(client).YunBeiAvailableBalance(t.Context())
			if (err != nil) != tc.wantErr || got != tc.want || calls != 1 {
				t.Fatalf("余额=%d，error=%v，请求次数=%d", got, err, calls)
			}
			if err != nil && strings.Contains(err.Error(), "unit-private") {
				t.Fatal("余额错误泄漏服务端正文")
			}
		})
	}
}

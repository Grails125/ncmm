package ncmm

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/weapi"
	cookiepkg "github.com/3899/ncmm/pkg/cookie"
	"github.com/spf13/cobra"
)

func TestYunbeiEarningsUsesUserPointBalance(t *testing.T) {
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
			client, err := api.NewClient(&api.Config{Cookie: cookiepkg.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close(context.Background())
			calls := 0
			client.GetClient().Transport = musicPartnerTestTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Path != "/weapi/v1/user/info" {
					t.Fatalf("收益查询使用了错误余额来源: %s", req.URL.Path)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: req}, nil
			})
			command := &cobra.Command{}
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			sign := &SignIn{cmd: command}
			got := sign.fetchYunbeiEarnings(t.Context(), client, weapi.New(client), 0)
			if got.Balance != tc.want || got.BalanceKnown != tc.known || got.TodayKnown || calls != 1 {
				t.Fatalf("收益余额状态错误: %+v，请求次数=%d", got, calls)
			}
			if !tc.known && !strings.Contains(yunbeiEarningsLine(got), "当前余额 --") {
				t.Fatal("未知余额被显示为零")
			}
		})
	}
}

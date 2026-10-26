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

func TestVipLotteryModuleListPreservesFreeAndUsedState(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		free       int64
		used       bool
		missing    bool
	}{
		{"未许愿", `{"code":200,"data":{"vip":true,"thisDayCanUseFreeCount":1,"useEveryDayFreeCount":false,"activityBaseInfo":{"prizeName":"测试奖品","serviceTimestamp":1792991205147,"startTime":1739808000000,"endTime":1930060799000,"awardTimestamp":1793498400208},"activityProcessInfo":{"prizeTableId":1001,"status":0,"visitorParticipateNum":0}}}`, 1, false, false},
		{"已许愿", `{"code":200,"data":{"vip":true,"thisDayCanUseFreeCount":0,"useEveryDayFreeCount":true}}`, 0, true, false},
		{"未知次数", `{"code":200,"data":{"vip":true}}`, 0, false, true},
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
				if req.Method != http.MethodPost || req.URL.Host != "music.163.com" || req.URL.Path != "/weapi/vipnewcenter/app/lottery/module/list" {
					t.Fatal("错误查询路由")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: req}, nil
			})
			got, err := New(client).VipLotteryModuleList(t.Context())
			if err != nil || calls != 1 || got == nil || got.Code != 200 || got.Data == nil {
				t.Fatalf("响应无效: %#v, %v", got, err)
			}
			if tc.missing {
				if got.Data.ThisDayCanUseFreeCount != nil || got.Data.UseEveryDayFreeCount != nil {
					t.Fatal("缺失状态被补成零值")
				}
				return
			}
			if got.Data.ThisDayCanUseFreeCount == nil || *got.Data.ThisDayCanUseFreeCount != tc.free || got.Data.UseEveryDayFreeCount == nil || *got.Data.UseEveryDayFreeCount != tc.used {
				t.Fatal("状态不匹配")
			}
		})
	}
}

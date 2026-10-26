package eapi

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/weapi"
)

func TestVipConfirmationWritesDoNotRetryOrExposeTransportError(t *testing.T) {
	for _, task := range []string{"sign", "reward", "legacy-sign", "legacy-reward"} {
		t.Run(task, func(t *testing.T) {
			cfg := &api.Config{Retry: 2}
			cfg.Cookie.Filepath = filepath.Join(t.TempDir(), "cookie.json")
			client, err := api.NewClient(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })
			calls := 0
			client.GetClient().Transport = qrLoginTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, fmt.Errorf("synthetic-vip-private-error")
			})
			if task == "legacy-sign" {
				_, err = weapi.New(client).VipTaskSign(t.Context(), &weapi.VipTaskSignReq{})
			} else if task == "legacy-reward" {
				_, err = weapi.New(client).VipRewardGetAll(t.Context(), &weapi.VipRewardGetAllReq{})
			} else if task == "sign" {
				_, err = New(client).VipTaskSign(t.Context(), &VipTaskSignReq{})
			} else {
				_, err = New(client).VipRewardGetAll(t.Context(), &VipRewardGetAllReq{})
			}
			if err == nil || calls != 1 || strings.Contains(err.Error(), "synthetic-vip-private-error") {
				t.Fatalf("write must not retry or expose transport details: calls=%d err=%v", calls, err)
			}
		})
	}
}

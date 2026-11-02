package ncmm

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/config"
)

type vipSongLikeReceiptFake struct {
	reply  *eapi.SongLikeResp
	err    error
	cancel context.CancelFunc
	calls  int
}

func (f *vipSongLikeReceiptFake) SongLike(context.Context, *eapi.SongLikeReq) (*eapi.SongLikeResp, error) {
	f.calls++
	f.cancel()
	return f.reply, f.err
}

func TestVipSongLikeRequiresAcceptedReceipt(t *testing.T) {
	for _, test := range []struct {
		name  string
		reply *eapi.SongLikeResp
		err   error
		want  string
	}{
		{name: "business-403", reply: &eapi.SongLikeResp{Code: 403}, want: "服务码=403"},
		{name: "business-405", reply: &eapi.SongLikeResp{Code: 405}, want: "服务码=405"},
		{name: "nil-receipt", want: "回执缺失"},
		{name: "missing-code", reply: &eapi.SongLikeResp{}, want: "服务码=0"},
		{name: "transport-error", err: errors.New("fixture-token-do-not-log"), want: "请求失败"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newVipConfirmationClient(t, func(path string) string {
				if path != "/api/v6/playlist/detail" {
					t.Fatalf("意外接口：%s", path)
				}
				return `{"code":200,"playlist":{"trackIds":[{"id":123}]}}`
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fake := &vipSongLikeReceiptFake{reply: test.reply, err: test.err, cancel: cancel}
			command := NewSign(&Root{Cfg: &config.Config{}}, nil)
			var output bytes.Buffer
			command.cmd.SetOut(&output)
			ids := command.doVipSongLike(ctx, weapi.New(client), fake)
			if len(ids) != 0 || fake.calls != 1 || !strings.Contains(output.String(), test.want) {
				t.Fatalf("未接受的红心请求不能计入成功：ids=%v calls=%d output=%s", ids, fake.calls, output.String())
			}
			for _, invalid := range []string{"已红心", "成功: 1", "fixture-token-do-not-log"} {
				if strings.Contains(output.String(), invalid) {
					t.Fatalf("误报成功或泄漏错误：%s", output.String())
				}
			}
		})
	}
}

func TestVipSongLikeHTTP200RejectionIsNotAccepted(t *testing.T) {
	for _, body := range []string{`{"code":403}`, `{"code":405}`, `{}`, `null`} {
		t.Run(body, func(t *testing.T) {
			likes := 0
			client := newVipConfirmationClient(t, func(path string) string {
				switch path {
				case "/api/v6/playlist/detail":
					return `{"code":200,"playlist":{"trackIds":[{"id":123}]}}`
				case "/eapi/song/like":
					likes++
					return body
				default:
					t.Fatalf("意外接口：%s", path)
					return ""
				}
			})
			command := NewSign(&Root{Cfg: &config.Config{}}, nil)
			var output bytes.Buffer
			command.cmd.SetOut(&output)
			if ids := command.doVipSongLike(t.Context(), weapi.New(client), eapi.New(client)); len(ids) != 0 || likes != 1 {
				t.Fatalf("HTTP 200 不能代替业务回执：ids=%v likes=%d output=%s", ids, likes, output.String())
			}
			if !strings.Contains(output.String(), "未获接受") || strings.Contains(output.String(), "请求已接受（服务码=200）") {
				t.Fatalf("错误回执被当作接受：%s", output.String())
			}
		})
	}
}

func TestVipSongLikeAcceptedReceiptDoesNotClaimNewLikeOrCompletion(t *testing.T) {
	client := newVipConfirmationClient(t, func(path string) string {
		if path != "/api/v6/playlist/detail" {
			t.Fatalf("意外接口：%s", path)
		}
		return `{"code":200,"playlist":{"trackIds":[{"id":123}]}}`
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fake := &vipSongLikeReceiptFake{reply: &eapi.SongLikeResp{Code: 200}, cancel: cancel}
	command := NewSign(&Root{Cfg: &config.Config{}}, nil)
	var output bytes.Buffer
	command.cmd.SetOut(&output)
	ids := command.doVipSongLike(ctx, weapi.New(client), fake)
	if fake.calls != 1 || len(ids) != 0 || !strings.Contains(output.String(), "请求已接受（服务码=200）") || !strings.Contains(output.String(), "不据此确认新增红心或任务完成") {
		t.Fatalf("接受回执不能推断新增红心或扩大取消后的清理集合：ids=%v output=%s", ids, output.String())
	}
}

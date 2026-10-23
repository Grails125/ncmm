package ncmm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	cookiepkg "github.com/3899/ncmm/pkg/cookie"
	"github.com/3899/ncmm/pkg/log"
)

func newYunbeiFollowTestAPI(t *testing.T, handler func(*http.Request) string) *eapi.Api {
	t.Helper()
	client, err := api.NewClient(&api.Config{Cookie: cookiepkg.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, log.Default)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	client.GetClient().Transport = musicianVipClaimTransport(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(handler(request))), Request: request}, nil
	})
	return eapi.New(client)
}

func TestYunbeiFollowNeverChangesExistingOrUnknownSubscriptions(t *testing.T) {
	for _, tc := range []struct{ name, hot string }{
		{"already-followed", `{"code":200,"data":[{"artists":[{"id":1,"name":"已有歌手","followed":true}]}]}`},
		{"missing-state", `{"code":200,"data":[{"artists":[{"id":1,"name":"状态未知"}]}]}`},
		{"null-state", `{"code":200,"data":[{"artists":[{"id":1,"name":"状态未知","followed":null}]}]}`},
		{"invalid-id", `{"code":200,"data":[{"artists":[{"id":0,"name":"无效歌手","followed":false}]}]}`},
		{"empty", `{"code":200,"data":[]}`},
		{"query-rejected", `{"code":503,"data":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutations := 0
			request := newYunbeiFollowTestAPI(t, func(r *http.Request) string {
				if r.URL.Path == "/eapi/artist/hot" {
					return tc.hot
				}
				mutations++
				return `{"code":500,"message":"test rejection"}`
			})
			command := NewSign(nil, nil)
			var output bytes.Buffer
			command.cmd.SetOut(&output)
			command.doFollowArtist(t.Context(), request)
			if mutations != 0 {
				t.Fatalf("状态不明确或已有关注时不得修改收藏，实际写请求 %d 次", mutations)
			}
			if strings.Contains(output.String(), "成功") || strings.Contains(output.String(), "操作完毕") {
				t.Fatalf("跳过不得伪报完成: %s", output.String())
			}
		})
	}
}

func TestYunbeiFollowSearchesEveryGroupForExplicitUnfollowedArtist(t *testing.T) {
	request := newYunbeiFollowTestAPI(t, func(r *http.Request) string {
		if r.URL.Path == "/eapi/artist/hot" {
			return `{"code":200,"data":[{"artists":[{"id":1,"name":"已有歌手","followed":true}]},{"artists":[{"id":2,"name":"新歌手","followed":false}]}]}`
		}
		return `{"code":500,"message":"test rejection"}`
	})
	command := NewSign(nil, nil)
	var output bytes.Buffer
	command.cmd.SetOut(&output)
	command.doFollowArtist(t.Context(), request)
	if !strings.Contains(output.String(), "新歌手") || strings.Contains(output.String(), "已有歌手") {
		t.Fatalf("只能选择后续分组中明确未关注的歌手: %s", output.String())
	}
}

func TestYunbeiFollowOnlyCleansUpItsOwnSuccessfulSubscription(t *testing.T) {
	for _, tc := range []struct {
		name               string
		subCode, unsubCode int
		cancelAfterSub     bool
	}{
		{"success", 200, 200, false},
		{"subscribe-rejected", 403, 200, false},
		{"cleanup-rejected", 200, 500, false},
		{"cancel-cleans-up", 200, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			subCalls, unsubCalls := 0, 0
			request := newYunbeiFollowTestAPI(t, func(r *http.Request) string {
				switch r.URL.Path {
				case "/weapi/artist/sub":
					subCalls++
					if tc.cancelAfterSub {
						cancel()
					}
					return fmt.Sprintf(`{"code":%d}`, tc.subCode)
				case "/weapi/artist/unsub":
					unsubCalls++
					deadline, ok := r.Context().Deadline()
					if r.Context().Err() != nil || !ok || time.Until(deadline) > 5*time.Second {
						t.Error("清理必须使用可用且有界的上下文")
					}
					return fmt.Sprintf(`{"code":%d}`, tc.unsubCode)
				default:
					t.Fatalf("不应调用其他接口: %s", r.URL.Path)
					return ""
				}
			})
			command := NewSign(nil, nil)
			var output bytes.Buffer
			command.cmd.SetOut(&output)
			followed := false
			delay := time.Duration(0)
			if tc.cancelAfterSub {
				delay = time.Hour
			}
			started := time.Now()
			command.followYunbeiArtist(ctx, request, eapi.ArtistHotRespArtist{Id: 2, Name: "新歌手", Followed: &followed}, delay)
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("取消必须及时结束等待: %s", elapsed)
			}
			wantUnsub := 0
			if tc.subCode == 200 {
				wantUnsub = 1
			}
			if subCalls != 1 || unsubCalls != wantUnsub {
				t.Fatalf("仅清理本次明确新增成功的关注: sub=%d unsub=%d", subCalls, unsubCalls)
			}
			completed := strings.Contains(output.String(), "操作及清理已完成")
			if want := tc.subCode == 200 && tc.unsubCode == 200 && !tc.cancelAfterSub; completed != want {
				t.Fatalf("失败或取消不能伪报完成: %s", output.String())
			}
			if tc.cancelAfterSub && !strings.Contains(output.String(), "任务已取消") {
				t.Fatalf("应说明已取消: %s", output.String())
			}
		})
	}
}

func TestYunbeiFollowCancellationBeforeStartingDoesNotCallAPI(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	command := NewSign(nil, nil)
	var output bytes.Buffer
	command.cmd.SetOut(&output)
	command.doFollowArtist(ctx, nil)
	if !strings.Contains(output.String(), "取消") {
		t.Fatalf("应提示取消: %s", output.String())
	}
}

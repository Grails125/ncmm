package ncmm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/config"
	cookiepkg "github.com/3899/ncmm/pkg/cookie"
)

func playMediaClient(t *testing.T, transport http.RoundTripper) *api.Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cookie.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := api.NewClient(&api.Config{Cookie: cookiepkg.Config{Filepath: path, ReadOnly: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	client.GetClient().Transport = transport
	return client
}

func TestPlayIdsAudioUsesTLSNoCookiesAndAcceptsChunkedBody(t *testing.T) {
	client := playMediaClient(t, playIdsRoleTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.Header.Get("Cookie") != "" {
			t.Errorf("CDN 必须使用 HTTPS，且不发送会话 Cookie")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"audio/mpeg"}}, ContentLength: -1, Body: io.NopCloser(strings.NewReader("audio")), Request: r}, nil
	}))
	u, _ := url.Parse("https://m804.music.126.net/")
	client.SetCookies(u, []*http.Cookie{{Name: "MUSIC_U", Value: "private-session", Path: "/"}})
	if err := downloadAudioToBuffer(t.Context(), client, "http://m804.music.126.net/song?authSecret=private"); err != nil {
		t.Fatalf("完整分块音频不能误报中断：%v", err)
	}
}

func TestPlayIdsAudioErrorDoesNotLeakSignedURL(t *testing.T) {
	client := playMediaClient(t, playIdsRoleTransport(func(r *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: "https://m804.music.126.net/private?authSecret=private", Err: errors.New("private")}
	}))
	err := downloadAudioToBuffer(t.Context(), client, "http://m804.music.126.net/private?authSecret=private")
	if err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "authSecret") {
		t.Fatalf("必须输出安全诊断：%v", err)
	}
}

func TestPlayIdsRequiresAudioBeforeReportingPlayback(t *testing.T) {
	for _, scenario := range []string{"unavailable", "success", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			posts, audioAttempts := 0, 0
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			client := playMediaClient(t, playIdsRoleTransport(func(r *http.Request) (*http.Response, error) {
				body := ""
				switch r.URL.Path {
				case "/weapi/w/nuser/account/get":
					body = `{"code":200,"account":{"id":10},"profile":{"userId":10,"nickname":"测试"}}`
				case "/weapi/v3/song/detail":
					body = `{"code":200,"songs":[{"id":123,"name":"测试歌曲","dt":1000,"al":{"id":1},"ar":[{"id":200}]}]}`
				case "/eapi/nmusician/workbench/musician/role/get":
					body = `{"code":200,"data":{"userId":10,"isMusician":false}}`
				case "/weapi/song/enhance/player/url/v1":
					body = `{"code":200,"data":[{"id":123,"url":"http://m804.music.126.net/audio"}]}`
				case "/audio":
					audioAttempts++
					if scenario != "unavailable" {
						if scenario == "cancelled" {
							cancel()
						}
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"audio/mpeg"}}, ContentLength: -1, Body: io.NopCloser(strings.NewReader("audio")), Request: r}, nil
					}
					return &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				case "/weapi/feedback/weblog":
					posts++
					if scenario == "unavailable" {
						cancel()
					}
					body = `{"code":200}`
				default:
					t.Fatalf("意外接口：%s", r.URL.Path)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			}))
			c := NewPlayIds(&Root{Cfg: &config.Config{}}, nil)
			c.opts.DailyMin, c.opts.DailyMax, c.opts.RunMin, c.opts.RunMax = 1, 1, 1, 1
			c.opts.DisableMixPlay = true
			c.sharedDB = newPlayStatsMemoryDB()
			if err := c.sharedDB.Set(ctx, playIdsDailyTargetKey("10"), "1"); err != nil {
				t.Fatal(err)
			}
			if err := c.sharedDB.Set(ctx, playIdsTodayNumKey("10"), "0"); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			count, err := c.executeForClient(ctx, client, "account", []string{"123"})
			if scenario == "success" {
				if err != nil || count != 1 || posts != 1 || audioAttempts != 1 {
					t.Fatalf("成功音频应正常上报：count=%d posts=%d err=%v", count, posts, err)
				}
			} else {
				if err == nil || count != 0 || posts != 0 || audioAttempts == 0 || time.Since(start) >= time.Second {
					t.Fatalf("无音频或已取消时不得等待整首、计数、上报：count=%d posts=%d duration=%s err=%v", count, posts, time.Since(start), err)
				}
				value, _ := c.sharedDB.Get(t.Context(), playIdsTodayNumKey("10"))
				if string(value) != "0" {
					t.Fatalf("未完成播放不能更新今日数量：%s", value)
				}
				if scenario == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatalf("未保留取消状态：%v", err)
				}
			}
		})
	}
}

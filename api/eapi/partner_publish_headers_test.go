package eapi

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/pkg/log"
)

func TestPartnerIOSIdentityFromCommonUA(t *testing.T) {
	for _, tc := range []struct {
		ua, os, osVersion, appVersion, buildVersion string
	}{
		{"NeteaseMusic 9.4.95/6806 (iPhone; iOS 16.6.1; zh_CN)", "iPhone OS", "16.6.1", "9.4.95", "6806"},
		{"NeteaseMusic/9.4.95/6806 (iPhone; iOS 16.6.1)", "iPhone OS", "16.6.1", "9.4.95", "6806"},
		{"neteasemusic/9.4.95.6806 (iPad; iOS 16.6.1; Scale/2.00)", "iPad", "16.6.1", "9.4.95", "6806"},
		{"NeteaseMusic/9.4.95.6806 (iPhone; CPU iPhone OS 16_6_1 like Mac OS X)", "iPhone OS", "16.6.1", "9.4.95", "6806"},
		{"NeteaseMusic/9.5.37.9000 (Android; 15)", "", "", "", ""},
	} {
		t.Run(tc.ua, func(t *testing.T) {
			identity := partnerIOSIdentityFromUA(tc.ua)
			if identity.os != tc.os || identity.osVersion != tc.osVersion || identity.appVersion != tc.appVersion || identity.buildVersion != tc.buildVersion {
				t.Fatalf("所选 UA 身份解析错误：%+v", identity)
			}
		})
	}
}

func TestPartnerPublishTransportUsesSelectedIOSIdentity(t *testing.T) {
	const ua = "NeteaseMusic 9.4.95/6806 (iPhone; iOS 16.6.1; zh_CN)"
	for _, pcCookie := range []bool{false, true} {
		for _, publishNote := range []bool{false, true} {
			name := "comment"
			if publishNote {
				name = "note"
			}
			if pcCookie {
				name += "/pc_cookie"
			} else {
				name += "/no_os_cookie"
			}
			t.Run(name, func(t *testing.T) {
				cfg := &api.Config{Retry: 3, UserAgent: api.UserAgentConfig{EAPI: ua}}
				cfg.Cookie.Filepath = filepath.Join(t.TempDir(), "cookie.json")
				client, err := api.NewClient(cfg, log.Default)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = client.Close(context.Background()) })
				origin, _ := url.Parse("https://music.163.com/")
				cookies := []*http.Cookie{{Name: "deviceId", Value: "unit-test-device", Path: "/"}, {Name: "MUSIC_U", Value: "unit-test-session", Path: "/"}, {Name: "__csrf", Value: "unit-test-csrf", Path: "/"}}
				if pcCookie {
					cookies = append(cookies, &http.Cookie{Name: "os", Value: "pc", Path: "/"}, &http.Cookie{Name: "osver", Value: "Windows 11", Path: "/"}, &http.Cookie{Name: "appver", Value: "2.3.17", Path: "/"}, &http.Cookie{Name: "buildver", Value: "1034", Path: "/"})
				}
				client.SetCookies(origin, cookies)
				calls := 0
				client.GetClient().Transport = nosTestTransport(func(request *http.Request) (*http.Response, error) {
					calls++
					for header, expected := range map[string]string{"x-os": "iPhone OS", "x-osver": "16.6.1", "x-appver": "9.4.95", "x-buildver": "6806", "User-Agent": ua, "x-deviceid": "unit-test-device", "x-music-u": "unit-test-session"} {
						if request.Header.Get(header) != expected {
							t.Errorf("推荐发布身份头 %s 不匹配：got=%q want=%q", header, request.Header.Get(header), expected)
						}
					}
					expectedPath := "/eapi/v1/resource/comments/add"
					if publishNote {
						expectedPath = "/eapi/note/share/friends/resource"
					}
					if request.URL.Path != expectedPath {
						t.Fatal("推荐发布协议不正确")
					}
					var payload struct {
						Header partnerIOSPublishHeader `json:"header"`
					}
					nosTestEAPIPayload(t, request, &payload)
					if payload.Header.OS != request.Header.Get("x-os") || payload.Header.OSVersion != request.Header.Get("x-osver") || payload.Header.AppVersion != request.Header.Get("x-appver") || payload.Header.Build != request.Header.Get("x-buildver") || payload.Header.DeviceID != request.Header.Get("x-deviceid") {
						t.Fatal("加密 header 和 HTTP 头必须共享同一移动身份")
					}
					if payload.Header.MusicU != "unit-test-session" || payload.Header.CSRF != "unit-test-csrf" {
						t.Fatal("加密 header 只能沿用当前会话的登录和 CSRF")
					}
					return nosTestResponse(request, 200, `{"code":200,"id":901,"event":{"id":901},"comment":{"commentId":456}}`), nil
				})
				endpoint := New(client)
				if publishNote {
					_, err = endpoint.PartnerNotePublish(t.Context(), partnerNoteTestRequest())
				} else {
					_, err = endpoint.PartnerCommentAdd(t.Context(), &PartnerCommentReq{ResourceID: 123, Content: "正文", CryptoMode: api.CryptoModeEAPI})
				}
				if err != nil || calls != 1 || client.UserAgent(api.CryptoModeEAPI) != ua {
					t.Fatalf("推荐应保持所选 UA 并只请求一次：calls=%d err=%v", calls, err)
				}
				if pcCookie && vipMemberGiftCookieValue(client, "os") != "pc" {
					t.Fatal("适配请求头不应回写账号 Cookie")
				}
			})
		}
	}
}

func TestPartnerPublishOnlyFallsBackToIOSCookieMetadata(t *testing.T) {
	for _, cookieOS := range []string{"pc", "iPad"} {
		t.Run(cookieOS, func(t *testing.T) {
			cfg := &api.Config{UserAgent: api.UserAgentConfig{EAPI: "neteasemusic/9.4.95 (iPad; iOS 16.6.1; Scale/2.00)"}}
			cfg.Cookie.Filepath = filepath.Join(t.TempDir(), "cookie.json")
			client, err := api.NewClient(cfg, log.Default)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })
			origin, _ := url.Parse("https://music.163.com/")
			client.SetCookies(origin, []*http.Cookie{{Name: "os", Value: cookieOS, Path: "/"}, {Name: "buildver", Value: "6806", Path: "/"}})
			opts := api.NewOptions()
			var payloadHeader interface{}
			ok := New(client).partnerPublishHeaders(opts, api.CryptoModeEAPI, &payloadHeader)
			if ok != (cookieOS == "iPad") {
				t.Fatal("缺少 UA 字段时只能继承 iOS Cookie 身份")
			}
			if ok && (opts.Headers["x-os"] != "iPad" || opts.Headers["x-buildver"] != "6806") {
				t.Fatal("未使用匹配的 iOS 补充字段")
			}
		})
	}
}

func TestPartnerEncryptedSessionHeaderStaysPrivate(t *testing.T) {
	const secret = "private-encrypted-partner-session"
	directory := t.TempDir()
	logPath := filepath.Join(directory, "debug.log")
	logConfig := &log.Config{Level: "debug"}
	logConfig.Rotate.Filename = logPath
	logger := log.New(logConfig)
	previous := log.Default
	log.Default = logger
	t.Cleanup(func() { log.Default = previous; _ = logger.Close() })
	cfg := &api.Config{Debug: true, Retry: 5}
	cfg.Cookie.Filepath = filepath.Join(directory, "cookie.json")
	client, err := api.NewClient(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	origin, _ := url.Parse("https://music.163.com/")
	client.SetCookies(origin, []*http.Cookie{{Name: "MUSIC_U", Value: secret, Path: "/"}, {Name: "__csrf", Value: secret, Path: "/"}, {Name: "deviceId", Value: "unit-test-device", Path: "/"}})
	calls := 0
	client.GetClient().Transport = nosTestTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		var payload struct {
			Header partnerIOSPublishHeader `json:"header"`
		}
		nosTestEAPIPayload(t, request, &payload)
		if payload.Header.MusicU != secret || payload.Header.CSRF != secret {
			t.Fatal("应使用当前会话的真实凭据")
		}
		return nosTestResponse(request, 503, `{"code":503,"message":"`+secret+`"}`), nil
	})
	endpoint := New(client)
	for _, publish := range []func() error{
		func() error { _, err := endpoint.PartnerNotePublish(t.Context(), partnerNoteTestRequest()); return err },
		func() error {
			_, err := endpoint.PartnerCommentAdd(t.Context(), &PartnerCommentReq{ResourceID: 123, Content: "正文", CryptoMode: api.CryptoModeEAPI})
			return err
		},
	} {
		if err := publish(); err == nil || strings.Contains(err.Error(), secret) {
			t.Fatal("发布失败不得回显内层会话凭据")
		}
	}
	if calls != 2 {
		t.Fatal("两个发布请求均不得自动重试")
	}
	_ = logger.Close()
	content, _ := os.ReadFile(logPath)
	if strings.Contains(string(content), secret) {
		t.Fatal("调试日志不得包含加密前 header 的登录或 CSRF 凭据")
	}
}

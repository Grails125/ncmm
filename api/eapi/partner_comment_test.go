package eapi

import (
	"bytes"
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/xeapi"
	"github.com/3899/ncmm/pkg/cookie"
	"github.com/3899/ncmm/pkg/crypto"
	"github.com/3899/ncmm/pkg/log"
	"gopkg.in/natefinch/lumberjack.v2"
)

func TestPartnerCommentPayload(t *testing.T) {
	req := &PartnerCommentReq{ResourceID: 9007199254740993, Content: "  测试乐评  ", CheckToken: " transient-token "}
	payload, err := newPartnerCommentPayload(req)
	if err != nil {
		t.Fatal(err)
	}
	if payload.ThreadID != "R_SO_4_9007199254740993" || payload.Content != "测试乐评" || payload.Source != `{"type":16}` || payload.CheckToken != "transient-token" || !payload.ER {
		t.Fatal("歌曲线程、乐评内容或合伙人入口参数错误")
	}
	if req.Content != "  测试乐评  " || req.CheckToken != " transient-token " {
		t.Fatal("不得修改调用方的原始请求")
	}
	for _, invalid := range []*PartnerCommentReq{nil, {}, {ResourceID: -1, Content: "有效内容"}, {ResourceID: 1, Content: " \n "}, {ResourceID: 1, Content: strings.Repeat("字", 501)}} {
		if _, err := newPartnerCommentPayload(invalid); err == nil {
			t.Fatal("错误的歌曲 ID 或空白、超长评论不得发出请求")
		}
	}
}

func TestPartnerCommentEAPIUsesIOSIdentityAndDoesNotRetry(t *testing.T) {
	for _, status := range []int{200, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			client, err := api.NewClient(&api.Config{Retry: 5, UserAgent: api.UserAgentConfig{EAPI: "NeteaseMusic 9.4.95/6806 (iPhone; iOS 16.6.1; zh_CN)"}, Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, log.Default)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })
			baseURL, _ := url.Parse("https://music.163.com/")
			client.SetCookies(baseURL, []*http.Cookie{{Name: "deviceId", Value: "unit-test-ios-device", Path: "/"}})
			calls := 0
			client.GetClient().Transport = nosTestTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Host != "interface3.music.163.com" || req.URL.Path != "/eapi/v1/resource/comments/add" || req.Header.Get("x-os") != "iPhone OS" || !strings.Contains(req.UserAgent(), "iPhone") {
					t.Fatal("iOS 乐评必须使用匹配的 EAPI 地址和身份")
				}
				if req.Header.Get("X-antiCheatToken") != "short-token" {
					t.Fatal("缺少用户配置的短效凭证")
				}
				if err := req.ParseForm(); err != nil {
					t.Fatal(err)
				}
				if req.PostForm.Get("B") != "" || req.PostForm.Get("params") == "" {
					t.Fatal("EAPI 不能混用 XEAPI 载荷")
				}
				plain, err := crypto.EApiDecrypt(req.PostForm.Get("params"), "hex")
				if err != nil {
					t.Fatal(err)
				}
				parts := strings.Split(string(plain), "-36cd479b6b5-")
				if len(parts) != 3 || parts[0] != "/api/v1/resource/comments/add" {
					t.Fatal("EAPI 签名路径错误")
				}
				var payload partnerCommentPayload
				if err := json.Unmarshal([]byte(parts[1]), &payload); err != nil {
					t.Fatal(err)
				}
				if payload.ThreadID != "R_SO_4_123" || payload.Content != "测试乐评" || payload.Source != `{"type":16}` || payload.OS != "iOS" || payload.DeviceID == "" || payload.VerifyID != 1 || payload.CheckToken != "short-token" {
					t.Fatal("iOS 乐评业务和设备字段不正确")
				}
				if _, ok := payload.Header.(map[string]interface{}); !ok {
					t.Fatal("iOS header 应为对象")
				}
				body := `{"code":200,"comment":{"commentId":456}}`
				if status != 200 {
					body = `{"code":503,"message":"private-response"}`
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})
			reply, err := New(client).PartnerCommentAdd(t.Context(), &PartnerCommentReq{ResourceID: 123, Content: "测试乐评", CheckToken: "short-token", CryptoMode: api.CryptoModeEAPI})
			if calls != 1 || (err != nil) != (status != 200) {
				t.Fatalf("发布不能自动重发，calls=%d err=%v", calls, err)
			}
			if status == 200 && (reply == nil || reply.Comment.CommentID != 456) {
				t.Fatal("未保留真实乐评 ID")
			}
			if status != 200 {
				var failure *PartnerPublishError
				if !errors.As(err, &failure) || failure.NotSent || failure.Mode != api.CryptoModeEAPI || failure.HTTPStatus != status || failure.ServiceCode != 503 {
					t.Fatal("已发出的失败请求须保留协议和受控状态码")
				}
			}
		})
	}
}

func TestPartnerCommentOnlyMarksExplicitBusinessRejection(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		rejected   bool
	}{
		{"explicit", `{"code":400,"message":"private-message","msg":"private-msg","errDetail":"private-detail","needPolling":false}`, 200, true},
		{"comment_id", `{"code":400,"comment":{"commentId":456}}`, 200, false},
		{"root_id", `{"code":400,"id":456}`, 200, false},
		{"root_comment_id", `{"code":400,"commentId":456}`, 200, false},
		{"event_id", `{"code":400,"event":{"id":456}}`, 200, false},
		{"invalid_id", `{"code":400,"comment":{"commentId":-1}}`, 200, false},
		{"polling", `{"code":400,"needPolling":true}`, 200, false},
		{"http_failure", `{"code":400}`, 503, false},
		{"missing_success_id", `{"code":200}`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &api.Config{Retry: 5}
			cfg.Cookie.Filepath = filepath.Join(t.TempDir(), "cookie.json")
			client, err := api.NewClient(cfg, log.Default)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })
			calls := 0
			client.GetClient().Transport = nosTestTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				return nosTestResponse(request, tc.status, tc.body), nil
			})
			reply, err := New(client).PartnerCommentAdd(t.Context(), &PartnerCommentReq{ResourceID: 123, Content: "正文", CryptoMode: api.CryptoModeEAPI})
			var failure *PartnerPublishError
			if !errors.As(err, &failure) || failure.Rejected != tc.rejected || failure.NotSent || failure.HTTPStatus != tc.status || calls != 1 {
				t.Fatalf("明确拒绝与未知乐评区分错误：%v", err)
			}
			if tc.rejected && (failure.Phase != "业务拒绝" || failure.ServiceCode != 400) {
				t.Fatal("明确拒绝必须保留业务阶段和服务码")
			}
			if tc.name == "explicit" && (reply.Message != "private-message" || reply.Msg != "private-msg" || string(reply.ErrDetail) != `"private-detail"`) {
				t.Fatal("应保留响应消息供调用方受控提取")
			}
			if strings.Contains(err.Error(), "private-") {
				t.Fatal("发布错误不得回显响应中的隐私")
			}
		})
	}
}

func TestPartnerPublishingPreflightDoesNotCreateUnknownWrites(t *testing.T) {
	client, err := api.NewClient(&api.Config{Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, log.Default)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	calls := 0
	client.GetClient().Transport = nosTestTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		t.Fatal("本地预检失败不得访问网络")
		return nil, fmt.Errorf("private-network-detail")
	})
	endpoint := New(client)
	note := partnerNoteTestRequest()
	note.CryptoMode = api.CryptoModeXEAPI
	for _, operation := range []func() error{
		func() error {
			_, err := endpoint.PartnerCommentAdd(t.Context(), &PartnerCommentReq{ResourceID: 123, Content: "正文"})
			return err
		},
		func() error {
			_, err := endpoint.PartnerCommentAdd(t.Context(), &PartnerCommentReq{ResourceID: 123, Content: "正文", CryptoMode: api.CryptoModeWEAPI})
			return err
		},
		func() error { _, err := endpoint.PartnerCommentAdd(t.Context(), nil); return err },
		func() error { _, err := endpoint.PartnerNotePublish(t.Context(), note); return err },
		func() error { _, err := endpoint.PartnerNotePublish(t.Context(), nil); return err },
	} {
		err := operation()
		var failure *PartnerPublishError
		if !errors.As(err, &failure) || !failure.NotSent || failure.HTTPStatus != 0 || failure.ServiceCode != 0 {
			t.Fatalf("本地失败必须标记尚未发送：%v", err)
		}
	}
	if calls != 0 {
		t.Fatal("预检发生了网络写入")
	}
}

func TestPartnerPublishErrorDoesNotRetainUnderlyingSecrets(t *testing.T) {
	const private = "private-url-cookie-content"
	for _, source := range []error{fmt.Errorf("raw error %s", private), &api.HTTPStatusError{StatusCode: 403, ServiceCode: 301}, fmt.Errorf("wrapped cancellation %s: %w", private, context.Canceled)} {
		failure := partnerPublishFailure(t.Context(), api.CryptoModeEAPI, "请求或响应处理", false, source)
		if strings.Contains(fmt.Sprintf("%#v %s", failure, failure), private) {
			t.Fatal("不得在结构或文案中保留原始错误和隐私")
		}
		if errors.Is(source, context.Canceled) && !errors.Is(failure, context.Canceled) {
			t.Fatal("安全错误必须保留取消语义")
		}
	}
	failure := partnerPublishFailure(t.Context(), api.CryptoMode(private), private, true, fmt.Errorf("raw %s", private))
	if strings.Contains(fmt.Sprintf("%#v %s", failure, failure), private) {
		t.Fatal("未知协议和阶段不得把外部内容保存在诊断结构中")
	}
}

func TestPartnerCommentTransportAndResult(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		wantErr  bool
		wantID   int64
		wantCode int64
	}{
		{"success", 200, `{"code":200,"message":"发布成功","comment":{"commentId":9007199254740993,"threadId":"R_SO_4_123"}}`, false, 9007199254740993, 200},
		{"missing_id", 200, `{"code":200,"comment":{}}`, true, 0, 200},
		{"rejected", 200, `{"code":405,"message":"拒绝"}`, false, 0, 405},
		{"account_blocked", 200, `{"code":301,"message":"登录失效"}`, false, 0, 301},
		{"http_failure_no_retry", 503, `{"code":503}`, true, 0, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := api.NewClient(&api.Config{Retry: 3, UserAgent: api.UserAgentConfig{XEAPI: "partner-test"}, Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, log.Default)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })
			calls := 0
			client.GetClient().Transport = nosTestTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/gorilla/anti/crawler/security/key/get" {
					return partnerCommentTestKey(t, req), nil
				}
				calls++
				if req.Method != http.MethodPost || req.URL.Scheme != "https" || req.URL.Host != "interface3.music.163.com" || req.URL.Path != "/xeapi/v1/resource/comments/add" {
					t.Fatalf("乐评接口路径错误: %s %s", req.Method, req.URL)
				}
				if req.Header.Get("X-antiCheatToken") != "unit-test-short-token" || req.Referer() != "https://mp.music.163.com/" {
					t.Fatal("乐评请求缺少独立短效凭据或来源")
				}
				if err := req.ParseForm(); err != nil {
					t.Fatal(err)
				}
				if req.PostForm.Get("B") == "" || req.PostForm.Get("S") == "" || req.PostForm.Get("R") == "" || req.PostForm.Get("content") != "" {
					t.Fatal("乐评内容必须通过现有 XEAPI 加密传输")
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: req}, nil
			})
			reply, err := New(client).PartnerCommentAdd(t.Context(), &PartnerCommentReq{ResourceID: 123, Content: "测试乐评", CheckToken: "unit-test-short-token"})
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v，预期错误=%v", err, tc.wantErr)
			}
			if !tc.wantErr && (reply == nil || reply.Comment.CommentID != tc.wantID) {
				t.Fatal("必须返回服务端的真实评论 ID，且不能丢失整数精度")
			}
			if reply == nil || reply.Code != tc.wantCode {
				t.Fatalf("上层必须能识别业务响应码，预期 %d", tc.wantCode)
			}
			if calls != 1 {
				t.Fatalf("不可自动重发评论，实际请求 %d 次", calls)
			}
		})
	}
}

func TestPartnerCommentDeletePayload(t *testing.T) {
	req := &PartnerCommentDeleteReq{ResourceID: 9007199254740993, CommentID: 9007199254740995}
	payload, err := newPartnerCommentDeletePayload(req)
	if err != nil {
		t.Fatal(err)
	}
	if payload.ThreadID != "R_SO_4_9007199254740993" || payload.CommentID != req.CommentID {
		t.Fatal("删除必须使用指定歌曲线程和真实评论 ID，不能丢失整数精度")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 || fields["threadId"] == nil || fields["commentId"] == nil || fields["csrf_token"] == nil {
		t.Fatal("删除不得携带发布正文、来源或新增评论的短效令牌")
	}
	for _, invalid := range []*PartnerCommentDeleteReq{nil, {}, {ResourceID: -1, CommentID: 1}, {ResourceID: 1, CommentID: -1}, {ResourceID: 1}} {
		if _, err := newPartnerCommentDeletePayload(invalid); err == nil {
			t.Fatal("无效歌曲或评论 ID 不得发出删除请求")
		}
	}
}

func TestPartnerCommentDeleteTransport(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		wantCode int64
		wantErr  bool
	}{
		{"success", 200, `{"code":200}`, 200, false},
		{"denied", 200, `{"code":403,"message":"无权删除"}`, 403, false},
		{"account_blocked", 200, `{"code":301}`, 301, false},
		{"http_denied", 403, `{"code":301}`, 301, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := api.NewClient(&api.Config{Retry: 3, Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, log.Default)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })
			root, _ := url.Parse("https://music.163.com/")
			client.SetCookies(root, []*http.Cookie{{Name: "MUSIC_U", Value: "unit-test-account", Path: "/"}, {Name: "__csrf", Value: "unit-test-csrf", Path: "/"}})
			calls := 0
			client.GetClient().Transport = nosTestTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != http.MethodPost || req.URL.Scheme != "https" || req.URL.Host != "music.163.com" || req.URL.Path != "/weapi/resource/comments/delete" {
					t.Fatalf("必须使用既有官方网页删除接口: %s %s", req.Method, req.URL.Path)
				}
				if req.Header.Get("X-antiCheatToken") != "" || req.URL.Query().Get("csrf_token") != "unit-test-csrf" {
					t.Fatal("删除只使用当前账号会话及 CSRF，不携带发布短效令牌")
				}
				if ck, err := req.Cookie("MUSIC_U"); err != nil || ck.Value != "unit-test-account" {
					t.Fatal("删除必须使用当前账号会话")
				}
				if err := req.ParseForm(); err != nil || req.PostForm.Get("params") == "" || req.PostForm.Get("encSecKey") == "" || req.PostForm.Get("commentId") != "" {
					t.Fatal("删除必须使用 WEAPI 加密载荷")
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: req}, nil
			})
			reply, err := New(client).PartnerCommentDelete(t.Context(), &PartnerCommentDeleteReq{ResourceID: 123, CommentID: 456})
			if (err != nil) != tc.wantErr || reply == nil || reply.Code != tc.wantCode {
				t.Fatalf("删除结果必须保留业务响应码 %d，err=%v", tc.wantCode, err)
			}
			if calls != 1 {
				t.Fatalf("删除不得自动重试，实际请求 %d 次", calls)
			}
		})
	}
}

func TestPartnerCommentDeleteCancelAndSecrets(t *testing.T) {
	const secret = "private-comment-delete-session"
	logPath := filepath.Join(t.TempDir(), "debug.log")
	l := log.New(&log.Config{Level: "debug", Rotate: lumberjack.Logger{Filename: logPath}})
	previous := log.Default
	log.Default = l
	t.Cleanup(func() { log.Default = previous; _ = l.Close() })
	client, err := api.NewClient(&api.Config{Debug: true, Retry: 3, Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, l)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	root, _ := url.Parse("https://music.163.com/")
	client.SetCookies(root, []*http.Cookie{{Name: "MUSIC_U", Value: secret, Path: "/"}, {Name: "__csrf", Value: secret, Path: "/"}})
	calls := 0
	client.GetClient().Transport = nosTestTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("底层诊断包含 %s", secret)
	})
	apiClient := New(client)
	req := &PartnerCommentDeleteReq{ResourceID: 123, CommentID: 456}
	_, err = apiClient.PartnerCommentDelete(t.Context(), req)
	if err == nil || strings.Contains(err.Error(), secret) || calls != 1 {
		t.Fatal("删除失败不能泄露底层凭据或自动重试")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = apiClient.PartnerCommentDelete(ctx, req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("删除必须保留取消状态: %v", err)
	}
	_ = l.Close()
	raw, _ := os.ReadFile(logPath)
	if strings.Contains(string(raw), secret) {
		t.Fatal("删除请求不能把当前账号凭据写入调试日志")
	}
}

// 仅生成离线测试的公开密钥响应，整个 transport 不允许真实网络请求。
func partnerCommentTestKey(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}
	state := []byte(`{"publicKey":"3m5wN9om11qRESjEV+5EoFf9qLEylO6gyThMbl1XxEk=","version":"test","nextUpdateTime":0,"sk":"test-key"}`)
	key, _ := hex.DecodeString("ab1d5a430f6bb04a3f01e81ddd72bd916d5ce591248ac128714806d7f8fb1b84")
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	padding := aes.BlockSize - len(state)%aes.BlockSize
	state = append(state, bytes.Repeat([]byte{byte(padding)}, padding)...)
	encrypted := base64.StdEncoding.EncodeToString(crypto.AesEncryptECB(block, state))
	body, err := json.Marshal(map[string]any{"code": 200, "data": map[string]any{"encryptedData": encrypted, "timestamp": "1", "signature": xeapi.Sign("1", req.PostForm.Get("nonce"))}})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)), Request: req}
}

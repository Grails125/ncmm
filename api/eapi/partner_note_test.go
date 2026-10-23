package eapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/pkg/crypto"
	"github.com/3899/ncmm/pkg/log"
)

type partnerNoteTransport func(*http.Request) (*http.Response, error)

func (transport partnerNoteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return transport(req)
}

func partnerNoteTestRequest() *PartnerNotePublishReq {
	return &PartnerNotePublishReq{SongID: 123, UserID: 456, Title: "推荐歌曲", Content: "听见一首好歌", Pics: `[{"originId":"789","width":1080,"height":1440}]`, CryptoMode: api.CryptoModeEAPI}
}

func TestPartnerNotePublishUsesSongSourceAndPublicUploadedCover(t *testing.T) {
	cfg := &api.Config{Retry: 3}
	cfg.Cookie.Filepath = filepath.Join(t.TempDir(), "cookie.json")
	client, err := api.NewClient(cfg, log.Default)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	seen := 0
	client.GetClient().Transport = partnerNoteTransport(func(request *http.Request) (*http.Response, error) {
		seen++
		if request.URL.Host != "interface3.music.163.com" || request.URL.Path != "/eapi/note/share/friends/resource" {
			t.Fatal("推荐笔记使用了错误的发布接口")
		}
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		plain, err := crypto.EApiDecrypt(request.PostForm.Get("params"), "hex")
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(string(plain), "-36cd479b6b5-")
		if len(parts) != 3 {
			t.Fatal("EAPI 请求格式错误")
		}
		var payload DailySongSharePublishReq
		if err := json.Unmarshal([]byte(parts[1]), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Id != "123" || payload.ResourceID != "123" || payload.ThreadID != "R_SO_4_123" || payload.UID != "456" || payload.Type != "song" {
			t.Fatal("推荐笔记未关联当前账号和歌曲")
		}
		if payload.PrivacySetting != "0" || payload.SocialSpaceVisible != 1 || payload.Pics == "" || payload.Msg != "听见一首好歌" || payload.Title != "推荐歌曲" {
			t.Fatal("推荐笔记的公开范围或内容不正确")
		}
		if payload.ProduceInfo != "" || payload.AddComment || payload.ContentDeclaration != "" {
			t.Fatal("不应伪造官方模板渲染信息或顺带发表普通评论")
		}
		var source struct {
			BizCode         string `json:"bizCode"`
			EntranceCode    string `json:"entranceCode"`
			NeedReachNotice bool   `json:"needReachNotice"`
		}
		if err := json.Unmarshal([]byte(payload.PubSource), &source); err != nil || source.BizCode != "Music_Partner" || source.EntranceCode != "Publisher_Redirect" || !source.NeedReachNotice {
			t.Fatal("推荐笔记未携带抓包确认的合伙人来源")
		}
		var topics []partnerNoteActivity
		if err := json.Unmarshal([]byte(payload.ActivityInfoList), &topics); err != nil || len(topics) != 1 || topics[0].ID != "13827903" || topics[0].Type != 3 || topics[0].SubType != 11 || !topics[0].Selected || topics[0].CanChange {
			t.Fatal("默认合伙人话题不正确")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":200,"id":901,"event":{"id":901},"needPolling":false}`)), Request: request}, nil
	})
	result, err := New(client).PartnerNotePublish(context.Background(), partnerNoteTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.ConfirmedEventID()
	if err != nil || id != 901 || seen != 1 {
		t.Fatalf("发布结果未确认：id=%d requests=%d err=%v", id, seen, err)
	}
}

func TestPartnerNoteRejectsInvalidInputBeforeSending(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*PartnerNotePublishReq)
	}{
		{"song", func(req *PartnerNotePublishReq) { req.SongID = 0 }},
		{"account", func(req *PartnerNotePublishReq) { req.UserID = 0 }},
		{"blank title", func(req *PartnerNotePublishReq) { req.Title = " " }},
		{"blank content", func(req *PartnerNotePublishReq) { req.Content = " " }},
		{"long content", func(req *PartnerNotePublishReq) { req.Content = strings.Repeat("歌", 501) }},
		{"missing cover", func(req *PartnerNotePublishReq) { req.Pics = "[]" }},
		{"invalid cover", func(req *PartnerNotePublishReq) { req.Pics = `[{"originId":"0","width":1,"height":1}]` }},
		{"unknown protocol", func(req *PartnerNotePublishReq) { req.CryptoMode = api.CryptoModeWEAPI }},
		{"invalid topic", func(req *PartnerNotePublishReq) { req.Topics = []PartnerNoteTopic{{ID: "-1", Name: "话题", Type: 2}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := partnerNoteTestRequest()
			tc.change(req)
			if _, err := (&Api{}).PartnerNotePublish(context.Background(), req); err == nil {
				t.Fatal("无效请求应在访问客户端前拒绝")
			}
		})
	}
}

func TestPartnerNoteConfirmedEventIDRejectsAmbiguousResponses(t *testing.T) {
	for _, tc := range []struct {
		body string
		id   int64
	}{
		{`{"code":200,"id":12,"event":{"id":12}}`, 12},
		{`{"code":200,"id":12}`, 12},
		{`{"code":200,"event":{"id":12}}`, 12},
		{`{"code":200,"id":12,"needPolling":true}`, 0},
		{`{"code":200,"id":12,"event":{"id":13}}`, 0},
		{`{"code":200}`, 0},
		{`{"code":200,"id":-1}`, 0},
		{`{"code":403,"id":12}`, 0},
	} {
		var result PartnerNotePublishResp
		if err := json.Unmarshal([]byte(tc.body), &result); err != nil {
			t.Fatal(err)
		}
		id, err := result.ConfirmedEventID()
		if id != tc.id || (err == nil) != (tc.id > 0) {
			t.Fatalf("未正确识别发布状态：%s id=%d err=%v", tc.body, id, err)
		}
	}
}

func TestPartnerNoteFailureDoesNotRetryOrLogPrivateContent(t *testing.T) {
	directory := t.TempDir()
	logPath := filepath.Join(directory, "partner-note.log")
	logConfig := &log.Config{Level: "debug"}
	logConfig.Rotate.Filename = logPath
	logger := log.New(logConfig)
	previous := log.Default
	log.Default = logger
	log.Info("推荐笔记隐私测试")
	defer func() { log.Default = previous; _ = logger.Close() }()
	cfg := &api.Config{Retry: 5, Debug: true}
	cfg.Cookie.Filepath = filepath.Join(directory, "cookie.json")
	client, err := api.NewClient(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	seen := 0
	client.GetClient().Transport = partnerNoteTransport(func(request *http.Request) (*http.Response, error) {
		seen++
		return &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":503,"message":"synthetic-note-response-private"}`)), Request: request}, nil
	})
	req := partnerNoteTestRequest()
	req.Content = "synthetic-note-content-private"
	req.CheckToken = "synthetic-note-token-private"
	_, err = New(client).PartnerNotePublish(context.Background(), req)
	if err == nil || seen != 1 {
		t.Fatalf("笔记发布故障不得自动重发：requests=%d err=%v", seen, err)
	}
	logged, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, private := range []string{req.Content, req.CheckToken, "synthetic-note-response-private"} {
		if strings.Contains(string(logged), private) || strings.Contains(err.Error(), private) {
			t.Error("推荐笔记请求或错误泄露了私密内容")
		}
	}
}

func TestPartnerNoteUnknownReceiptRemainsSent(t *testing.T) {
	for _, body := range []string{`{"code":200}`, `{"code":200,"id":901,"needPolling":true}`, `{"code":200,"id":901,"event":{"id":902}}`} {
		t.Run(body, func(t *testing.T) {
			cfg := &api.Config{Retry: 5}
			cfg.Cookie.Filepath = filepath.Join(t.TempDir(), "cookie.json")
			client, err := api.NewClient(cfg, log.Default)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })
			calls := 0
			client.GetClient().Transport = partnerNoteTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})
			_, err = New(client).PartnerNotePublish(t.Context(), partnerNoteTestRequest())
			var failure *PartnerPublishError
			if !errors.As(err, &failure) || failure.NotSent || failure.Phase != "发布回执" || failure.Mode != api.CryptoModeEAPI || failure.HTTPStatus != 200 || failure.ServiceCode != 200 || calls != 1 {
				t.Fatalf("不明确的回执不能误记为尚未发送或自动重试：%v", err)
			}
		})
	}
}

func TestPartnerPublishCancellationKeepsSentClassification(t *testing.T) {
	for _, cancelBefore := range []bool{false, true} {
		t.Run(map[bool]string{false: "during_request", true: "before_request"}[cancelBefore], func(t *testing.T) {
			cfg := &api.Config{Retry: 5}
			cfg.Cookie.Filepath = filepath.Join(t.TempDir(), "cookie.json")
			client, err := api.NewClient(cfg, log.Default)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cancelBefore {
				cancel()
			}
			calls := 0
			client.GetClient().Transport = partnerNoteTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				cancel()
				return nil, context.Canceled
			})
			_, err = New(client).PartnerNotePublish(ctx, partnerNoteTestRequest())
			var failure *PartnerPublishError
			if !errors.As(err, &failure) || !errors.Is(err, context.Canceled) || failure.NotSent != cancelBefore || calls != map[bool]int{false: 1, true: 0}[cancelBefore] {
				t.Fatalf("取消须保留是否已调用发布请求：%v", err)
			}
		})
	}
}

func TestPartnerNoteOnlyMarksExplicitBusinessRejection(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		rejected   bool
	}{
		{"explicit", `{"code":400,"message":"private-message","msg":"private-msg","errDetail":"private-detail","needPolling":false}`, 200, true},
		{"structured_detail", `{"code":400,"errDetail":{"field":"private-detail"}}`, 200, true},
		{"root_id", `{"code":400,"id":901}`, 200, false},
		{"event_id", `{"code":400,"event":{"id":901}}`, 200, false},
		{"invalid_id", `{"code":400,"id":-1}`, 200, false},
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
			client.GetClient().Transport = partnerNoteTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: request}, nil
			})
			reply, err := New(client).PartnerNotePublish(t.Context(), partnerNoteTestRequest())
			var failure *PartnerPublishError
			if !errors.As(err, &failure) || failure.Rejected != tc.rejected || failure.NotSent || failure.HTTPStatus != tc.status || calls != 1 {
				t.Fatalf("明确拒绝与未知结果区分错误：%v", err)
			}
			if tc.rejected && (failure.Phase != "业务拒绝" || failure.ServiceCode != 400) {
				t.Fatal("明确拒绝必须保留业务阶段和服务码")
			}
			if tc.name == "explicit" && (reply.Message != "private-message" || reply.Msg != "private-msg" || string(reply.ErrDetail) != `"private-detail"`) {
				t.Fatal("应向调用方保留诊断字段，由调用方受控提取")
			}
			if strings.Contains(err.Error(), "private-") {
				t.Fatal("结构化错误不得回显响应消息或详情")
			}
		})
	}
}

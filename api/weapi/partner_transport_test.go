package weapi

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/pkg/cookie"
	"github.com/3899/ncmm/pkg/log"
)

type partnerTestTransport func(*http.Request) (*http.Response, error)

func (f partnerTestTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestPartnerNativeRequestsUseExpectedProtocol(t *testing.T) {
	client, err := api.NewClient(&api.Config{Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, log.Default)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	rootURL, _ := url.Parse("https://music.163.com/")
	client.SetCookies(rootURL, []*http.Cookie{{Name: "MUSIC_U", Value: "unit-test-session", Path: "/"}, {Name: "__csrf", Value: "unit-test-csrf", Path: "/"}})
	called := make(map[string]int)
	client.GetClient().Transport = partnerTestTransport(func(req *http.Request) (*http.Response, error) {
		called[req.URL.Path]++
		if req.Host != "interface.music.163.com" || req.Referer() != "https://mp.music.163.com/" {
			t.Error("unexpected host or referer")
		}
		if ck, err := req.Cookie("MUSIC_U"); err != nil || ck.Value != "unit-test-session" {
			t.Error("account cookie missing")
		}
		payload := `{"code":200,"data":{}}`
		if req.URL.Path == "/api/music/partner/extra/wait/evaluate/work/list" {
			if req.Method != http.MethodGet {
				t.Error("extra list must use GET")
			}
			payload = `{"code":200,"data":[]}`
		} else {
			if req.Method != http.MethodPost {
				t.Error("WEAPI writes must use POST")
			}
			if req.URL.Query().Get("csrf_token") != "unit-test-csrf" {
				t.Error("CSRF query missing")
			}
			if err := req.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if req.Form.Get("params") == "" || len(req.Form.Get("encSecKey")) != 256 {
				t.Error("WEAPI encryption missing")
			}
			if strings.Contains(req.Form.Get("params"), "unit-test-csrf") {
				t.Error("plaintext CSRF in encrypted form")
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(payload)), Request: req}, nil
	})
	partner := New(client)
	if _, err := partner.PartnerDailyTask(context.Background(), &PartnerTaskReq{}); err != nil {
		t.Fatal(err)
	}
	if _, err := partner.PartnerExtraTask(context.Background(), &PartnerExtraTaskReq{}); err != nil {
		t.Fatal(err)
	}
	if _, err := partner.PartnerExtraReport(context.Background(), &PartnerExtraReportReq{WorkId: "1", ResourceId: "2", InteractType: "PLAY_END"}); err != nil {
		t.Fatal(err)
	}
	if _, err := partner.PartnerEvaluate(context.Background(), &PartnerEvaluateReq{TaskId: "1", WorkId: "2", Score: "3", Tags: ThreeAOnePartnerTags, CustomTags: "[]"}); err != nil {
		t.Fatal(err)
	}
	if len(called) != 4 {
		t.Fatalf("called paths=%d", len(called))
	}
	for path, count := range called {
		if count != 1 {
			t.Fatalf("%s called %d times", path, count)
		}
	}
}

package eapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/pkg/crypto"
	"github.com/3899/ncmm/pkg/log"
)

func TestSMSPCRequestContractAndSameIdentity(t *testing.T) {
	cfg := &api.Config{Retry: 3}
	cfg.Cookie.Filepath = filepath.Join(t.TempDir(), "cookie.json")
	client, err := api.NewClient(cfg, log.Default)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	identity := NewSMSLoginIdentity()
	seen := 0
	client.GetClient().Transport = partnerNoteTransport(func(req *http.Request) (*http.Response, error) {
		seen++
		if req.URL.Host != "interfacepc.music.163.com" {
			t.Fatal("wrong SMS host")
		}
		if req.Header.Get("x-os") != "pc" || req.Header.Get("x-osver") != smsPCOSVersion || req.Header.Get("x-appver") != smsPCVersion || req.Header.Get("x-aeapi") != "" || req.Header.Get("X-MAM-CustomMark") != "" {
			t.Fatal("SMS PC request mixed mobile AEAPI headers")
		}
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		plain, err := crypto.EApiDecrypt(req.Form.Get("params"), "hex")
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(string(plain), "-36cd479b6b5-")
		if len(parts) != 3 {
			t.Fatal("invalid EAPI envelope")
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(parts[1]), &payload); err != nil {
			t.Fatal(err)
		}
		var header smsPCHeader
		if err := json.Unmarshal([]byte(payload["header"].(string)), &header); err != nil {
			t.Fatal(err)
		}
		if header.DeviceID != identity.DeviceID || header.ClientSign != identity.ClientSign || header.OS != "pc" || header.AppVer != smsPCVersion || header.RequestID <= 0 {
			t.Fatal("SMS identity changed")
		}
		if payload["e_r"] != true || !strings.Contains(req.UserAgent(), smsPCVersion) {
			t.Fatal("SMS client protocol mismatch")
		}
		if seen == 1 {
			if req.URL.Path != "/eapi/middle/captcha/sent/v1" || payload["scene"] != "0" || payload["cellphone"] != "13800000000" || payload["ctcode"] != "86" || payload["secrete"] != "music_middleuser_pclogin" {
				t.Fatal("send contract mismatch")
			}
		} else {
			if req.URL.Path != "/eapi/w/login/cellphone" || payload["phone"] != "13800000000" || payload["captcha"] != "123456" || payload["type"] != "1" || payload["remember"] != "true" || payload["countrycode"] != "86" {
				t.Fatal("login contract mismatch")
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":200,"data":true}`))}, nil
	})
	if _, err := New(client).SMSCaptchaSend(context.Background(), "13800000000", 86, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := New(client).SMSCellphoneLogin(context.Background(), "13800000000", 86, "123456", identity); err != nil {
		t.Fatal(err)
	}
	if seen != 2 {
		t.Fatalf("requests=%d", seen)
	}
}

func TestSMSWriteDoesNotRetryOrLeakTransportError(t *testing.T) {
	cfg := &api.Config{Retry: 3}
	cfg.Cookie.Filepath = filepath.Join(t.TempDir(), "cookie.json")
	client, err := api.NewClient(cfg, log.Default)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	seen := 0
	client.GetClient().Transport = partnerNoteTransport(func(req *http.Request) (*http.Response, error) {
		seen++
		return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("13800000000 123456 secret"))}, nil
	})
	_, err = New(client).SMSCaptchaSend(context.Background(), "13800000000", 86, NewSMSLoginIdentity())
	if err == nil || seen != 1 || strings.Contains(err.Error(), "13800000000") || strings.Contains(err.Error(), "secret") {
		t.Fatal("SMS transport failure retried or leaked")
	}
}

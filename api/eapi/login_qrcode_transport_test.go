package eapi

import (
	"context"
	"encoding/json"
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

type qrLoginTransport func(*http.Request) (*http.Response, error)

func (transport qrLoginTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return transport(req)
}

func TestQRCodePCFailureDoesNotRetryOrExposeCredentials(t *testing.T) {
	directory := t.TempDir()
	logPath := filepath.Join(directory, "login.log")
	logConfig := &log.Config{Level: "debug"}
	logConfig.Rotate.Filename = logPath
	logger := log.New(logConfig)
	previous := log.Default
	log.Default = logger
	log.Info("QR transport privacy regression")
	defer func() {
		log.Default = previous
		_ = logger.Close()
	}()
	cfg := &api.Config{Retry: 5, Debug: true}
	cfg.Cookie.Filepath = filepath.Join(directory, "anonymous.json")
	client, err := api.NewClient(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	seen := 0
	client.GetClient().Transport = qrLoginTransport(func(req *http.Request) (*http.Response, error) {
		seen++
		return &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": {"application/json"}, "Set-Cookie": {"MUSIC_U=synthetic-response-private-marker; Path=/"}}, Body: io.NopCloser(strings.NewReader(`{"code":503,"message":"synthetic-response-private-marker"}`)), Request: req}, nil
	})
	_, err = New(client).QrcodeCheck(context.Background(), &QrcodeCheckReq{Type: 3, Key: "synthetic-key-private-marker"})
	if err == nil || seen != 1 {
		t.Fatalf("QR refusal must fail without automatic retry: requests=%d error=%v", seen, err)
	}
	content, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, secret := range []string{"synthetic-key-private-marker", "synthetic-response-private-marker"} {
		if strings.Contains(string(content), secret) || strings.Contains(err.Error(), secret) {
			t.Error("QR request or response exposed private contents")
		}
	}
}

func TestQRCodePCKeyAndCheckTransport(t *testing.T) {
	cfg := &api.Config{Retry: 5, Debug: true}
	cfg.Cookie.Filepath = filepath.Join(t.TempDir(), "anonymous.json")
	client, err := api.NewClient(cfg, log.Default)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	seen := 0
	client.GetClient().Transport = qrLoginTransport(func(req *http.Request) (*http.Response, error) {
		seen++
		if req.URL.Host != "interfacepc.music.163.com" {
			t.Errorf("QR endpoint host = %s", req.URL.Host)
		}
		if req.Header.Get("x-os") != "pc" || !strings.Contains(req.UserAgent(), "NeteaseMusicDesktop/") {
			t.Error("QR key/check must use a consistent PC identity")
		}
		// PC EAPI 不使用移动端 AEAPI 响应压缩协议，两端请求必须保持一致。
		for _, name := range []string{"x-aeapi", "X-Client-Enc-State", "X-MAM-CustomMark"} {
			if req.Header.Get(name) != "" {
				t.Errorf("PC QR 请求不应携带移动端专属头 %s", name)
			}
		}
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		plain, err := crypto.EApiDecrypt(req.PostForm.Get("params"), "hex")
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(string(plain), "-36cd479b6b5-")
		if len(parts) != 3 {
			t.Fatal("malformed EAPI payload")
		}
		var payload struct {
			Type   int64  `json:"type"`
			Key    string `json:"key"`
			Header struct {
				OS     string `json:"os"`
				Appver string `json:"appver"`
			} `json:"header"`
		}
		if err := json.Unmarshal([]byte(parts[1]), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Type != 3 || payload.Header.OS != "pc" || payload.Header.Appver == "" || !strings.Contains(req.UserAgent(), "NeteaseMusicDesktop/"+payload.Header.Appver) {
			t.Error("QR EAPI type/header do not match PC login")
		}
		body := `{"code":200,"unikey":"synthetic-key"}`
		wantPath := "/eapi/login/qrcode/unikey"
		if seen == 2 {
			wantPath = "/eapi/login/qrcode/client/login"
			body = `{"code":801}`
			if payload.Key != "synthetic-key" {
				t.Error("QR key was not preserved between key/check")
			}
		}
		if req.URL.Path != wantPath || parts[0] != strings.Replace(wantPath, "/eapi/", "/api/", 1) {
			t.Error("QR request path/encrypted path mismatch")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	request := New(client)
	key, err := request.QrcodeCreateKey(context.Background(), &QrcodeCreateKeyReq{Type: 3})
	if err != nil || key == nil || key.UniKey != "synthetic-key" {
		t.Fatalf("create key failed: %v", err)
	}
	status, err := request.QrcodeCheck(context.Background(), &QrcodeCheckReq{Key: key.UniKey, Type: 3})
	if err != nil || status == nil || status.Code != 801 || seen != 2 {
		t.Fatalf("check failed: calls=%d error=%v", seen, err)
	}
}

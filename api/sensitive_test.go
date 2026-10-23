package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/pkg/cookie"
	"github.com/3899/ncmm/pkg/log"
	"gopkg.in/natefinch/lumberjack.v2"
)

type sensitiveTransport func(*http.Request) (*http.Response, error)

type sensitiveLogger struct{ bytes.Buffer }

func (l *sensitiveLogger) Errorf(f string, v ...interface{}) { fmt.Fprintf(&l.Buffer, f, v...) }
func (l *sensitiveLogger) Warnf(f string, v ...interface{})  { fmt.Fprintf(&l.Buffer, f, v...) }
func (l *sensitiveLogger) Debugf(f string, v ...interface{}) { fmt.Fprintf(&l.Buffer, f, v...) }

func (f sensitiveTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSensitiveRequestDoesNotExposeCredentials(t *testing.T) {
	const secret = "private-csrf-and-response-value"
	logPath := filepath.Join(t.TempDir(), "debug.log")
	l := log.New(&log.Config{Level: "debug", Rotate: lumberjack.Logger{Filename: logPath}})
	previous := log.Default
	log.Default = l
	t.Cleanup(func() { log.Default = previous; _ = l.Close() })
	c, err := NewClient(&Config{Debug: true, Retry: 1, Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, l)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	var restyLog sensitiveLogger
	c.cli.SetLogger(&restyLog)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/error" {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"code":405,"message":"` + secret + `","secret":"` + secret + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"secret":"` + secret + `"}`))
	}))
	defer server.Close()
	for _, path := range []string{"/success", "/error"} {
		var resp map[string]any
		_, err := c.Request(context.Background(), server.URL+path+"?token="+secret, map[string]string{"csrf_token": secret}, &resp, &Options{CryptoMode: CryptoModeWEAPI, Sensitive: true})
		if path == "/success" && err != nil {
			t.Fatal(err)
		}
		if path == "/error" && (err == nil || strings.Contains(err.Error(), secret)) {
			t.Fatalf("unsafe HTTP error: %v", err)
		}
		if path == "/error" && (!strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "405")) {
			t.Fatalf("HTTP and service codes must both be preserved: %v", err)
		}
	}
	c.cli.SetTransport(sensitiveTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New(secret) }))
	var resp map[string]any
	_, err = c.Request(context.Background(), server.URL, map[string]string{"token": secret}, &resp, &Options{CryptoMode: CryptoModeWEAPI, Sensitive: true})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe network error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.Request(ctx, server.URL, map[string]string{}, &resp, &Options{CryptoMode: CryptoModeWEAPI, Sensitive: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	_ = l.Close()
	b, _ := os.ReadFile(logPath)
	if strings.Contains(string(b), secret) || strings.Contains(restyLog.String(), secret) {
		t.Fatal("sensitive request leaked to debug log")
	}
}

func TestSensitiveHTTPErrorDiagnostic(t *testing.T) {
	const secret = "private-response-and-url-value"
	for _, test := range []struct {
		name string
		body string
		code int64
	}{
		{name: "numeric top-level code", body: `{"code":405,"message":"` + secret + `","data":{"token":"` + secret + `"}}`, code: 405},
		{name: "negative code", body: `{"code":-1,"message":"` + secret + `"}`, code: -1},
		{name: "missing code", body: `{"message":"` + secret + `"}`},
		{name: "nested code only", body: `{"data":{"code":405},"message":"` + secret + `"}`},
		{name: "string code", body: `{"code":"405","message":"` + secret + `"}`},
		{name: "fractional code", body: `{"code":405.5,"message":"` + secret + `"}`},
		{name: "overflowing code", body: `{"code":9223372036854775808,"message":"` + secret + `"}`},
		{name: "null code", body: `{"code":null,"message":"` + secret + `"}`},
		{name: "nested object code", body: `{"code":{"value":405},"message":"` + secret + `"}`},
		{name: "array response", body: `[{"code":405,"message":"` + secret + `"}]`},
		{name: "malformed after code", body: `{"code":405,"message":"` + secret},
		{name: "trailing content", body: `{"code":405} ` + secret},
		{name: "HTML response", body: `<html>` + secret + `</html>`},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, err := NewClient(&Config{Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close(context.Background())
			c.cli.SetTransport(sensitiveTransport(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(test.body)), Request: request}, nil
			}))
			var reply map[string]any
			response, err := c.Request(context.Background(), "https://offline.invalid/error?token="+secret, map[string]string{"token": secret}, &reply, &Options{CryptoMode: CryptoModeAPI, Sensitive: true})
			var statusErr *HTTPStatusError
			if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusForbidden {
				t.Fatalf("HTTP error type or status lost: %v", err)
			}
			if statusErr.ServiceCode != test.code {
				t.Fatalf("service code = %d; want %d", statusErr.ServiceCode, test.code)
			}
			want := "音乐接口 HTTP 状态码：403"
			if test.code != 0 {
				want += fmt.Sprintf("，服务码：%d", test.code)
			}
			if err.Error() != want {
				t.Fatalf("diagnostic = %q; want %q", err.Error(), want)
			}
			if response != nil || len(reply) != 0 || strings.Contains(fmt.Sprintf("%#v", err), secret) {
				t.Fatal("HTTP rejection retained response payload or credentials")
			}
		})
	}
}

func TestCSRFRootFallbackOnlyForMusicSubdomains(t *testing.T) {
	c, err := NewClient(&Config{Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	u, _ := url.Parse("https://music.163.com")
	c.SetCookies(u, []*http.Cookie{{Name: "__csrf", Value: "root-token"}})
	for _, raw := range []string{"https://music.163.com", "https://interface.music.163.com/weapi/test", "https://interface3.music.163.com"} {
		if token, ok := c.GetCSRF(raw); !ok || token != "root-token" {
			t.Fatalf("missing fallback for %s", raw)
		}
	}
	for _, raw := range []string{"https://evil.163.com", "https://music.163.com.evil.test", "https://notmusic.163.com"} {
		if _, ok := c.GetCSRF(raw); ok {
			t.Fatalf("token forwarded outside music domain: %s", raw)
		}
	}
	sub, _ := url.Parse("https://interface.music.163.com")
	c.SetCookies(sub, []*http.Cookie{{Name: "__csrf", Value: "sub-token"}})
	if token, _ := c.GetCSRF(sub.String()); token != "sub-token" {
		t.Fatal("subdomain token must take priority")
	}
}

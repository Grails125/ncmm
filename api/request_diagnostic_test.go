package api

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/pkg/cookie"
	"github.com/3899/ncmm/pkg/log"
)

func TestSensitiveRequestClassifiesFailuresWithoutPrivateDetails(t *testing.T) {
	const private = "private-url-key-cookie-value"
	logPath := filepath.Join(t.TempDir(), "diagnostic.log")
	logCfg := &log.Config{Level: "debug"}
	logCfg.Rotate.Filename = logPath
	logger := log.New(logCfg)
	previous := log.Default
	log.Default = logger
	defer func() { log.Default = previous; _ = logger.Close() }()
	for _, tc := range []struct {
		name, encoding, want string
		err                  error
	}{
		{name: "timeout", err: &net.DNSError{Err: private, Name: private, IsTimeout: true}, want: "请求超时"},
		{name: "dns", err: &net.DNSError{Err: private, Name: private}, want: "域名解析失败"},
		{name: "tls", err: tls.RecordHeaderError{Msg: private}, want: "TLS 连接失败"},
		{name: "connection", err: io.ErrUnexpectedEOF, want: "连接中断"},
		{name: "invalid compression", encoding: "deflate", want: "响应读取或解压失败"},
		{name: "private encoding", encoding: private, want: "响应读取或解压失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewClient(&Config{Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "anonymous.json")}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close(context.Background())
			client.GetClient().Transport = sensitiveTransport(func(req *http.Request) (*http.Response, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Encoding": {tc.encoding}}, Body: io.NopCloser(strings.NewReader(private)), Request: req}, nil
			})
			var reply map[string]any
			_, err = client.Request(context.Background(), "https://offline.invalid/qr?key="+private, map[string]string{"key": private}, &reply, &Options{CryptoMode: CryptoModeAPI, Sensitive: true, NoRetry: true})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误分类缺失: %v; want %s", err, tc.want)
			}
			if strings.Contains(fmt.Sprintf("%#v", err), private) {
				t.Fatal("诊断错误保留了私密内容")
			}
		})
	}
	content, err := os.ReadFile(logPath)
	if err != nil || strings.Contains(string(content), private) {
		t.Fatalf("诊断日志不可用或暴露了私密内容: %v", err)
	}
}

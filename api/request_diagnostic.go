package api

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/3899/ncmm/pkg/log"
	"github.com/go-resty/resty/v2"
)

// 敏感请求只保留受控分类和数值状态，不记录底层 URL、Cookie 或响应正文。
func sensitiveRequestError(err error, response *resty.Response) error {
	kind, status, encoding := "请求失败", 0, "无"
	if response != nil && response.RawResponse != nil {
		status = response.StatusCode()
		switch value := strings.ToLower(strings.TrimSpace(response.Header().Get("Content-Encoding"))); value {
		case "gzip", "deflate", "br", "identity":
			encoding = value
		case "":
		default:
			encoding = "其他"
		}
	}
	var networkErr net.Error
	var dnsErr *net.DNSError
	var connectionErr *net.OpError
	var certificateErr *tls.CertificateVerificationError
	var recordErr tls.RecordHeaderError
	var authorityErr x509.UnknownAuthorityError
	var invalidCertificateErr x509.CertificateInvalidError
	var hostnameErr x509.HostnameError
	switch {
	case errors.As(err, &networkErr) && networkErr.Timeout():
		kind = "请求超时"
	case errors.As(err, &dnsErr):
		kind = "域名解析失败"
	case errors.As(err, &certificateErr), errors.As(err, &recordErr), errors.As(err, &authorityErr), errors.As(err, &invalidCertificateErr), errors.As(err, &hostnameErr):
		kind = "TLS 连接失败"
	case status > 0:
		kind = "响应读取或解压失败"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.As(err, &connectionErr):
		kind = "连接中断"
	}
	log.Warn("音乐接口请求失败：分类=%s，HTTP=%d，响应编码=%s，错误类型=%T", kind, status, encoding, err)
	return fmt.Errorf("音乐接口%s，请稍后重试", kind)
}

// Resty 重试会直接记录原始错误；敏感请求改由 sensitiveRequestError 输出安全诊断。
type sensitiveRequestLogger struct{}

func (sensitiveRequestLogger) Errorf(string, ...interface{}) {}
func (sensitiveRequestLogger) Warnf(string, ...interface{})  {}
func (sensitiveRequestLogger) Debugf(string, ...interface{}) {}

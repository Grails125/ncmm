package ncmm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/3899/ncmm/api"
)

// 媒体请求独立于账号会话，不向图片或音频 CDN 发送登录 Cookie。
func neteaseMediaClient(client *api.Client) *http.Client {
	return &http.Client{Transport: client.GetClient().Transport, Timeout: 45 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !neteaseMediaURL(req.URL) {
			return fmt.Errorf("媒体重定向不在允许的网易云域名内")
		}
		return nil
	}}
}

func neteaseMediaURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return strings.HasSuffix(host, ".music.126.net") || strings.HasSuffix(host, ".music.163.com") || host == "music.163.com"
}

func neteaseMediaGet(ctx context.Context, media *http.Client, raw string) (*http.Response, error) {
	u, err := url.Parse(raw)
	if err == nil && u.Scheme == "http" {
		u.Scheme = "https"
	}
	if err != nil || !neteaseMediaURL(u) {
		return nil, fmt.Errorf("媒体地址无效或不在允许的网易云域名内")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("无法创建媒体请求")
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		response, err := media.Do(req)
		if err == nil && response.StatusCode == http.StatusOK {
			return response, nil
		}
		status := 0
		if response != nil {
			status = response.StatusCode
			response.Body.Close()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		kind, retry := neteaseMediaFailureKind(err, status)
		if retry && attempt < 2 {
			// 只重试无账号凭据的只读 CDN GET；发布和互动上报不走此路径。
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(250 * time.Millisecond):
				continue
			}
		}
		failure := fmt.Errorf("获取媒体失败（CDN=%s，分类=%s，HTTP=%d，尝试=%d/2）", u.Hostname(), kind, status, attempt)
		if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests {
			return nil, errors.Join(failure, &api.HTTPStatusError{StatusCode: status})
		}
		return nil, failure
	}
	return nil, fmt.Errorf("获取媒体失败")
}

// 原始网络错误通常带有签名 URL；只输出受控分类和已验证的 CDN 域名。
func neteaseMediaFailureKind(err error, status int) (string, bool) {
	var dns *net.DNSError
	var network net.Error
	var connection *net.OpError
	var certificate *tls.CertificateVerificationError
	var authority x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	var record tls.RecordHeaderError
	switch {
	case errors.As(err, &dns):
		return "域名解析失败", dns.Timeout() || dns.Temporary()
	case errors.As(err, &certificate), errors.As(err, &authority), errors.As(err, &invalid), errors.As(err, &hostname), errors.As(err, &record):
		return "TLS 证书或握手失败", false
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &network) && network.Timeout():
		return "网络请求超时", true
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.As(err, &connection):
		return "连接中断", true
	case status >= 500 && status <= 599:
		return "CDN 暂时不可用", true
	case status > 0:
		return "HTTP 拒绝或资源不可用", false
	default:
		return "请求或重定向失败", false
	}
}

// 音频和封面共用无 Cookie、限制域名的 CDN 客户端；分块传输不要求 Content-Length。
func readNeteaseAudio(ctx context.Context, media *http.Client, raw string) error {
	response, err := neteaseMediaGet(ctx, media, raw)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	kind := strings.ToLower(response.Header.Get("Content-Type"))
	if !strings.HasPrefix(kind, "audio/") && !strings.HasPrefix(kind, "application/octet-stream") {
		return fmt.Errorf("音频地址返回了非音频数据")
	}
	count, err := io.Copy(io.Discard, io.LimitReader(response.Body, 32*1024*1024+1))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		kind, _ := neteaseMediaFailureKind(err, 0)
		return fmt.Errorf("读取音频失败（分类=%s）", kind)
	}
	if count == 0 || count > 32*1024*1024 || response.ContentLength > 0 && count != response.ContentLength {
		return fmt.Errorf("音频不完整或超过大小限制")
	}
	return nil
}

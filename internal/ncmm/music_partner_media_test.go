package ncmm

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/3899/ncmm/api/weapi"
)

func TestPartnerMediaRetriesOnlySafeTransientReads(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failure   error
		status    int
		wantCalls int
		wantError bool
	}{
		{"连接中断", io.EOF, 0, 2, false},
		{"CDN临时不可用", nil, 503, 2, false},
		{"拒绝访问", nil, 403, 1, true},
		{"证书异常", &tls.CertificateVerificationError{Err: errors.New("private-signature")}, 0, 1, true},
		{"DNS明确不存在", &net.DNSError{Err: "private-signature", IsNotFound: true}, 0, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := &partnerRecommendClient{media: &http.Client{Transport: musicianVipClaimTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
					t.Fatal("媒体不能发送登录凭据")
				}
				if calls == 1 {
					if tc.failure != nil {
						return nil, tc.failure
					}
					return &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"audio/mpeg"}}, Body: io.NopCloser(strings.NewReader("audio")), Request: r}, nil
			})}}
			err := c.PrepareAudio(t.Context(), weapi.PartnerWork{PlayUrl: "https://m801.music.126.net/private-signature/audio?token=private-signature"})
			if (err != nil) != tc.wantError || calls != tc.wantCalls {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if err != nil && (strings.Contains(err.Error(), "private-signature") || !strings.Contains(err.Error(), "m801.music.126.net")) {
				t.Fatal("诊断应包含 CDN 和受控原因，不能泄漏带签名地址")
			}
		})
	}
}

func TestPartnerMediaCancellationDoesNotRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	c := &partnerRecommendClient{media: &http.Client{Transport: musicianVipClaimTransport(func(r *http.Request) (*http.Response, error) { calls++; cancel(); return nil, io.EOF })}}
	_, err := c.mediaGet(ctx, "https://m801.music.126.net/a")
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("取消后不得继续请求：calls=%d err=%v", calls, err)
	}
}

func TestPartnerMediaFailureHasSafeReason(t *testing.T) {
	c := &partnerRecommendClient{media: &http.Client{Transport: musicianVipClaimTransport(func(r *http.Request) (*http.Response, error) {
		return nil, &net.DNSError{Err: "token=private", Name: "private", IsNotFound: true}
	})}}
	_, err := c.mediaGet(t.Context(), "https://m801.music.126.net/private?token=private")
	if err == nil || !strings.Contains(err.Error(), "域名解析失败") || strings.Contains(err.Error(), "private") {
		t.Fatalf("未提供安全原因：%v", err)
	}
}

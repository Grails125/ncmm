package api

import (
	"net/http"
	"net/url"

	"github.com/go-resty/resty/v2"
)

// 独立 HTTP 客户端只共享线程安全的 Transport，避免临时网页会话污染持久化 Cookie。
func (c *Client) requestClient(opts *Options) *resty.Client {
	if !opts.FansGroupWebSession && !opts.NoRetry {
		return c.cli
	}
	httpClient := *c.GetClient()
	if opts.FansGroupWebSession {
		httpClient.Jar = fansGroupCookieView{base: httpClient.Jar}
		transport := httpClient.Transport
		if transport == nil {
			transport = http.DefaultTransport
		}
		httpClient.Transport = fansGroupTransport{base: transport}
	}
	client := resty.NewWithClient(&httpClient)
	client.OnAfterResponse(contentEncoding)
	if !opts.NoRetry {
		client.SetRetryCount(c.cfg.Retry)
	}
	return client
}

type fansGroupCookieView struct{ base http.CookieJar }

func (j fansGroupCookieView) Cookies(u *url.URL) []*http.Cookie {
	if j.base == nil {
		return nil
	}
	var cookies []*http.Cookie
	for _, cookie := range j.base.Cookies(u) {
		if cookie.Name != "os" {
			cookies = append(cookies, cookie)
		}
	}
	return cookies
}
func (fansGroupCookieView) SetCookies(*url.URL, []*http.Cookie) {}

type fansGroupTransport struct{ base http.RoundTripper }

func (t fansGroupTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	request := r.Clone(r.Context())
	request.Header.Del("Cookie")
	for _, cookie := range r.Cookies() {
		if cookie.Name != "os" {
			request.AddCookie(cookie)
		}
	}
	return t.base.RoundTrip(request)
}

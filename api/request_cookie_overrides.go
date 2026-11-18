package api

import (
	"net/http"
	"net/url"
)

// 隔离的会话身份只影响一次请求；保留登录凭据，丢弃响应 Set-Cookie 对共享账号的修改。
type requestCookieOverrideView struct {
	base      http.CookieJar
	overrides []*http.Cookie
}

func (j requestCookieOverrideView) Cookies(u *url.URL) []*http.Cookie {
	if j.base == nil {
		return nil
	}
	return withoutOverriddenCookies(j.base.Cookies(u), j.overrides)
}

func (requestCookieOverrideView) SetCookies(*url.URL, []*http.Cookie) {}

type requestCookieOverrideTransport struct {
	base      http.RoundTripper
	overrides []*http.Cookie
}

func (t requestCookieOverrideTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	request := r.Clone(r.Context())
	cookies := withoutOverriddenCookies(r.Cookies(), t.overrides)
	request.Header.Del("Cookie")
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	for _, cookie := range t.overrides {
		if cookie != nil {
			request.AddCookie(cookie)
		}
	}
	return t.base.RoundTrip(request)
}

func withoutOverriddenCookies(cookies, overrides []*http.Cookie) []*http.Cookie {
	result := make([]*http.Cookie, 0, len(cookies))
	for _, cookie := range cookies {
		replaced := false
		for _, override := range overrides {
			if override != nil && override.Name == cookie.Name {
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, cookie)
		}
	}
	return result
}

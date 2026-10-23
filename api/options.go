// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

package api

import (
	"fmt"
	"net/http"
)

// HTTPStatusError 仅保留明确拒绝的数值状态码，不保存响应正文或凭据。
type HTTPStatusError struct {
	StatusCode  int
	ServiceCode int64
}

func (e *HTTPStatusError) Error() string {
	if e.ServiceCode != 0 {
		return fmt.Sprintf("音乐接口 HTTP 状态码：%d，服务码：%d", e.StatusCode, e.ServiceCode)
	}
	return fmt.Sprintf("音乐接口 HTTP 状态码：%d", e.StatusCode)
}

type CryptoMode string

const (
	CryptoModeAPI   CryptoMode = "api"
	CryptoModeEAPI  CryptoMode = "eapi"
	CryptoModeXEAPI CryptoMode = "xeapi"
	CryptoModeWEAPI CryptoMode = "weapi"
	CryptoModeLinux CryptoMode = "linux"
)

type Options struct {
	Method     string
	CryptoMode CryptoMode
	Headers    map[string]string
	Cookies    []*http.Cookie
	// Sensitive 禁止输出包含登录凭据的请求、响应与底层 URL 错误。
	Sensitive bool
	// FansGroupWebSession 仅用于乐迷团播放；请求独立过滤 os，不修改或回写账号 Cookie。
	FansGroupWebSession bool
	// NoRetry 禁止自动重发不可安全重放的写请求。
	NoRetry bool
}

func (o *Options) SetCookies(c ...*http.Cookie) {
	o.Cookies = append(o.Cookies, c...)
}

func (o *Options) SetHeader(key, value string) *Options {
	o.Headers[key] = value
	return o
}

func (o *Options) SetHeaders(h map[string]string) *Options {
	for k, v := range h {
		o.Headers[k] = v
	}
	return o
}

func NewOptions() *Options {
	return &Options{
		Method:     http.MethodPost,
		CryptoMode: CryptoModeWEAPI,
		Headers:    make(map[string]string),
		Cookies:    []*http.Cookie{},
	}
}

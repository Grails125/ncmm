// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

package api

import (
	"fmt"
	"net/http"
)

// HTTPStatusError 保留明确拒绝的状态码，避免重试非幂等写入或泄露响应体。
type HTTPStatusError struct{ StatusCode int }

func (e *HTTPStatusError) Error() string {
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

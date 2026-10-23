// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

package eapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/pkg/log"
	"github.com/go-resty/resty/v2"
)

// 维护中的 api-enhanced 图片上传实现也使用此网易 NOS 节点。
// https://github.com/NeteaseCloudMusicApiEnhanced/api-enhanced/blob/main/plugins/upload.js
const fallbackUploadNode = "https://nosup-hz1.127.net"

var nosBucketPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)
var nosHostPattern = regexp.MustCompile(`^nosup-[a-z0-9-]+\.127\.net$`)

type uploadLbsResp struct {
	Upload []string `json:"upload"`
}

// NOS 只需要专用上传令牌，不能继承音乐 API 的账号 Cookie 或跟随重定向。
func (a *Api) nosClient() *resty.Client {
	client := *a.client.GetClient()
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if transport, ok := client.Transport.(*http.Transport); ok {
		transport = transport.Clone()
		if transport.TLSClientConfig != nil {
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
			transport.TLSClientConfig.InsecureSkipVerify = false
		}
		client.Transport = transport
	}
	return resty.NewWithClient(&client).SetRetryCount(0).SetDebug(false)
}

// normalizeUploadNode 只允许网易 NOS 上传域，旧 LBS 返回的 HTTP 节点升级为 HTTPS。
func normalizeUploadNode(raw string) (string, error) {
	node, err := url.Parse(raw)
	if err != nil || node.User != nil || (node.Scheme != "http" && node.Scheme != "https") || !nosHostPattern.MatchString(node.Host) || (node.Path != "" && node.Path != "/") || node.RawQuery != "" || node.Fragment != "" {
		return "", fmt.Errorf("不可信的 NOS 上传节点")
	}
	return "https://" + node.Host, nil
}

// getUploadNode 使用令牌分配的 bucket 查询节点；仅网络故障和 5xx 可尝试固定备用节点。
func (a *Api) getUploadNode(ctx context.Context, bucket string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", imageUploadFailure(ctx, "查询节点", "请求失败", 0, 0, err)
	}
	if !nosBucketPattern.MatchString(bucket) {
		return "", imageUploadFailure(ctx, "查询节点", "节点无效", 0, 0, nil)
	}
	client := a.nosClient()
	defer client.GetClient().CloseIdleConnections()
	resp, err := client.R().SetContext(ctx).SetQueryParams(map[string]string{"version": "1.0", "bucketname": bucket}).Get("https://wanproxy.127.net/lbs")
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		log.Warn("[NOS] LBS 节点查询失败，尝试网易备用节点 bucket=%s err=%v", bucket, err)
		return fallbackUploadNode, nil
	}
	if resp.StatusCode() >= 500 && resp.StatusCode() <= 599 {
		log.Warn("[NOS] LBS 暂时不可用，尝试网易备用节点 bucket=%s status=%d", bucket, resp.StatusCode())
		return fallbackUploadNode, nil
	}
	if resp.StatusCode() != http.StatusOK {
		return "", imageUploadFailure(ctx, "查询节点", "HTTP拒绝", resp.StatusCode(), 0, nil)
	}

	var lbs uploadLbsResp
	if err := json.Unmarshal(resp.Body(), &lbs); err != nil {
		return "", imageUploadFailure(ctx, "查询节点", "响应格式无效", resp.StatusCode(), 0, err)
	}
	for _, raw := range lbs.Upload {
		if node, err := normalizeUploadNode(raw); err == nil {
			return node, nil
		}
	}
	return "", imageUploadFailure(ctx, "查询节点", "节点无效", resp.StatusCode(), 0, nil)
}

func eventImageUploadURL(node, bucket, objectKey string) (string, error) {
	node, err := normalizeUploadNode(node)
	if err != nil {
		return "", err
	}
	if !nosBucketPattern.MatchString(bucket) {
		return "", fmt.Errorf("无效的 NOS bucket")
	}
	key, err := url.PathUnescape(objectKey)
	if err != nil || key == "" || strings.HasPrefix(key, "/") || strings.ContainsAny(key, "\\\r\n\x00") {
		return "", fmt.Errorf("无效的 NOS 图片路径")
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("无效的 NOS 图片路径")
		}
	}
	u, _ := url.Parse(node)
	u.Path = "/" + bucket + "/" + key
	u.RawQuery = url.Values{"version": {"1.0"}, "offset": {"0"}, "complete": {"true"}}.Encode()
	return u.String(), nil
}

// rawUpload 使用 NOS 的 POST 完整上传协议；禁止重试，以免响应丢失时重复提交。
func (a *Api) rawUpload(ctx context.Context, rawURL string, headers map[string]string, data []byte) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" {
		return imageUploadFailure(ctx, "上传文件", "路径无效", 0, 0, nil)
	}
	if _, err := normalizeUploadNode("https://" + u.Host); err != nil {
		return imageUploadFailure(ctx, "上传文件", "节点无效", 0, 0, err)
	}
	if err := ctx.Err(); err != nil {
		return imageUploadFailure(ctx, "上传文件", "请求失败", 0, 0, err)
	}
	client := a.nosClient()
	defer client.GetClient().CloseIdleConnections()
	req := client.R().SetContext(ctx).SetBody(data).SetHeader("User-Agent", a.client.UserAgent(api.CryptoModeEAPI))
	// 上传仅允许这两个头，防止调用方意外带入账号会话。
	for _, name := range []string{"X-Nos-Token", "Content-Type"} {
		req.SetHeader(name, headers[name])
	}
	resp, err := req.Post(rawURL)
	if err != nil {
		if ctx.Err() != nil {
			return imageUploadFailure(ctx, "上传文件", "请求失败", 0, 0, ctx.Err())
		}
		return imageUploadFailure(ctx, "上传文件", "请求失败", 0, 0, err)
	}
	if resp.StatusCode() != http.StatusOK {
		return imageUploadFailure(ctx, "上传文件", "HTTP拒绝", resp.StatusCode(), 0, nil)
	}
	var result struct {
		ErrCode json.RawMessage `json:"errCode"`
		Offset  *int64          `json:"offset"`
	}
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return imageUploadFailure(ctx, "上传文件", "响应格式无效", resp.StatusCode(), 0, err)
	}
	if len(result.ErrCode) > 0 && string(result.ErrCode) != "null" && string(result.ErrCode) != "0" && string(result.ErrCode) != `""` {
		code, _ := strconv.ParseInt(strings.Trim(string(result.ErrCode), `"`), 10, 64)
		return imageUploadFailure(ctx, "上传文件", "服务拒绝", resp.StatusCode(), code, nil)
	}
	if result.Offset == nil || *result.Offset != int64(len(data)) {
		return imageUploadFailure(ctx, "上传文件", "上传不完整", resp.StatusCode(), 0, nil)
	}
	return nil
}

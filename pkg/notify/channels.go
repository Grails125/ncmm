// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// ---------- Webhook ----------

type webhookChannel struct {
	cfg    WebhookConfig
	client *http.Client
}

func (c *webhookChannel) Name() string { return "webhook" }

func (c *webhookChannel) Send(ctx context.Context, msg Message) error {
	method := strings.ToUpper(strings.TrimSpace(c.cfg.Method))
	if method == "" {
		method = http.MethodPost
	}
	body, contentType, err := buildWebhookBody(c.cfg.BodyTemplate, msg)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range c.cfg.Headers {
		if strings.TrimSpace(k) != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http status %d", resp.StatusCode)
	}
	return nil
}

func buildWebhookBody(template string, msg Message) ([]byte, string, error) {
	host, _ := os.Hostname()
	now := time.Now().Format(time.RFC3339)
	if strings.TrimSpace(template) == "" {
		payload := map[string]string{
			"title":   msg.Title,
			"content": msg.Content,
			"level":   msg.Level,
			"time":    now,
			"host":    host,
		}
		b, err := json.Marshal(payload)
		return b, "application/json", err
	}
	replacer := strings.NewReplacer(
		"{{title}}", msg.Title,
		"{{content}}", msg.Content,
		"{{level}}", msg.Level,
		"{{time}}", now,
		"{{host}}", host,
	)
	out := replacer.Replace(template)
	return []byte(out), "application/json", nil
}

// ---------- Bark ----------

type barkChannel struct {
	cfg    BarkConfig
	client *http.Client
}

func (c *barkChannel) Name() string { return "bark" }

func (c *barkChannel) Send(ctx context.Context, msg Message) error {
	server := strings.TrimRight(strings.TrimSpace(c.cfg.Server), "/")
	if server == "" {
		server = "https://api.day.app"
	}
	u := fmt.Sprintf("%s/%s/%s/%s",
		server,
		url.PathEscape(c.cfg.Key),
		url.PathEscape(msg.Title),
		url.PathEscape(msg.Content),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		Code int `json:"code"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http status %d", resp.StatusCode)
	}
	if result.Code != 0 && result.Code != 200 {
		return fmt.Errorf("bark code %d", result.Code)
	}
	return nil
}

// ---------- Server酱 ----------

type serverChanChannel struct {
	cfg    ServerChanConfig
	client *http.Client
}

func (c *serverChanChannel) Name() string { return "serverchan" }

func (c *serverChanChannel) Send(ctx context.Context, msg Message) error {
	key := strings.TrimSpace(c.cfg.SCKEY)
	var endpoint string
	if strings.HasPrefix(strings.ToUpper(key), "SCT") {
		endpoint = fmt.Sprintf("https://sctapi.ftqq.com/%s.send", url.PathEscape(key))
	} else {
		endpoint = fmt.Sprintf("https://sc.ftqq.com/%s.send", url.PathEscape(key))
	}
	form := url.Values{}
	form.Set("text", msg.Title)
	form.Set("desp", strings.ReplaceAll(msg.Content, "\n", "\n\n"))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// ---------- Telegram ----------

type telegramChannel struct {
	cfg     TelegramConfig
	client  *http.Client
	timeout time.Duration
}

func (c *telegramChannel) Name() string { return "telegram" }

func (c *telegramChannel) Send(ctx context.Context, msg Message) error {
	apiHost := strings.TrimSpace(c.cfg.APIHost)
	var base string
	switch {
	case apiHost == "":
		base = "https://api.telegram.org"
	case strings.HasPrefix(apiHost, "http://") || strings.HasPrefix(apiHost, "https://"):
		base = strings.TrimRight(apiHost, "/")
	default:
		base = "https://" + strings.TrimRight(apiHost, "/")
	}
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", base, c.cfg.BotToken)

	client := c.client
	if proxy := strings.TrimSpace(c.cfg.Proxy); proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil {
			return fmt.Errorf("invalid telegram proxy: %w", err)
		}
		client = &http.Client{
			Timeout: c.timeout,
			Transport: &http.Transport{
				Proxy: http.ProxyURL(u),
			},
		}
	}

	form := url.Values{}
	form.Set("chat_id", c.cfg.UserID)
	form.Set("text", msg.Title+"\n\n"+msg.Content)
	form.Set("disable_web_page_preview", "true")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if !result.OK {
		if result.Description != "" {
			return fmt.Errorf("telegram: %s", result.Description)
		}
		return fmt.Errorf("telegram send failed")
	}
	return nil
}

// ---------- DingTalk ----------

type dingTalkChannel struct {
	cfg    DingTalkConfig
	client *http.Client
}

func (c *dingTalkChannel) Name() string { return "dingtalk" }

func (c *dingTalkChannel) Send(ctx context.Context, msg Message) error {
	endpoint := fmt.Sprintf("https://oapi.dingtalk.com/robot/send?access_token=%s", url.QueryEscape(c.cfg.AccessToken))
	if secret := strings.TrimSpace(c.cfg.Secret); secret != "" {
		ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
		stringToSign := ts + "\n" + secret
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(stringToSign))
		sign := url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		endpoint = fmt.Sprintf("%s&timestamp=%s&sign=%s", endpoint, ts, sign)
	}
	payload := map[string]any{
		"msgtype": "text",
		"text": map[string]string{
			"content": msg.Title + "\n\n" + msg.Content,
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json;charset=utf-8")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result.ErrCode != 0 {
		return fmt.Errorf("dingtalk: %s (%d)", result.ErrMsg, result.ErrCode)
	}
	return nil
}

// ---------- CoolPush (QQ) ----------

type coolPushChannel struct {
	cfg    CoolPushConfig
	client *http.Client
}

func (c *coolPushChannel) Name() string { return "coolpush" }

func (c *coolPushChannel) Send(ctx context.Context, msg Message) error {
	endpoint := fmt.Sprintf("https://qmsg.zendee.cn/%s/%s",
		url.PathEscape(c.cfg.Mode), url.PathEscape(c.cfg.SKey))
	form := url.Values{}
	form.Set("msg", msg.Title+"\n\n"+msg.Content)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		Code int    `json:"code"`
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result.Code != 0 {
		if result.Reason != "" {
			return fmt.Errorf("coolpush: %s", result.Reason)
		}
		return fmt.Errorf("coolpush code %d", result.Code)
	}
	return nil
}

// ---------- PushPlus ----------

type pushPlusChannel struct {
	cfg    PushPlusConfig
	client *http.Client
}

func (c *pushPlusChannel) Name() string { return "pushplus" }

func (c *pushPlusChannel) Send(ctx context.Context, msg Message) error {
	payload := map[string]string{
		"token":   c.cfg.Token,
		"title":   msg.Title,
		"content": msg.Content,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://www.pushplus.plus/send", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result.Code != 200 {
		if result.Msg != "" {
			return fmt.Errorf("pushplus: %s", result.Msg)
		}
		return fmt.Errorf("pushplus code %d", result.Code)
	}
	return nil
}

// ---------- WeCom group robot ----------

type weComKeyChannel struct {
	cfg    WeComKeyConfig
	client *http.Client
}

func (c *weComKeyChannel) Name() string { return "wecom_key" }

func (c *weComKeyChannel) Send(ctx context.Context, msg Message) error {
	endpoint := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=%s", url.QueryEscape(c.cfg.Key))
	payload := map[string]any{
		"msgtype": "text",
		"text": map[string]string{
			"content": msg.Title + "\n" + strings.ReplaceAll(msg.Content, "\n", "\n\n"),
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result.ErrCode != 0 {
		return fmt.Errorf("wecom_key: %s (%d)", result.ErrMsg, result.ErrCode)
	}
	return nil
}

// ---------- WeCom application ----------

type weComAppChannel struct {
	cfg    WeComAppConfig
	client *http.Client
}

func (c *weComAppChannel) Name() string { return "wecom_app" }

func (c *weComAppChannel) Send(ctx context.Context, msg Message) error {
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return err
	}
	endpoint := "https://qyapi.weixin.qq.com/cgi-bin/message/send?access_token=" + url.QueryEscape(token)
	toUser := strings.TrimSpace(c.cfg.ToUser)
	if toUser == "" {
		toUser = "@all"
	}
	agentID, err := strconv.Atoi(strings.TrimSpace(c.cfg.AgentID))
	if err != nil {
		return fmt.Errorf("invalid agent_id: %w", err)
	}

	var payload map[string]any
	if mediaID := strings.TrimSpace(c.cfg.MediaID); mediaID != "" {
		payload = map[string]any{
			"touser":  toUser,
			"msgtype": "mpnews",
			"agentid": agentID,
			"mpnews": map[string]any{
				"articles": []map[string]string{
					{
						"title":              msg.Title,
						"thumb_media_id":     mediaID,
						"author":             "ncmm",
						"content_source_url": "",
						"content":            strings.ReplaceAll(msg.Content, "\n", "<br/>"),
						"digest":             msg.Content,
					},
				},
			},
		}
	} else {
		payload = map[string]any{
			"touser":  toUser,
			"msgtype": "text",
			"agentid": agentID,
			"text": map[string]string{
				"content": msg.Title + "\n\n" + msg.Content,
			},
			"safe": 0,
		}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result.ErrCode != 0 {
		return fmt.Errorf("wecom_app: %s (%d)", result.ErrMsg, result.ErrCode)
	}
	return nil
}

func (c *weComAppChannel) getAccessToken(ctx context.Context) (string, error) {
	u := "https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=" + url.QueryEscape(c.cfg.CorpID) +
		"&corpsecret=" + url.QueryEscape(c.cfg.CorpSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var result struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.ErrCode != 0 || result.AccessToken == "" {
		return "", fmt.Errorf("gettoken: %s (%d)", result.ErrMsg, result.ErrCode)
	}
	return result.AccessToken, nil
}

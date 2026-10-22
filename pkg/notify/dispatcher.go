// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

package notify

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Message is a notification payload.
type Message struct {
	Title   string
	Content string
	Level   string // error / warn / info
}

// Channel sends a message to one provider.
type Channel interface {
	Name() string
	Send(ctx context.Context, msg Message) error
}

// Dispatcher fans out to all enabled channels.
type Dispatcher struct {
	channels []Channel
	client   *http.Client
	timeout  time.Duration
}

// NewDispatcher builds a dispatcher from channel config.
func NewDispatcher(cfg *ChannelsConfig, timeout time.Duration) *Dispatcher {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	d := &Dispatcher{
		client:  client,
		timeout: timeout,
	}
	if cfg == nil {
		return d
	}

	if cfg.Webhook.Enabled && strings.TrimSpace(cfg.Webhook.URL) != "" {
		d.channels = append(d.channels, &webhookChannel{cfg: cfg.Webhook, client: client})
	}
	if cfg.Bark.Enabled && strings.TrimSpace(cfg.Bark.Key) != "" {
		d.channels = append(d.channels, &barkChannel{cfg: cfg.Bark, client: client})
	}
	if cfg.ServerChan.Enabled && strings.TrimSpace(cfg.ServerChan.SCKEY) != "" {
		d.channels = append(d.channels, &serverChanChannel{cfg: cfg.ServerChan, client: client})
	}
	if cfg.Telegram.Enabled && strings.TrimSpace(cfg.Telegram.BotToken) != "" && strings.TrimSpace(cfg.Telegram.UserID) != "" {
		d.channels = append(d.channels, &telegramChannel{cfg: cfg.Telegram, client: client, timeout: timeout})
	}
	if cfg.DingTalk.Enabled && strings.TrimSpace(cfg.DingTalk.AccessToken) != "" {
		d.channels = append(d.channels, &dingTalkChannel{cfg: cfg.DingTalk, client: client})
	}
	if cfg.CoolPush.Enabled && strings.TrimSpace(cfg.CoolPush.SKey) != "" && strings.TrimSpace(cfg.CoolPush.Mode) != "" {
		d.channels = append(d.channels, &coolPushChannel{cfg: cfg.CoolPush, client: client})
	}
	if cfg.PushPlus.Enabled && strings.TrimSpace(cfg.PushPlus.Token) != "" {
		d.channels = append(d.channels, &pushPlusChannel{cfg: cfg.PushPlus, client: client})
	}
	if cfg.WeComKey.Enabled && strings.TrimSpace(cfg.WeComKey.Key) != "" {
		d.channels = append(d.channels, &weComKeyChannel{cfg: cfg.WeComKey, client: client})
	}
	if cfg.WeComApp.Enabled && strings.TrimSpace(cfg.WeComApp.CorpID) != "" &&
		strings.TrimSpace(cfg.WeComApp.CorpSecret) != "" && strings.TrimSpace(cfg.WeComApp.AgentID) != "" {
		d.channels = append(d.channels, &weComAppChannel{cfg: cfg.WeComApp, client: client})
	}
	return d
}

// Len returns the number of enabled channels.
func (d *Dispatcher) Len() int {
	if d == nil {
		return 0
	}
	return len(d.channels)
}

// SendAll sends to every channel; collects errors without aborting others.
func (d *Dispatcher) SendAll(ctx context.Context, msg Message) error {
	if d == nil || len(d.channels) == 0 {
		return fmt.Errorf("no notify channels enabled")
	}
	if strings.TrimSpace(msg.Level) == "" {
		msg.Level = "error"
	}
	var errs []string
	for _, ch := range d.channels {
		cctx, cancel := context.WithTimeout(ctx, d.timeout)
		err := ch.Send(cctx, msg)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", ch.Name(), err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

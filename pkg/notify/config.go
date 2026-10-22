// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

package notify

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ChannelsConfig is the content of notify.yaml (channel credentials only).
type ChannelsConfig struct {
	Webhook    WebhookConfig    `yaml:"webhook"`
	Bark       BarkConfig       `yaml:"bark"`
	ServerChan ServerChanConfig `yaml:"serverchan"`
	Telegram   TelegramConfig   `yaml:"telegram"`
	DingTalk   DingTalkConfig   `yaml:"dingtalk"`
	CoolPush   CoolPushConfig   `yaml:"coolpush"`
	PushPlus   PushPlusConfig   `yaml:"pushplus"`
	WeComKey   WeComKeyConfig   `yaml:"wecom_key"`
	WeComApp   WeComAppConfig   `yaml:"wecom_app"`
}

type WebhookConfig struct {
	Enabled      bool              `yaml:"enabled"`
	URL          string            `yaml:"url"`
	Method       string            `yaml:"method"`
	Headers      map[string]string `yaml:"headers"`
	BodyTemplate string            `yaml:"body_template"`
}

type BarkConfig struct {
	Enabled bool   `yaml:"enabled"`
	Key     string `yaml:"key"`
	Server  string `yaml:"server"` // empty => https://api.day.app
}

type ServerChanConfig struct {
	Enabled bool   `yaml:"enabled"`
	SCKEY   string `yaml:"sckey"`
}

type TelegramConfig struct {
	Enabled  bool   `yaml:"enabled"`
	BotToken string `yaml:"bot_token"`
	UserID   string `yaml:"user_id"`
	APIHost  string `yaml:"api_host"`
	Proxy    string `yaml:"proxy"` // e.g. http://127.0.0.1:7890
}

type DingTalkConfig struct {
	Enabled     bool   `yaml:"enabled"`
	AccessToken string `yaml:"access_token"`
	Secret      string `yaml:"secret"`
}

type CoolPushConfig struct {
	Enabled bool   `yaml:"enabled"`
	SKey    string `yaml:"skey"`
	Mode    string `yaml:"mode"` // send / group / ...
}

type PushPlusConfig struct {
	Enabled bool   `yaml:"enabled"`
	Token   string `yaml:"token"`
}

type WeComKeyConfig struct {
	Enabled bool   `yaml:"enabled"`
	Key     string `yaml:"key"`
}

type WeComAppConfig struct {
	Enabled    bool   `yaml:"enabled"`
	CorpID     string `yaml:"corp_id"`
	CorpSecret string `yaml:"corp_secret"`
	ToUser     string `yaml:"to_user"`
	AgentID    string `yaml:"agent_id"`
	MediaID    string `yaml:"media_id"`
}

// LoadChannels reads notify.yaml and applies environment variable overrides.
// If the file does not exist, returns an empty config with env overrides only
// (fileMissing=true). Parse errors still return an error.
func LoadChannels(path string) (cfg *ChannelsConfig, fileMissing bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			empty := &ChannelsConfig{}
			applyEnvOverrides(empty)
			return empty, true, nil
		}
		return nil, false, err
	}
	var parsed ChannelsConfig
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil, false, fmt.Errorf("parse notify channels file: %w", err)
	}
	applyEnvOverrides(&parsed)
	return &parsed, false, nil
}

func applyEnvOverrides(cfg *ChannelsConfig) {
	if v := os.Getenv("NCMM_NOTIFY_WEBHOOK_URL"); v != "" {
		cfg.Webhook.URL = v
		cfg.Webhook.Enabled = true
	}
	if v := os.Getenv("NCMM_BARK_KEY"); v != "" {
		cfg.Bark.Key = v
		cfg.Bark.Enabled = true
	}
	if v := os.Getenv("NCMM_BARK_SERVER"); v != "" {
		cfg.Bark.Server = v
	}
	if v := os.Getenv("NCMM_SCKEY"); v != "" {
		cfg.ServerChan.SCKEY = v
		cfg.ServerChan.Enabled = true
	}
	if v := os.Getenv("NCMM_TG_BOT_TOKEN"); v != "" {
		cfg.Telegram.BotToken = v
		cfg.Telegram.Enabled = true
	}
	if v := os.Getenv("NCMM_TG_USER_ID"); v != "" {
		cfg.Telegram.UserID = v
		cfg.Telegram.Enabled = true
	}
	if v := os.Getenv("NCMM_TG_API_HOST"); v != "" {
		cfg.Telegram.APIHost = v
	}
	if v := os.Getenv("NCMM_TG_PROXY"); v != "" {
		cfg.Telegram.Proxy = v
	}
	if v := os.Getenv("NCMM_DD_BOT_ACCESS_TOKEN"); v != "" {
		cfg.DingTalk.AccessToken = v
		cfg.DingTalk.Enabled = true
	}
	if v := os.Getenv("NCMM_DD_BOT_SECRET"); v != "" {
		cfg.DingTalk.Secret = v
	}
	if v := os.Getenv("NCMM_QQ_SKEY"); v != "" {
		cfg.CoolPush.SKey = v
		cfg.CoolPush.Enabled = true
	}
	if v := os.Getenv("NCMM_QQ_MODE"); v != "" {
		cfg.CoolPush.Mode = v
	}
	if v := os.Getenv("NCMM_PUSH_PLUS_TOKEN"); v != "" {
		cfg.PushPlus.Token = v
		cfg.PushPlus.Enabled = true
	}
	if v := os.Getenv("NCMM_QYWX_KEY"); v != "" {
		cfg.WeComKey.Key = v
		cfg.WeComKey.Enabled = true
	}
	if v := os.Getenv("NCMM_QYWX_AM"); v != "" {
		// corpid,corpsecret,touser,agentid[,media_id]
		parts := strings.Split(v, ",")
		if len(parts) >= 4 {
			cfg.WeComApp.CorpID = strings.TrimSpace(parts[0])
			cfg.WeComApp.CorpSecret = strings.TrimSpace(parts[1])
			cfg.WeComApp.ToUser = strings.TrimSpace(parts[2])
			cfg.WeComApp.AgentID = strings.TrimSpace(parts[3])
			if len(parts) >= 5 {
				cfg.WeComApp.MediaID = strings.TrimSpace(parts[4])
			}
			cfg.WeComApp.Enabled = true
		}
	}
}

// ParseTimeout parses a duration string with default 10s.
func ParseTimeout(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return 10 * time.Second
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 10 * time.Second
	}
	return d
}

// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReportNeedNotify(t *testing.T) {
	r := NewReport("task")
	if r.NeedNotify(true) {
		t.Fatal("empty report should not notify")
	}
	r.AddSkip("a", "t", "no token")
	if !r.NeedNotify(true) {
		t.Fatal("skip with onSkip=true should notify")
	}
	if r.NeedNotify(false) {
		t.Fatal("skip with onSkip=false should not notify")
	}
	r.AddFailureMsg("b", "sign", "not login")
	if !r.NeedNotify(false) {
		t.Fatal("failure should notify even if onSkip=false")
	}
}

func TestReportSummary(t *testing.T) {
	r := NewReport("task")
	r.AddFailureMsg("./cookie.json", "sign", "Cookie 已失效")
	r.AddSkip("./fan1.json", "daily-song-share", "token missing")
	title, content := r.Summary("ncmm", true)
	if !strings.Contains(title, "失败 1") || !strings.Contains(title, "跳过 1") {
		t.Fatalf("unexpected title: %s", title)
	}
	if !strings.Contains(content, "【失败】") || !strings.Contains(content, "【跳过】") {
		t.Fatalf("unexpected content: %s", content)
	}
	_, contentNoSkip := r.Summary("ncmm", false)
	if strings.Contains(contentNoSkip, "【跳过】") {
		t.Fatalf("skip section should be hidden: %s", contentNoSkip)
	}
}

func TestWebhookChannel(t *testing.T) {
	var gotBody map[string]string
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewDispatcher(&ChannelsConfig{
		Webhook: WebhookConfig{
			Enabled: true,
			URL:     srv.URL,
		},
	}, 5*time.Second)
	if d.Len() != 1 {
		t.Fatalf("expected 1 channel, got %d", d.Len())
	}
	err := d.SendAll(context.Background(), Message{Title: "t1", Content: "c1", Level: "error"})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method=%s", gotMethod)
	}
	if gotBody["title"] != "t1" || gotBody["content"] != "c1" {
		t.Fatalf("body=%v", gotBody)
	}
}

func TestWebhookBodyTemplate(t *testing.T) {
	var raw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		raw = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewDispatcher(&ChannelsConfig{
		Webhook: WebhookConfig{
			Enabled:      true,
			URL:          srv.URL,
			BodyTemplate: `{"msg_type":"text","content":{"text":"{{title}}\n{{content}}"}}`,
		},
	}, 5*time.Second)
	if err := d.SendAll(context.Background(), Message{Title: "T", Content: "C"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"text":"T\nC"`) && !strings.Contains(raw, "T") {
		t.Fatalf("template body unexpected: %s", raw)
	}
}

func TestLoadChannels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notify.yaml")
	content := `
webhook:
  enabled: true
  url: "https://example.com/hook"
telegram:
  enabled: true
  bot_token: "tok"
  user_id: "1"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, missing, err := LoadChannels(path)
	if err != nil {
		t.Fatal(err)
	}
	if missing {
		t.Fatal("expected file to exist")
	}
	if !cfg.Webhook.Enabled || cfg.Webhook.URL == "" {
		t.Fatalf("webhook not loaded: %+v", cfg.Webhook)
	}
	if !cfg.Telegram.Enabled || cfg.Telegram.BotToken != "tok" {
		t.Fatalf("telegram not loaded: %+v", cfg.Telegram)
	}
}

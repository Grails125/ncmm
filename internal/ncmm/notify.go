// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

package ncmm

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/3899/ncmm/pkg/log"
	"github.com/3899/ncmm/pkg/notify"
)

// initNotify prepares report collector and optional channel dispatcher from config.
func (c *Root) initNotify() {
	c.Report = notify.NewReport("ncmm")
	c.Notifier = nil

	if c.Cfg == nil || c.Cfg.Notify == nil || !c.Cfg.Notify.Enabled {
		return
	}

	path := resolveNotifyFilePath(c.CfgPath, c.Opts.Home, c.Cfg.Notify.File)
	cfg, fileMissing, err := notify.LoadChannels(path)
	if err != nil {
		log.Warn("[notify] load channels file failed (%s): %v", path, err)
		return
	}
	if fileMissing {
		log.Warn("[notify] channels file not found: %s (env overrides still applied if set)", path)
	}

	timeout := notify.ParseTimeout(c.Cfg.Notify.Timeout)
	d := notify.NewDispatcher(cfg, timeout)
	if d.Len() == 0 {
		log.Warn("[notify] enabled but no valid channel configured (file=%s)", path)
		return
	}
	c.Notifier = d
	if fileMissing {
		log.Info("[notify] loaded %d channel(s) from environment", d.Len())
	} else {
		log.Info("[notify] loaded %d channel(s) from %s", d.Len(), path)
	}
}

func resolveNotifyFilePath(cfgPath, home, file string) string {
	file = strings.TrimSpace(file)
	if file == "" {
		file = "notify.yaml"
	}
	if filepath.IsAbs(file) {
		return filepath.Clean(file)
	}
	if cfgPath != "" && cfgPath != "default" {
		return filepath.Clean(filepath.Join(filepath.Dir(cfgPath), file))
	}
	if home == "" {
		home = "."
	}
	return filepath.Clean(filepath.Join(home, file))
}

// ReportFailure records a failure for end-of-run notification.
func (c *Root) ReportFailure(account, task string, err error) {
	if c == nil || c.Report == nil || err == nil {
		return
	}
	c.Report.AddFailure(account, task, err)
}

// ReportFailureMsg records a failure message.
func (c *Root) ReportFailureMsg(account, task, message string) {
	if c == nil || c.Report == nil {
		return
	}
	c.Report.AddFailureMsg(account, task, message)
}

// ReportSkip records a skipped task for end-of-run notification.
func (c *Root) ReportSkip(account, task, reason string) {
	if c == nil || c.Report == nil {
		return
	}
	c.Report.AddSkip(account, task, reason)
}

// FlushNotify sends aggregated failure/skip summary if needed.
// Notification errors are logged only and never returned.
func (c *Root) FlushNotify(command string) {
	if c == nil || c.Report == nil {
		return
	}
	if command != "" {
		c.Report.SetCommand(command)
	}
	if c.Cfg == nil || c.Cfg.Notify == nil || !c.Cfg.Notify.Enabled {
		return
	}
	onSkip := c.Cfg.Notify.OnSkipEnabled()
	if !c.Report.NeedNotify(onSkip) {
		return
	}
	if c.Notifier == nil || c.Notifier.Len() == 0 {
		log.Warn("[notify] has events to push but no channel available")
		return
	}

	title, content := c.Report.Summary(c.Cfg.Notify.TitlePrefix, onSkip)
	msg := notify.Message{
		Title:   title,
		Content: content,
		Level:   "error",
	}
	if err := c.Notifier.SendAll(context.Background(), msg); err != nil {
		log.Warn("[notify] send failed: %v", err)
		return
	}
	log.Info("[notify] summary sent: %s", title)
}


// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

package notify

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Kind classifies a report item.
type Kind string

const (
	KindFailure Kind = "failure"
	KindSkip    Kind = "skip"
)

// Item is one failure or skip entry collected during a run.
type Item struct {
	Kind    Kind
	Account string
	Task    string
	Message string
}

// Report collects failures/skips for end-of-run summary notification.
type Report struct {
	mu        sync.Mutex
	Command   string
	StartedAt time.Time
	items     []Item
}

// NewReport creates an empty report.
func NewReport(command string) *Report {
	return &Report{
		Command:   command,
		StartedAt: time.Now(),
	}
}

// SetCommand sets the command name shown in the summary.
func (r *Report) SetCommand(command string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Command = command
}

// AddFailure records a task/account failure.
func (r *Report) AddFailure(account, task string, err error) {
	if r == nil || err == nil {
		return
	}
	r.add(KindFailure, account, task, err.Error())
}

// AddFailureMsg records a failure with a plain message.
func (r *Report) AddFailureMsg(account, task, message string) {
	if r == nil || strings.TrimSpace(message) == "" {
		return
	}
	r.add(KindFailure, account, task, message)
}

// AddSkip records a skipped task.
func (r *Report) AddSkip(account, task, reason string) {
	if r == nil {
		return
	}
	if strings.TrimSpace(reason) == "" {
		reason = "skipped"
	}
	r.add(KindSkip, account, task, reason)
}

func (r *Report) add(kind Kind, account, task, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, Item{
		Kind:    kind,
		Account: strings.TrimSpace(account),
		Task:    strings.TrimSpace(task),
		Message: strings.TrimSpace(message),
	})
}

// Items returns a copy of collected items.
func (r *Report) Items() []Item {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Item, len(r.items))
	copy(out, r.items)
	return out
}

// Counts returns failure and skip counts.
func (r *Report) Counts() (failures, skips int) {
	if r == nil {
		return 0, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, it := range r.items {
		switch it.Kind {
		case KindFailure:
			failures++
		case KindSkip:
			skips++
		}
	}
	return failures, skips
}

// NeedNotify reports whether a push should be sent.
// Success never notifies. Failures always notify when present.
// Skips notify only when onSkip is true.
func (r *Report) NeedNotify(onSkip bool) bool {
	failures, skips := r.Counts()
	if failures > 0 {
		return true
	}
	return onSkip && skips > 0
}

// Summary builds title and content for notification channels.
func (r *Report) Summary(titlePrefix string, onSkip bool) (title, content string) {
	if r == nil {
		return "", ""
	}
	prefix := strings.TrimSpace(titlePrefix)
	if prefix == "" {
		prefix = "ncmm"
	}

	items := r.Items()
	var failures, skips []Item
	for _, it := range items {
		switch it.Kind {
		case KindFailure:
			failures = append(failures, it)
		case KindSkip:
			if onSkip {
				skips = append(skips, it)
			}
		}
	}

	parts := make([]string, 0, 2)
	if len(failures) > 0 {
		parts = append(parts, fmt.Sprintf("失败 %d", len(failures)))
	}
	if len(skips) > 0 {
		parts = append(parts, fmt.Sprintf("跳过 %d", len(skips)))
	}
	title = fmt.Sprintf("[%s] 运行异常 (%s)", prefix, strings.Join(parts, " · "))

	host, _ := os.Hostname()
	r.mu.Lock()
	cmd := r.Command
	started := r.StartedAt
	r.mu.Unlock()
	if cmd == "" {
		cmd = "ncmm"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "主机: %s\n", host)
	fmt.Fprintf(&b, "命令: %s\n", cmd)
	fmt.Fprintf(&b, "开始: %s\n", started.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "结束: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "失败: %d  跳过: %d\n", len(failures), len(skips))

	idx := 1
	if len(failures) > 0 {
		b.WriteString("\n【失败】\n")
		for _, it := range failures {
			writeItem(&b, idx, it)
			idx++
		}
	}
	if len(skips) > 0 {
		b.WriteString("\n【跳过】\n")
		for _, it := range skips {
			writeItem(&b, idx, it)
			idx++
		}
	}
	return title, b.String()
}

func writeItem(b *strings.Builder, idx int, it Item) {
	account := it.Account
	if account == "" {
		account = "-"
	}
	task := it.Task
	if task == "" {
		task = "-"
	}
	fmt.Fprintf(b, "%d. [%s] %s\n   %s\n", idx, account, task, it.Message)
}

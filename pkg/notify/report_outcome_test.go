package notify

import (
	"strings"
	"testing"
)

func TestReportDeduplicatesRepeatedOutcome(t *testing.T) {
	r := NewReport("task")
	r.AddSkip("account", "播放", "未开启")
	r.AddSkip("account", "播放", "未开启")
	r.AddSkip("other", "播放", "未开启")
	if _, skips := r.Counts(); skips != 2 {
		t.Fatalf("重复轮次不应重复计数：got %d", skips)
	}
}

func TestReportUnconfirmedIsVisibleWithoutSkipNotification(t *testing.T) {
	r := NewReport("task")
	r.AddUnconfirmed("account", "乐签", "官方状态尚未确认")
	r.AddUnconfirmed("account", "乐签", "官方状态尚未确认")
	if !r.NeedNotify(false) {
		t.Fatal("未确认结果应通知，不能当作成功")
	}
	failures, skips, unconfirmed := r.OutcomeCounts()
	if failures != 0 || skips != 0 || unconfirmed != 1 {
		t.Fatalf("wrong counts: %d/%d/%d", failures, skips, unconfirmed)
	}
	title, content := r.Summary("ncmm", false)
	if !strings.Contains(title, "未确认 1") || !strings.Contains(content, "官方状态尚未确认") {
		t.Fatalf("missing unconfirmed outcome: %s %s", title, content)
	}
}

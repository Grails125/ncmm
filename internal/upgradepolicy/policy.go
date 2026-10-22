// Package upgradepolicy enforces only explicitly declared native release requirements.
package upgradepolicy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const Marker = "<!-- ncmm-force-upgrade -->"

var nativeVersion = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)-grails\.(\d+)$`)

// CompareNative 仅比较本仓库版本；未知或上游分发版本不能触发强制升级。
func CompareNative(a, b string) (int, bool) {
	x, y := nativeVersion.FindStringSubmatch(a), nativeVersion.FindStringSubmatch(b)
	if x == nil || y == nil {
		return 0, false
	}
	for i := 1; i <= 4; i++ {
		left, e1 := strconv.ParseUint(x[i], 10, 64)
		right, e2 := strconv.ParseUint(y[i], 10, 64)
		if e1 != nil || e2 != nil {
			return 0, false
		}
		if left < right {
			return -1, true
		}
		if left > right {
			return 1, true
		}
	}
	return 0, true
}

type Cache struct {
	LastCheckTime       time.Time `json:"last_check_time"`
	LastCheckFailed     bool      `json:"last_check_failed,omitempty"`
	RequiredVersion     string    `json:"required_version,omitempty"`
	RequiredPublishedAt time.Time `json:"required_published_at,omitempty"`
}

type Status struct {
	Required        bool      `json:"required"`
	CurrentVersion  string    `json:"currentVersion"`
	RequiredVersion string    `json:"requiredVersion,omitempty"`
	GraceUntil      time.Time `json:"graceUntil,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	UpdateURL       string    `json:"updateURL,omitempty"`
}

func (c *Cache) Observe(tag, body string, published time.Time) {
	if _, ok := CompareNative(tag, tag); !ok || published.IsZero() {
		return
	}
	if !strings.Contains(body, Marker) {
		if c.RequiredVersion == tag {
			c.RequiredVersion = ""
			c.RequiredPublishedAt = time.Time{}
		}
		return
	}
	if old, ok := CompareNative(tag, c.RequiredVersion); c.RequiredVersion == "" || !ok || old >= 0 {
		c.RequiredVersion = tag
		c.RequiredPublishedAt = published
	}
}

func Evaluate(c Cache, current string, now time.Time) Status {
	s := Status{CurrentVersion: current}
	if c.LastCheckFailed || c.LastCheckTime.IsZero() || now.Sub(c.LastCheckTime) > 24*time.Hour || c.LastCheckTime.After(now.Add(time.Minute)) || c.RequiredPublishedAt.IsZero() || c.RequiredPublishedAt.After(now) {
		return s
	}
	cmp, ok := CompareNative(current, c.RequiredVersion)
	if !ok || cmp >= 0 {
		return s
	}
	s.RequiredVersion = c.RequiredVersion
	s.GraceUntil = c.RequiredPublishedAt.AddDate(0, 1, 0)
	s.Required = !now.Before(s.GraceUntil)
	s.Reason = "发布方要求升级到受支持版本后继续执行自动任务"
	s.UpdateURL = "https://github.com/Grails125/ncmm/releases/tag/" + c.RequiredVersion
	return s
}

func Read(home, current string, now time.Time) Status {
	data, err := os.ReadFile(filepath.Join(home, "ncmm-update.json"))
	if err != nil {
		return Status{CurrentVersion: current}
	}
	var cache Cache
	if json.Unmarshal(data, &cache) != nil {
		return Status{CurrentVersion: current}
	}
	return Evaluate(cache, current, now)
}

func AllowsCommand(path string) bool {
	parts := strings.Fields(path)
	if len(parts) < 2 {
		return true
	}
	switch parts[1] {
	case "update", "version", "web", "login", "auth", "help":
		return true
	case "plugin":
		return len(parts) == 2 || parts[2] == "list" || parts[2] == "install"
	default:
		return false
	}
}

type RequiredError struct{ Status Status }

func (e *RequiredError) Error() string {
	return fmt.Sprintf("当前版本 %s 需要升级至 %s 后继续执行任务；请运行 ncmm update --apply 或重建 Docker 镜像", e.Status.CurrentVersion, e.Status.RequiredVersion)
}
func (e *RequiredError) ExitCode() int { return 3 }

func Check(home, current string, now time.Time) error {
	status := Read(home, current, now)
	if status.Required {
		return &RequiredError{Status: status}
	}
	return nil
}

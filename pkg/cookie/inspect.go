package cookie

import (
	"slices"
	"strings"
	"time"
)

// HasNonEmpty 检查仓库中是否存在未过期的非空字段，不受请求路径限制，不返回字段值。
func (c *Cookie) HasNonEmpty(names ...string) bool {
	c.jar.mu.Lock()
	defer c.jar.mu.Unlock()
	now := time.Now()
	for _, entries := range c.jar.entries {
		for _, entry := range entries {
			if entry.Persistent && !entry.Expires.After(now) {
				continue
			}
			if slices.Contains(names, entry.Name) && strings.TrimSpace(entry.Value) != "" {
				return true
			}
		}
	}
	return false
}

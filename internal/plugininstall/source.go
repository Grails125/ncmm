package plugininstall

import "time"

// Source 仅保存可公开的安装来源；凭据、签名下载地址和代理认证参数不得持久化。
type Source struct {
	Repository  string    `json:"repository"`
	Tag         string    `json:"tag"`
	Private     bool      `json:"private,omitempty"`
	Proxy       string    `json:"proxy,omitempty"`
	InstalledAt time.Time `json:"installedAt"`
}

// Update 是可重建的版本检查缓存；不会持久化访问令牌。
type Update struct {
	Name            string    `json:"name"`
	Repository      string    `json:"repository"`
	CurrentTag      string    `json:"currentTag"`
	LatestTag       string    `json:"latestTag,omitempty"`
	UpdateAvailable bool      `json:"updateAvailable"`
	CheckedAt       time.Time `json:"checkedAt"`
	Error           string    `json:"error,omitempty"`
}

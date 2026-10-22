package plugininstall

import "time"

// Source 仅保存可公开的安装来源；凭据、签名下载地址和代理认证参数不得持久化。
type Source struct {
	Repository  string    `json:"repository"`
	Tag         string    `json:"tag"`
	Private     bool      `json:"private,omitempty"`
	InstalledAt time.Time `json:"installedAt"`
}

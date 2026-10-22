package plugininstall

import (
	"fmt"
	"net/url"
	"strings"
)

// NormalizeProxy 只保存公开下载镜像地址，不允许把认证信息放进代理配置。
func NormalizeProxy(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "direct" {
		return "", nil
	}
	switch value {
	case "gh-proxy.com", "ghproxy.net", "githubproxy.cc":
		value = "https://" + value
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%w：代理必须为不含认证信息的 HTTPS 地址", ErrInvalidOptions)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

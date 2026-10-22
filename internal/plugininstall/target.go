// Package plugininstall 定义 CLI 与 WebUI 共用的插件安装输入契约。
package plugininstall

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var ErrInvalidOptions = errors.New("插件安装参数无效")

type Options struct {
	Target       string
	Tag          string
	Token        string
	DestDir      string
	Proxy        string
	ExpectedName string
}

type Target struct {
	Repository string
	URL        string
}

var repositoryPart = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func validRepository(parts []string) bool {
	return len(parts) == 2 && repositoryPart.MatchString(parts[0]) && repositoryPart.MatchString(parts[1]) && parts[0] != "." && parts[0] != ".." && parts[1] != "." && parts[1] != ".."
}

// ParseTarget 只接受仓库标识、GitHub 仓库页或 HTTP(S) 安装包直链，避免将网页保存成插件。
func (o Options) ParseTarget() (Target, error) {
	input := strings.TrimSpace(o.Target)
	fail := func(message string) (Target, error) {
		return Target{}, fmt.Errorf("%w：%s", ErrInvalidOptions, message)
	}
	if input == "" {
		return fail("请填写仓库或下载地址")
	}
	if !strings.Contains(input, "://") {
		parts := strings.Split(input, "/")
		if !validRepository(parts) {
			return fail("仓库格式应为 owner/repo")
		}
		return Target{Repository: input}, nil
	}
	u, err := url.Parse(input)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fail("请使用不含账号密码或片段的 HTTP(S) 地址")
	}
	if strings.EqualFold(u.Hostname(), "github.com") || strings.EqualFold(u.Hostname(), "www.github.com") {
		if u.Port() != "" {
			return fail("GitHub 仓库地址不支持自定义端口")
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) == 2 {
			parts[1] = strings.TrimSuffix(parts[1], ".git")
			if !validRepository(parts) {
				return fail("GitHub 仓库地址无效")
			}
			return Target{Repository: strings.Join(parts, "/")}, nil
		}
		isReleaseDownload := len(parts) >= 6 && parts[2] == "releases" && parts[3] == "download"
		isLatestDownload := len(parts) == 6 && parts[2] == "releases" && parts[3] == "latest" && parts[4] == "download"
		if !isReleaseDownload && !isLatestDownload {
			return fail("请填写 GitHub 仓库首页或 Release 安装包直链")
		}
	}
	if tag := strings.TrimSpace(o.Tag); tag != "" && !strings.EqualFold(tag, "latest") {
		return fail("下载直链已指定文件，请清空版本 Tag，或改用仓库地址")
	}
	return Target{URL: input}, nil
}

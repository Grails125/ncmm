package ncmm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/3899/ncmm/internal/plugininstall"
)

type pluginReleaseAsset struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	BrowserURL string `json:"browser_download_url"`
}
type pluginRelease struct {
	Tag    string               `json:"tag_name"`
	Assets []pluginReleaseAsset `json:"assets"`
}

// InstallPlugin 统一处理安装入口，只有成功验证的仓库发布才记录可升级来源。
func (p *Plugin) InstallPlugin(ctx context.Context, options PluginInstallOptions, logFn func(string, ...any)) (string, error) {
	target, err := options.ParseTarget()
	if err != nil {
		return "", err
	}
	proxy, err := plugininstall.NormalizeProxy(options.Proxy)
	if err != nil {
		return "", err
	}
	options.Proxy = proxy
	if logFn == nil {
		logFn = func(string, ...any) {}
	}
	if target.Repository != "" {
		return p.installGitHubRelease(ctx, target.Repository, options, logFn)
	}
	u, _ := url.Parse(target.URL)
	data, err := p.downloadPlugin(ctx, target.URL, options.Token, proxy, "", pluginArchiveLimit, logFn)
	if err != nil {
		return "", err
	}
	return p.installArchive(ctx, data, filepath.Base(u.Path), options.DestDir, options.ExpectedName, nil)
}

func (p *Plugin) InstallFromURL(ctx context.Context, targetURL, token, destDir string, logFn func(string, ...any)) (string, error) {
	return p.InstallPlugin(ctx, PluginInstallOptions{Target: targetURL, Token: token, DestDir: destDir}, logFn)
}
func (p *Plugin) InstallFromGitHub(ctx context.Context, repo, token, destDir string, logFn func(string, ...any)) (string, error) {
	return p.InstallPlugin(ctx, PluginInstallOptions{Target: repo, Token: token, DestDir: destDir}, logFn)
}

func (p *Plugin) installGitHubRelease(ctx context.Context, repo string, options PluginInstallOptions, logFn func(string, ...any)) (string, error) {
	tag := strings.TrimSpace(options.Tag)
	endpoint := "https://api.github.com/repos/" + repo + "/releases/latest"
	if tag != "" && !strings.EqualFold(tag, "latest") {
		endpoint = "https://api.github.com/repos/" + repo + "/releases/tags/" + url.PathEscape(tag)
	}
	data, err := p.downloadPlugin(ctx, endpoint, options.Token, "", "application/vnd.github+json", 4<<20, logFn)
	if err != nil {
		return "", err
	}
	var release pluginRelease
	if err = json.Unmarshal(data, &release); err != nil {
		return "", fmt.Errorf("插件发布信息格式无效")
	}
	if strings.TrimSpace(release.Tag) == "" {
		return "", fmt.Errorf("插件发布缺少实际版本标签")
	}
	var asset *pluginReleaseAsset
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x86_64"
	}
	for i := range release.Assets {
		name := strings.ToLower(release.Assets[i].Name)
		if strings.Contains(name, runtime.GOOS) && (strings.Contains(name, arch) || strings.Contains(name, runtime.GOARCH)) {
			asset = &release.Assets[i]
			break
		}
	}
	if asset == nil {
		return "", fmt.Errorf("插件发布没有匹配 %s/%s 的安装包", runtime.GOOS, runtime.GOARCH)
	}
	downloadURL := asset.URL
	if options.Token == "" && asset.BrowserURL != "" {
		downloadURL = asset.BrowserURL
	}
	parsed, err := url.Parse(downloadURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || (parsed.Host != "api.github.com" && parsed.Host != "github.com") {
		return "", fmt.Errorf("插件发布下载地址不是 GitHub 官方 HTTPS 地址")
	}
	data, err = p.downloadPlugin(ctx, downloadURL, options.Token, options.Proxy, "application/octet-stream", pluginArchiveLimit, logFn)
	if err != nil {
		return "", err
	}
	source := &plugininstall.Source{Repository: repo, Tag: release.Tag, Private: options.Token != "", InstalledAt: time.Now().UTC()}
	if !source.Private {
		source.Proxy = options.Proxy
	}
	return p.installArchive(ctx, data, asset.Name, options.DestDir, options.ExpectedName, source)
}

func (p *Plugin) downloadPlugin(ctx context.Context, target, token, proxy, accept string, limit int64, logFn func(string, ...any)) ([]byte, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("插件下载地址无效")
	}
	candidates := []string{target}
	// 私有仓库和 API 元数据永远直连；公共镜像仅处理公开 GitHub 安装包。
	if token == "" && proxy != "" && u.Scheme == "https" && u.Host == "github.com" {
		candidates = append([]string{proxy + "/" + target}, candidates...)
	}
	var lastErr error
	for index, candidate := range candidates {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, candidate, nil)
		if err != nil {
			return nil, fmt.Errorf("插件下载地址无效")
		}
		setPluginGitHubToken(req, token)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if logFn != nil {
			logFn("下载插件资源：%s%s\n", req.URL.Host, req.URL.EscapedPath())
		}
		response, err := p.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("插件下载连接失败")
		} else {
			if response.StatusCode != http.StatusOK {
				lastErr = fmt.Errorf("插件下载返回 HTTP %d", response.StatusCode)
				response.Body.Close()
			} else {
				body, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
				response.Body.Close()
				if readErr != nil {
					lastErr = fmt.Errorf("读取插件下载内容失败")
				} else if int64(len(body)) > limit {
					lastErr = fmt.Errorf("插件下载大小超过限制")
				} else {
					return body, nil
				}
			}
		}
		if index+1 < len(candidates) && logFn != nil {
			logFn("镜像下载失败，改用 GitHub 直连。\n")
		}
	}
	return nil, lastErr
}

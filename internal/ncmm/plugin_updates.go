package ncmm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/3899/ncmm/internal/atomicfile"
	"github.com/3899/ncmm/internal/filelock"
	"github.com/3899/ncmm/internal/plugininstall"
)

func readPluginSource(dir string) *plugininstall.Source {
	data, err := os.ReadFile(filepath.Join(dir, "source.json"))
	if err != nil || len(data) > 16<<10 {
		return nil
	}
	var source plugininstall.Source
	if json.Unmarshal(data, &source) != nil || source.Tag == "" {
		return nil
	}
	target, err := (plugininstall.Options{Target: source.Repository}).ParseTarget()
	if err != nil || target.Repository != source.Repository {
		return nil
	}
	proxy, err := plugininstall.NormalizeProxy(source.Proxy)
	if err != nil {
		return nil
	}
	source.Proxy = proxy
	if source.Private {
		source.Proxy = ""
	}
	return &source
}

func (p *Plugin) pluginUpdateCachePath() string {
	return filepath.Join(filepath.Dir(p.DefaultPluginDir()), "plugin-updates.json")
}
func (p *Plugin) readPluginUpdateCache() map[string]plugininstall.Update {
	result := map[string]plugininstall.Update{}
	data, err := os.ReadFile(p.pluginUpdateCachePath())
	if err == nil && len(data) <= 1<<20 {
		_ = json.Unmarshal(data, &result)
	}
	if result == nil {
		result = map[string]plugininstall.Update{}
	}
	return result
}

// CheckPluginUpdates 自动检查使用24小时磁盘缓存，手动检查也至少间隔60秒。
func (p *Plugin) CheckPluginUpdates(ctx context.Context, force bool) ([]plugininstall.Update, error) {
	sources := []pluginUpdateSource{}
	for _, info := range p.DiscoverPlugins() {
		if source := readPluginSource(filepath.Dir(info.Path)); source != nil {
			sources = append(sources, pluginUpdateSource{name: info.Name, source: *source})
		}
	}
	return p.checkPluginUpdateSources(ctx, sources, force)
}

type pluginUpdateSource struct {
	name   string
	source plugininstall.Source
}

// CheckPublicPluginRepositories 仅为内置公开市场查询版本，不安装程序、不接收凭据。
func (p *Plugin) CheckPublicPluginRepositories(ctx context.Context, repositories []string, force bool) ([]plugininstall.Update, error) {
	sources := make([]pluginUpdateSource, 0, len(repositories))
	for _, repository := range repositories {
		target, err := (plugininstall.Options{Target: repository}).ParseTarget()
		if err != nil || target.Repository != repository {
			return nil, fmt.Errorf("公开插件仓库标识无效")
		}
		sources = append(sources, pluginUpdateSource{source: plugininstall.Source{Repository: repository}})
	}
	return p.checkPluginUpdateSources(ctx, sources, force)
}

func (p *Plugin) checkPluginUpdateSources(ctx context.Context, sources []pluginUpdateSource, force bool) ([]plugininstall.Update, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	lock, err := filelock.Acquire(ctx, p.pluginUpdateCachePath()+".lock")
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	cache := p.readPluginUpdateCache()
	result := []plugininstall.Update{}
	for _, item := range sources {
		source := item.source
		key := strings.ToLower(source.Repository)
		update := cache[key]
		ttl := 24 * time.Hour
		if force {
			ttl = time.Minute
		}
		if update.CheckedAt.IsZero() || time.Since(update.CheckedAt) >= ttl || time.Since(update.CheckedAt) < 0 {
			update = plugininstall.Update{Repository: source.Repository, CheckedAt: time.Now().UTC()}
			if source.Private {
				update.Error = "私有仓库请指定 Token 手动安装目标 Tag；不会后台保存或发送凭据"
			} else {
				update.LatestTag, err = p.latestPluginTag(ctx, source.Repository, source.Proxy)
				if err != nil {
					update.Error = err.Error()
				}
			}
			cache[key] = update
		}
		update.Name = item.name
		update.CurrentTag = source.Tag
		update.UpdateAvailable = source.Tag != "" && update.Error == "" && update.LatestTag != "" && update.LatestTag != source.Tag
		result = append(result, update)
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return nil, err
	}
	if err = atomicfile.Write(p.pluginUpdateCachePath(), data, 0600); err != nil {
		return nil, err
	}
	return result, nil
}

func (p *Plugin) latestPluginTag(ctx context.Context, repo, proxy string) (string, error) {
	target := "https://github.com/" + repo + "/releases/latest"
	candidates := []string{target}
	if proxy != "" {
		candidates = append([]string{proxy + "/" + target}, candidates...)
	}
	var lastErr error
	for _, candidate := range candidates {
		tag, err := p.pluginTagRedirect(ctx, repo, candidate)
		if err == nil {
			return tag, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		lastErr = err
	}
	return "", lastErr
}

func (p *Plugin) pluginTagRedirect(ctx context.Context, repo, target string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
	client := *p.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("检查插件版本连接失败")
	}
	defer response.Body.Close()
	if response.StatusCode < 300 || response.StatusCode >= 400 {
		return "", fmt.Errorf("检查插件版本返回 HTTP %d", response.StatusCode)
	}
	location, err := req.URL.Parse(response.Header.Get("Location"))
	prefix := "/" + repo + "/releases/tag/"
	if err != nil || location.Scheme != "https" || location.Host != "github.com" || location.User != nil || location.RawQuery != "" || location.Fragment != "" || !strings.HasPrefix(location.Path, prefix) {
		return "", fmt.Errorf("插件版本跳转地址无效")
	}
	tag := strings.TrimPrefix(location.Path, prefix)
	if tag == "" || strings.ContainsAny(tag, "\r\n") {
		return "", fmt.Errorf("插件版本标签无效")
	}
	return tag, nil
}

func (p *Plugin) UpgradePlugin(ctx context.Context, name, token, proxy string) (string, error) {
	executable, err := p.FindPlugin(name)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(executable)
	if !strings.EqualFold(filepath.Base(dir), "ncmm-"+name) {
		return "", fmt.Errorf("此插件不是独立目录安装，请手动安装到标准插件目录")
	}
	source := readPluginSource(dir)
	if source == nil {
		return "", fmt.Errorf("插件没有可升级的 GitHub 来源，请手动安装")
	}
	if source.Private && strings.TrimSpace(token) == "" {
		return "", fmt.Errorf("升级私有插件需要本次提供 GitHub Token")
	}
	// 来源URL和目标名称由已安装元数据决定，不能由前端把升级变成另一插件安装。
	return p.InstallPlugin(ctx, PluginInstallOptions{Target: source.Repository, Token: token, Proxy: proxy, DestDir: filepath.Dir(dir), ExpectedName: name}, nil)
}

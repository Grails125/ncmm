package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
)

const (
	defaultBrowserUserAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/154.0.0.0 Safari/537.36"
	defaultIOSDisplayUserAgent = "neteasemusic/9.6.05 (iPhone; iOS 16.6.1; Scale/2.00)"
	userAgentUpdateTimeout     = 3 * time.Second
)

type userAgentVersionSource string

const (
	uaChrome userAgentVersionSource = "chrome"
	uaMac    userAgentVersionSource = "macos"
	uaIOS    userAgentVersionSource = "ios"
)

var userAgentVersionURLs = map[userAgentVersionSource]string{
	uaChrome: "https://versionhistory.googleapis.com/v1/chrome/platforms/win64/channels/stable/versions/all/releases?filter=endtime=none,fraction%3E0&order_by=version%20desc&pageSize=1",
	uaMac:    "https://music.163.com/api/osx/download/latest",
	uaIOS:    "https://itunes.apple.com/lookup?id=590338362&country=cn",
}

var userAgentNumericVersion = regexp.MustCompile(`^[0-9]{1,6}(?:\.[0-9]{1,6}){2,3}$`)
var macDownloadVersion = regexp.MustCompile(`^NeteaseCloudMusic_Music_official_([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)\.dmg$`)

type cachedUserAgentVersion struct {
	version    string
	retryAfter time.Time
	refreshing chan struct{}
}

type userAgentVersionCache struct {
	mu      sync.Mutex
	entries map[userAgentVersionSource]*cachedUserAgentVersion
	fetch   func(context.Context, userAgentVersionSource) (string, error)
}

var automaticUserAgentVersions = newUserAgentVersionCache(func(ctx context.Context, source userAgentVersionSource) (string, error) {
	return fetchOfficialUserAgentVersion(ctx, newUserAgentMetadataClient(), source, userAgentVersionURLs[source])
})

func newUserAgentVersionCache(fetch func(context.Context, userAgentVersionSource) (string, error)) *userAgentVersionCache {
	return &userAgentVersionCache{entries: make(map[userAgentVersionSource]*cachedUserAgentVersion), fetch: fetch}
}

// 缓存只保存公开版本号；并发客户端共用查询，失败保留最近成功结果并退避。
func (c *userAgentVersionCache) version(ctx context.Context, source userAgentVersionSource) string {
	c.mu.Lock()
	entry := c.entries[source]
	if entry == nil {
		entry = &cachedUserAgentVersion{}
		c.entries[source] = entry
	}
	previous := entry.version
	if time.Now().Before(entry.retryAfter) {
		c.mu.Unlock()
		return previous
	}
	if wait := entry.refreshing; wait != nil {
		c.mu.Unlock()
		select {
		case <-wait:
			return c.version(ctx, source)
		case <-ctx.Done():
			return previous
		}
	}
	entry.refreshing = make(chan struct{})
	c.mu.Unlock()

	version, err := c.fetch(ctx, source)
	c.mu.Lock()
	defer c.mu.Unlock()
	entry.retryAfter = time.Now().Add(15 * time.Minute)
	if err == nil && userAgentNumericVersion.MatchString(version) && versionAtLeast(version, entry.version) {
		entry.version = version
		entry.retryAfter = time.Now().Add(24 * time.Hour)
	}
	close(entry.refreshing)
	entry.refreshing = nil
	return entry.version
}

// 只识别本项目内置模板。抓包 UA、内部 build、系统版本和 Android 身份均不推测。
func (c *userAgentVersionCache) refresh(ctx context.Context, ua *UserAgentConfig, hasSessionIdentity bool) {
	if !ua.AutoUpdate {
		return
	}
	type replacement struct {
		target            *string
		source            userAgentVersionSource
		original, current string
	}
	var targets []replacement
	if ua.WEAPI == defaultBrowserUserAgent {
		targets = append(targets, replacement{&ua.WEAPI, uaChrome, "154.0.0.0", ua.WEAPI})
	}
	if !hasSessionIdentity {
		if ua.Default == DefaultWEAPIUserAgent {
			targets = append(targets, replacement{&ua.Default, uaMac, "2.3.17.1034", ua.Default})
		}
		if ua.WEAPI == DefaultWEAPIUserAgent {
			targets = append(targets, replacement{&ua.WEAPI, uaMac, "2.3.17.1034", ua.WEAPI})
		}
		if ua.EAPI == defaultIOSDisplayUserAgent {
			targets = append(targets, replacement{&ua.EAPI, uaIOS, "9.6.05", ua.EAPI})
		}
	}
	var wg sync.WaitGroup
	for _, target := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			version := c.version(ctx, target.source)
			if version == "" || !versionAtLeast(version, target.original) {
				return
			}
			if target.source == uaChrome {
				// Chrome 的简化 UA 仅公开主版本，保留既有 0.0.0 格式。
				version = strings.Split(version, ".")[0] + ".0.0.0"
			}
			*target.target = strings.Replace(target.current, target.original, version, 1)
		}()
	}
	wg.Wait()
}

func versionAtLeast(candidate, previous string) bool {
	if previous == "" {
		return true
	}
	a, b := strings.Split(candidate, "."), strings.Split(previous, ".")
	for i := range max(len(a), len(b)) {
		var av, bv int
		if i < len(a) {
			av, _ = strconv.Atoi(a[i])
		}
		if i < len(b) {
			bv, _ = strconv.Atoi(b[i])
		}
		if av != bv {
			return av > bv
		}
	}
	return true
}

func newUserAgentMetadataClient() *resty.Client {
	// 使用独立客户端，不继承业务 Cookie、代理凭据、调试输出或不安全的 TLS 配置。
	return resty.New().SetTimeout(userAgentUpdateTimeout).SetRetryCount(0).SetResponseBodyLimit(128 * 1024).
		SetRedirectPolicy(resty.RedirectPolicyFunc(func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }))
}

func fetchOfficialUserAgentVersion(ctx context.Context, client *resty.Client, source userAgentVersionSource, endpoint string) (string, error) {
	request := client.R().SetContext(ctx).SetHeader("Accept", "application/json").SetHeader("User-Agent", "ncmm-version-check")
	method := http.MethodGet
	if source == uaMac {
		method = http.MethodHead
	}
	response, err := request.Execute(method, endpoint)
	if err != nil {
		return "", fmt.Errorf("查询官方客户端版本失败: %w", err)
	}
	var version string
	if source == uaMac {
		if response.StatusCode() != http.StatusFound && response.StatusCode() != http.StatusTemporaryRedirect {
			return "", fmt.Errorf("官方桌面版本下载信息不可用")
		}
		location, err := url.Parse(response.Header().Get("Location"))
		if err != nil || location.Scheme != "https" || location.User != nil || !strings.HasSuffix(location.Hostname(), ".music.126.net") {
			return "", fmt.Errorf("官方桌面版本下载地址无效")
		}
		match := macDownloadVersion.FindStringSubmatch(path.Base(location.Path))
		if len(match) == 2 {
			version = match[1]
		}
	} else {
		if response.StatusCode() != http.StatusOK {
			return "", fmt.Errorf("官方版本查询暂不可用")
		}
		switch source {
		case uaChrome:
			var data struct {
				Releases []struct {
					Version  string  `json:"version"`
					Fraction float64 `json:"fraction"`
				} `json:"releases"`
			}
			if err := json.Unmarshal(response.Body(), &data); err != nil {
				return "", fmt.Errorf("Chrome 版本信息无效: %w", err)
			}
			if len(data.Releases) == 1 && data.Releases[0].Fraction > 0 {
				version = data.Releases[0].Version
			}
		case uaIOS:
			var data struct {
				Results []struct {
					TrackID  int64  `json:"trackId"`
					BundleID string `json:"bundleId"`
					Version  string `json:"version"`
				} `json:"results"`
			}
			if err := json.Unmarshal(response.Body(), &data); err != nil {
				return "", fmt.Errorf("App Store 版本信息无效: %w", err)
			}
			if len(data.Results) == 1 && data.Results[0].TrackID == 590338362 && data.Results[0].BundleID == "com.netease.cloudmusic" {
				version = data.Results[0].Version
			}
		}
	}
	if !userAgentNumericVersion.MatchString(version) || (source == uaIOS && len(strings.Split(version, ".")) != 3) || (source != uaIOS && len(strings.Split(version, ".")) != 4) {
		return "", fmt.Errorf("官方版本号不完整，沿用原 User-Agent")
	}
	return version, nil
}

func (c *Client) refreshBuiltinUserAgents() {
	if !c.cfg.UserAgent.AutoUpdate {
		return
	}
	// jar 仅含配置加载的 Cookie，此时尚未发送业务请求或构造补充身份头。
	hasIdentity := c.cookie.HasNonEmpty("os", "appver", "buildver", "osver")
	ctx, cancel := context.WithTimeout(context.Background(), userAgentUpdateTimeout)
	defer cancel()
	automaticUserAgentVersions.refresh(ctx, &c.cfg.UserAgent, hasIdentity)
}

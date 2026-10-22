package ncmm

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/3899/ncmm/config"
	"gopkg.in/yaml.v3"
)

// 每个实例都持有独立配置，不能展开并修改共享的默认配置对象。
func newDefaultRuntimeConfig() (*config.Config, error) {
	var cfg config.Config
	if err := yaml.Unmarshal(config.DefaultYAML(), &cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// normalizeRuntimePaths 仅调整内存中的配置引用，不改变工作目录或磁盘 YAML。
// notify.file 继续相对主配置目录解析，命令行显式输入文件继续使用调用目录。
func normalizeRuntimePaths(cfg *config.Config, home string) error {
	resolve := func(path string) string { return resolveHomePath(path, home) }
	resolveAll := func(paths []string) {
		for i, path := range paths {
			paths[i] = resolve(path)
		}
	}
	if cfg.Network != nil {
		cfg.Network.Cookie.Filepath = resolve(cfg.Network.Cookie.Filepath)
	}
	if cfg.Log != nil {
		cfg.Log.Rotate.Filename = resolve(cfg.Log.Rotate.Filename)
	}
	if cfg.Database != nil {
		cfg.Database.Path = resolve(cfg.Database.Path)
	}
	if cfg.Accounts != nil {
		cfg.Accounts.Main = resolve(cfg.Accounts.Main)
		cfg.Accounts.Primary = resolve(cfg.Accounts.Primary)
		resolveAll(cfg.Accounts.Secondary)
		if cfg.Accounts.Main != "" && cfg.Network != nil {
			cfg.Network.Cookie.Filepath = cfg.Accounts.Main
		}
		tokens, err := normalizeAccountTokens(cfg.Accounts.AntiCheatTokens, home)
		if err != nil {
			return fmt.Errorf("accounts.antiCheatTokens: %w", err)
		}
		cfg.Accounts.AntiCheatTokens = tokens
	}
	if cfg.FansGroup != nil {
		tokens, err := normalizeAccountTokens(cfg.FansGroup.CommentCheckTokens, home)
		if err != nil {
			return fmt.Errorf("fansGroup.commentCheckTokens: %w", err)
		}
		cfg.FansGroup.CommentCheckTokens = tokens
	}
	if cfg.PlayIds != nil {
		resolveAll(cfg.PlayIds.IDsFile)
	}
	if cfg.Musician != nil {
		resolveAll(cfg.Musician.Play.IDsFile)
	}
	if cfg.Note != nil {
		resolveAll(cfg.Note.TitlesFile)
		resolveAll(cfg.Note.MessagesFile)
		resolveAll(cfg.Note.ImageURLs)
	}
	if cfg.DailySongShare != nil {
		resolveAll(cfg.DailySongShare.TitlesFile)
		resolveAll(cfg.DailySongShare.MessagesFile)
		resolveAll(cfg.DailySongShare.ImageURLs)
	}
	return nil
}

func resolveHomePath(path, home string) string {
	if path == "" {
		return ""
	}
	if parsed, err := url.Parse(path); err == nil && (strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) {
		return path
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	path = os.Expand(path, func(name string) string {
		// Viper 会将令牌映射的键转为小写，因此同时识别其中的 ${home}。
		if strings.EqualFold(name, "HOME") {
			return home
		}
		return "${" + name + "}"
	})
	if !filepath.IsAbs(path) {
		path = filepath.Join(home, path)
	}
	return filepath.Clean(path)
}

func accountPathKey(path, home string) string {
	path = resolveHomePath(path, home)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return filepath.ToSlash(path)
}

func normalizeAccountTokens(tokens map[string]string, home string) (map[string]string, error) {
	if tokens == nil {
		return nil, nil
	}
	keys := make([]string, 0, len(tokens))
	for key := range tokens {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	normalized := make(map[string]string, len(tokens))
	for _, key := range keys {
		// 令牌查找沿用配置层大小写不敏感的语义，别名冲突也必须一致拒绝。
		canonical := strings.ToLower(accountPathKey(key, home))
		value := strings.TrimSpace(tokens[key])
		if previous := normalized[canonical]; previous != "" && value != "" && previous != value {
			return nil, fmt.Errorf("同一 Cookie 路径配置了不同的令牌，请保留一个有效配置")
		}
		if value != "" || normalized[canonical] == "" {
			normalized[canonical] = value
		}
	}
	return normalized, nil
}

// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

package ncmm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/internal/atomicfile"
	"github.com/3899/ncmm/internal/filelock"
	"github.com/3899/ncmm/internal/loginresult"
	"github.com/3899/ncmm/pkg/log"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type Login struct {
	root          *Root
	cmd           *cobra.Command
	l             *log.Logger
	isMain        bool
	noConfigWrite bool
	jsonResult    bool
}

func NewLogin(root *Root, l *log.Logger) *Login {
	c := &Login{
		root: root,
		l:    l,
		cmd: &cobra.Command{
			Use:     "login",
			Short:   "Login netease cloud music",
			Example: "  ncmm login -h\n  ncmm login qrcode\n  ncmm login phone\n  ncmm login cookiecloud\n  ncmm login cookie",
		},
	}
	c.addFlags()
	c.Add(qrcode(c, l))
	c.Add(phone(c, l))
	c.Add(cookieCloud(c, l))
	c.Add(cookie(c, l))

	return c
}

func (c *Login) addFlags() {
	c.cmd.PersistentFlags().BoolVarP(&c.isMain, "main", "m", false, "是否作为主账号登录 (默认登录为辅助账号)")
	c.cmd.PersistentFlags().BoolVar(&c.noConfigWrite, "no-config-write", false, "save the Cookie without updating config.yaml")
	c.cmd.PersistentFlags().BoolVar(&c.jsonResult, "json-result", false, "print a machine-readable login result")
}

func (c *Login) Add(command ...*cobra.Command) {
	c.cmd.AddCommand(command...)
}

func (c *Login) Command() *cobra.Command {
	return c.cmd
}

// saveLoginResult 处理登录 Cookie 的保存，根据主/辅账号智能推导输出文件名，并回写带有昵称注释的账号配置到 config.yaml
func (c *Login) saveLoginResult(ctx context.Context, nickname, avatarURL string, uid int64, tempCookieFile string, customOutput string, rawInputName string) error {
	var targetDir string
	if c.root.Cfg != nil && c.root.Cfg.Network != nil && c.root.Cfg.Network.Cookie.Filepath != "" {
		targetDir = filepath.Dir(c.root.Cfg.Network.Cookie.Filepath)
	}
	if targetDir == "" {
		home := c.root.Opts.Home
		if home == "" {
			home = config.HomeDir
		}
		targetDir = filepath.Clean(home)
	}

	var finalPath string
	if c.isMain {
		if customOutput != "" {
			if filepath.IsAbs(customOutput) {
				finalPath = customOutput
			} else {
				finalPath = filepath.Join(targetDir, customOutput)
			}
		} else if c.root.Cfg.Accounts != nil && c.root.Cfg.Accounts.Main != "" {
			finalPath = c.root.Cfg.Accounts.Main
		} else {
			finalPath = filepath.Join(targetDir, "cookie.json")
		}
	} else {
		var filename string
		if customOutput != "" {
			filename = customOutput
		} else if rawInputName != "" {
			base := filepath.Base(rawInputName)
			ext := filepath.Ext(base)
			nameWithoutExt := strings.TrimSuffix(base, ext)
			filename = nameWithoutExt + ".json"
		} else {
			filename = fmt.Sprintf("fan_%d.json", uid)
		}

		if filepath.IsAbs(filename) {
			finalPath = filename
		} else {
			finalPath = filepath.Join(targetDir, filename)
		}
	}
	finalPath = filepath.Clean(finalPath)

	// 如果临时 Cookie 文件存在且不等于最终路径，则将其移动/重命名过去
	if tempCookieFile != "" && tempCookieFile != finalPath {
		if err := os.MkdirAll(filepath.Dir(finalPath), 0755); err != nil {
			return fmt.Errorf("创建 Cookie 目录失败: %w", err)
		}
		if err := os.Rename(tempCookieFile, finalPath); err != nil {
			// 跨盘重命名失败兜底：复制 + 删除
			data, errRead := os.ReadFile(tempCookieFile)
			if errRead != nil {
				return fmt.Errorf("读取临时 Cookie 失败: %w", errRead)
			}
			if errWrite := os.WriteFile(finalPath, data, 0644); errWrite != nil {
				return fmt.Errorf("写入最终 Cookie 失败: %w", errWrite)
			}
			_ = os.Remove(tempCookieFile)
		}
	}

	// 规范化显示路径为包含 ${HOME} 的相对路径形式（若位于 Home 目录下）
	finalPathDisplay := formatHomePath(finalPath, c.root.Opts.Home)
	result := loginresult.Result{
		UID: uid, Nickname: nickname, AvatarURL: avatarURL, CookiePath: finalPath,
		AccountPath: finalPathDisplay, Main: c.isMain,
	}
	writeResult := func() error {
		if !c.jsonResult {
			return nil
		}
		return loginresult.Write(c.cmd.OutOrStdout(), result)
	}

	if c.noConfigWrite {
		c.cmd.Printf("Cookie 已保存至: %s\n", finalPath)
		return writeResult()
	}

	if c.root.CfgPath == "" || c.root.CfgPath == "default" {
		c.cmd.Printf("未检测到本地配置文件，跳过配置自动回写。Cookie 已保存至: %s\n", finalPath)
		return writeResult()
	}

	// 在同一个写锁中重新读取并复用既有路径拼写，保留令牌映射和账号元数据的键。
	storedPath, err := c.saveAccountConfig(ctx, finalPath, finalPathDisplay, nickname)
	if err != nil {
		return fmt.Errorf("回写配置文件失败: %w", err)
	}
	result.AccountPath = storedPath
	if c.root.Cfg.Accounts == nil {
		c.root.Cfg.Accounts = &config.AccountsConf{}
	}
	if c.isMain {
		c.root.Cfg.Accounts.Main = finalPath
		c.root.Cfg.Network.Cookie.Filepath = finalPath
	} else {
		found := false
		for _, path := range c.root.Cfg.Accounts.Secondary {
			if accountPathKey(path, c.root.Opts.Home) == accountPathKey(finalPath, c.root.Opts.Home) {
				found = true
				break
			}
		}
		if !found {
			c.root.Cfg.Accounts.Secondary = append(c.root.Cfg.Accounts.Secondary, finalPath)
		}
	}

	c.cmd.Printf("账号登录配置已更新成功！\nCookie 文件: %s\n", finalPath)
	return writeResult()
}

func (c *Login) saveAccountConfig(ctx context.Context, finalPath, displayPath, nickname string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	lock, err := filelock.Acquire(ctx, c.root.CfgPath+".lock")
	if err != nil {
		return "", err
	}
	defer lock.Close()
	data, err := os.ReadFile(c.root.CfgPath)
	if err != nil {
		return "", err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return "", err
	}
	var stored struct {
		Accounts *config.AccountsConf `yaml:"accounts"`
	}
	if err := document.Decode(&stored); err != nil {
		return "", err
	}
	if stored.Accounts != nil {
		paths := stored.Accounts.Secondary
		if c.isMain {
			paths = []string{stored.Accounts.Main, stored.Accounts.Primary}
		}
		for _, path := range paths {
			if path != "" && accountPathKey(path, c.root.Opts.Home) == accountPathKey(finalPath, c.root.Opts.Home) {
				displayPath = path
				break
			}
		}
	}
	if err := config.ApplyAccountUpdate(&document, displayPath, nickname, c.isMain); err != nil {
		return "", err
	}
	output, err := yaml.Marshal(&document)
	if err != nil {
		return "", err
	}
	if err := atomicfile.Write(c.root.CfgPath, output, 0644); err != nil {
		return "", err
	}
	return displayPath, nil
}

func formatHomePath(path, home string) string {
	if path == "" {
		return ""
	}
	if home == "" {
		return filepath.ToSlash(path)
	}
	home = filepath.Clean(home)
	cleanedPath := filepath.Clean(path)
	rel, err := filepath.Rel(home, cleanedPath)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
		return "${HOME}/" + filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/3899/ncmm/internal/plugininstall"
)

type pluginMarketEntry struct {
	Name        string                `json:"name"`
	Title       string                `json:"title"`
	Repository  string                `json:"repository"`
	Type        string                `json:"type"`
	Description string                `json:"description"`
	Homepage    string                `json:"homepage"`
	Author      string                `json:"author"`
	Features    []string              `json:"features"`
	Update      *plugininstall.Update `json:"update,omitempty"`
}

func publicPluginMarket() []pluginMarketEntry {
	return []pluginMarketEntry{{Name: "qqbot", Title: "QQ 机器人", Repository: "crossgg/ncmm-qqbot", Type: "daemon", Description: "通过 QQ 机器人接收通知和管理 NCMM；安装后需配置官方机器人凭据。", Homepage: "https://github.com/crossgg/ncmm-qqbot", Author: "crossgg", Features: []string{"QQ 私聊", "扫码登录", "Cookie 管理", "通知中继"}}}
}

type publicPluginVersionChecker interface {
	CheckPublicPluginRepositories(context.Context, []string, bool) ([]plugininstall.Update, error)
}

func (s *Server) publicPluginUpdates(ctx context.Context, force bool) ([]plugininstall.Update, error) {
	checker, ok := s.plugins.(publicPluginVersionChecker)
	if !ok {
		return nil, nil
	}
	repositories := []string{}
	for _, entry := range publicPluginMarket() {
		repositories = append(repositories, entry.Repository)
	}
	return checker.CheckPublicPluginRepositories(ctx, repositories, force)
}

// 市场只列出公开来源；展示入口不会下载或启动第三方程序。
func (s *Server) handlePluginMarket(w http.ResponseWriter, r *http.Request) {
	entries := publicPluginMarket()
	updates, err := s.publicPluginUpdates(r.Context(), false)
	for index := range entries {
		if err != nil {
			entries[index].Update = &plugininstall.Update{Repository: entries[index].Repository, Error: "版本暂不可用，可稍后检查更新"}
			continue
		}
		for _, update := range updates {
			if strings.EqualFold(update.Repository, entries[index].Repository) {
				entries[index].Update = &update
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handlePluginUpdates(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeError(w, 400, "插件管理不可用")
		return
	}
	var input struct {
		Force bool `json:"force"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&input); err != nil {
		writeError(w, 400, "更新检查参数无效")
		return
	}
	updates, err := s.plugins.CheckPluginUpdates(r.Context(), input.Force)
	if err != nil {
		writeError(w, 500, "检查插件更新失败")
		return
	}
	marketUpdates, err := s.publicPluginUpdates(r.Context(), input.Force)
	if err != nil {
		writeError(w, 500, "检查市场版本失败")
		return
	}
	updates = append(updates, marketUpdates...)
	writeJSON(w, 200, updates)
}

func (s *Server) handlePluginUpgrade(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeError(w, 400, "插件管理不可用")
		return
	}
	var input struct {
		Token string `json:"token"`
		Proxy string `json:"proxy"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16384)).Decode(&input); err != nil {
		writeError(w, 400, "升级参数无效")
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeError(w, 400, "插件名称不能为空")
		return
	}
	release, err := s.runner.reservePluginMutation(name)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	defer release()
	operation := func() error {
		_, err := s.plugins.UpgradePlugin(r.Context(), name, input.Token, input.Proxy)
		return err
	}
	if s.daemons != nil {
		err = s.daemons.mutate(name, true, operation)
	} else {
		err = operation()
	}
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "name": name})
}

func pluginTaskName(command string, args []string) string {
	if command != "plugin" {
		return ""
	}
	if len(args) >= 2 && args[0] == "run" && args[1] != "" && !strings.HasPrefix(args[1], "-") {
		return strings.ToLower(args[1])
	}
	// 全局参数可出现在子命令/名称前，不重复实现 Cobra 解析；未知布局采取保守互斥。
	return "*"
}

// 调用方持有 runner.mu。
func (m *runManager) pluginMutationBlocked(command string, args []string) bool {
	name := pluginTaskName(command, args)
	return m.mutatingPlugins[name] || (name == "*" && len(m.mutatingPlugins) > 0)
}

// 同一服务中的任务准入与升级互斥，网络期间不持有任务锁，也不阻塞其他插件。
func (m *runManager) reservePluginMutation(name string) (func(), error) {
	if m == nil {
		return func() {}, nil
	}
	key := strings.ToLower(strings.TrimSpace(name))
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mutatingPlugins[key] {
		return nil, fmt.Errorf("插件正在升级或卸载，请完成后重试")
	}
	for _, run := range m.runs {
		name := pluginTaskName(run.Command, run.Args)
		if (run.Status == "running" || run.Status == "queued") && (name == key || name == "*") {
			return nil, fmt.Errorf("插件仍有运行或排队任务，请先停止任务再升级或卸载")
		}
	}
	if m.mutatingPlugins == nil {
		m.mutatingPlugins = make(map[string]bool)
	}
	m.mutatingPlugins[key] = true
	return func() { m.mu.Lock(); delete(m.mutatingPlugins, key); m.mu.Unlock() }, nil
}

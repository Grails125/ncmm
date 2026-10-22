package webui

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type pluginMarketEntry struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Repository  string `json:"repository"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Homepage    string `json:"homepage"`
}

// 市场只列出公开来源；展示入口不会下载或启动第三方程序。
func (s *Server) handlePluginMarket(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []pluginMarketEntry{{Name: "qqbot", Title: "QQ 机器人", Repository: "crossgg/ncmm-qqbot", Type: "daemon", Description: "通过 QQ 机器人接收通知和管理 NCMM；安装后需配置官方机器人凭据。", Homepage: "https://github.com/crossgg/ncmm-qqbot"}})
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
	operation := func() error {
		_, err := s.plugins.UpgradePlugin(r.Context(), name, input.Token, input.Proxy)
		return err
	}
	var err error
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

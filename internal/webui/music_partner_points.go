package webui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/internal/partnerpoints"
	"gopkg.in/yaml.v3"
)

type partnerPointsReader func(context.Context, *api.Config, string) (*partnerpoints.Snapshot, error)

// 积分查询只接受已配置账号，不允许用请求参数读取任意 Cookie 文件。
func (s *Server) handlePartnerPointsGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	document, err := s.config.get()
	if err != nil || document.ParseError != "" {
		writeError(w, http.StatusInternalServerError, "读取账号配置失败，请检查配置后重试")
		return
	}
	root, ok := document.Data.(map[string]any)
	if !ok {
		writeError(w, http.StatusBadRequest, "账号配置格式无效")
		return
	}
	accounts := configuredPlayStatAccounts(root)
	account := r.URL.Query().Get("account")
	if account == "" && len(accounts) > 0 {
		account = accounts[0]
	}
	configured := false
	for _, candidate := range accounts {
		if candidate == account {
			configured = true
			break
		}
	}
	if !configured {
		writeError(w, http.StatusBadRequest, "请选择已配置的账号")
		return
	}
	var cfg struct {
		Network *api.Config `yaml:"network"`
	}
	if err := yaml.Unmarshal([]byte(document.Raw), &cfg); err != nil || cfg.Network == nil {
		writeError(w, http.StatusBadRequest, "网络配置无效，请检查配置后重试")
		return
	}
	reader := s.readPartnerPoints
	if reader == nil {
		reader = readPartnerPoints
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	snapshot, err := reader(ctx, cfg.Network, resolvePlayStatsPath(account, s.opts.Home))
	if err != nil || snapshot == nil {
		message := "音乐合伙人积分暂不可用，请确认账号已登录且有合伙人资格后刷新"
		var serviceErr *partnerpoints.ServiceError
		var code int64
		if errors.As(err, &serviceErr) {
			message = serviceErr.Error()
			code = serviceErr.Code
		}
		if s.opts.Output != nil {
			s.opts.Output("[webui] 读取音乐合伙人积分失败：账号=%q，服务码=%d，错误类型=%T\n", account, code, err)
		}
		writeError(w, http.StatusServiceUnavailable, message)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Account string `json:"account"`
		*partnerpoints.Snapshot
	}{Account: account, Snapshot: snapshot})
}

func readPartnerPoints(ctx context.Context, network *api.Config, cookieFile string) (*partnerpoints.Snapshot, error) {
	client, err := partnerpoints.NewReadClient(network, cookieFile)
	if err != nil {
		return nil, err
	}
	defer client.Close(context.Background())
	snapshot, err := partnerpoints.Read(ctx, weapi.New(client))
	if err != nil {
		return nil, fmt.Errorf("查询音乐合伙人积分：%w", err)
	}
	return snapshot, nil
}

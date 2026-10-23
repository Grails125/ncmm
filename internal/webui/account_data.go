package webui

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/internal/partnerpoints"
	"gopkg.in/yaml.v3"
)

type accountDataErrors struct {
	Partner string `json:"partner,omitempty"`
	Growth  string `json:"growth,omitempty"`
	Yunbei  string `json:"yunbei,omitempty"`
}

type accountDataResponse struct {
	Account       string                  `json:"account"`
	Partner       *partnerpoints.Snapshot `json:"partner"`
	GrowthTotal   *int64                  `json:"growthTotal"`
	VIPLevel      *int64                  `json:"vipLevel"`
	YunbeiBalance *int64                  `json:"yunbeiBalance"`
	UpdatedAt     time.Time               `json:"updatedAt"`
	Errors        accountDataErrors       `json:"errors"`
}

type accountDataResult struct {
	Partner                                *partnerpoints.Snapshot
	GrowthTotal, VIPLevel, YunbeiBalance   *int64
	PartnerError, GrowthError, YunbeiError error
}

type accountDataReader func(context.Context, *api.Config, string) accountDataResult

// 账号数据只接受已配置的账号标识，单个来源失败不抹去其他来源的查询结果。
func (s *Server) handleAccountDataGet(w http.ResponseWriter, r *http.Request) {
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
	reader := s.readAccountData
	if reader == nil {
		reader = readAccountData
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	data := reader(ctx, cfg.Network, resolvePlayStatsPath(account, s.opts.Home))
	response := accountDataResponse{Account: account, Partner: data.Partner, GrowthTotal: data.GrowthTotal, VIPLevel: data.VIPLevel, YunbeiBalance: data.YunbeiBalance, UpdatedAt: time.Now().UTC()}
	if data.PartnerError != nil || data.Partner == nil {
		response.Partner = nil
		response.Errors.Partner = "音乐合伙人积分暂不可用，请确认账号已登录且有参与资格后刷新"
		var serviceErr *partnerpoints.ServiceError
		if errors.As(data.PartnerError, &serviceErr) {
			response.Errors.Partner = serviceErr.Error()
		}
	}
	if data.GrowthError != nil || data.GrowthTotal == nil || data.VIPLevel == nil {
		response.Errors.Growth = "VIP 成长值或等级暂不可用，请稍后刷新"
	}
	if data.YunbeiError != nil || data.YunbeiBalance == nil {
		response.YunbeiBalance = nil
		response.Errors.Yunbei = "云贝余额暂不可用，请稍后刷新"
	}
	if s.opts.Output != nil && (response.Errors.Partner != "" || response.Errors.Growth != "" || response.Errors.Yunbei != "") {
		s.opts.Output("[webui] 读取账号数据部分失败：账号=%q，合伙人错误类型=%T，成长值错误类型=%T，云贝错误类型=%T\n", account, data.PartnerError, data.GrowthError, data.YunbeiError)
	}
	writeJSON(w, http.StatusOK, response)
}

func readAccountData(ctx context.Context, network *api.Config, cookieFile string) accountDataResult {
	client, err := partnerpoints.NewReadClient(network, cookieFile)
	if err != nil {
		return accountDataResult{PartnerError: err, GrowthError: err, YunbeiError: err}
	}
	defer client.Close(context.Background())
	return readAccountDataClient(ctx, client)
}

func readAccountDataClient(ctx context.Context, client *api.Client) accountDataResult {
	var result accountDataResult
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		result.Partner, result.PartnerError = partnerpoints.Read(ctx, weapi.New(client))
	}()
	go func() {
		defer wg.Done()
		result.GrowthTotal, result.VIPLevel, result.GrowthError = readAccountGrowth(ctx, client)
	}()
	go func() {
		defer wg.Done()
		result.YunbeiBalance, result.YunbeiError = readAccountYunbei(ctx, client)
	}()
	wg.Wait()
	return result
}

// 使用可空字段区分平台返回的零值和字段缺失，不查询领取或任务完成接口。
func readAccountGrowth(ctx context.Context, client *api.Client) (*int64, *int64, error) {
	var response struct {
		Code int64 `json:"code"`
		Data struct {
			UserLevel struct {
				GrowthPoint *int64 `json:"growthPoint"`
				Level       *int64 `json:"level"`
			} `json:"userLevel"`
		} `json:"data"`
	}
	req := &eapi.VipGrowPointReq{DeviceId: client.GetDeviceId(), OS: "iOS", VerifyId: 1, Header: struct{}{}, ER: true}
	opts := api.NewOptions()
	opts.CryptoMode = api.CryptoModeEAPI
	_, err := client.Request(ctx, "https://interface3.music.163.com/eapi/vipnewcenter/app/level/growhpoint/basic", req, &response, opts)
	if err != nil || response.Code != 200 {
		return nil, nil, errors.New("VIP 成长值读取失败")
	}
	level := response.Data.UserLevel
	if level.GrowthPoint == nil || level.Level == nil {
		return level.GrowthPoint, level.Level, errors.New("VIP 成长值数据不完整")
	}
	return level.GrowthPoint, level.Level, nil
}

func readAccountYunbei(ctx context.Context, client *api.Client) (*int64, error) {
	balance, err := weapi.New(client).YunBeiAvailableBalance(ctx)
	if err != nil {
		return nil, errors.New("云贝余额读取失败")
	}
	return &balance, nil
}

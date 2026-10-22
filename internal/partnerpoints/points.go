// Package partnerpoints 读取音乐合伙人当前期积分，不执行评分或账户写入。
package partnerpoints

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/pkg/log"
)

type Snapshot struct {
	Period              *int64    `json:"period"`
	Week                *int64    `json:"week"`
	StartDate           *int64    `json:"startDate"`
	EndDate             *int64    `json:"endDate"`
	Integral            *int64    `json:"integral"`
	CurrentWeekIntegral *int64    `json:"currentWeekIntegral"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

type Reader interface {
	PartnerHome(context.Context, *weapi.PartnerHomeReq) (*weapi.PartnerHomeResp, error)
}

// ServiceError 仅保留服务码，不包含服务端消息、响应体或请求凭据。
type ServiceError struct {
	Code int64
}

func (e *ServiceError) Error() string {
	if e.Code == 703 {
		return "当前账号尚未成为音乐合伙人，积分暂不可用"
	}
	if e.Code == 301 || e.Code == 401 || e.Code == 403 || e.Code == 419 {
		return "音乐合伙人积分读取失败，请重新登录并确认参与资格"
	}
	return fmt.Sprintf("音乐合伙人积分暂不可用（服务码 %d）", e.Code)
}

func Read(ctx context.Context, client Reader) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("音乐合伙人积分读取会话不可用")
	}
	response, err := client.PartnerHome(ctx, &weapi.PartnerHomeReq{})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		var status *api.HTTPStatusError
		if errors.As(err, &status) {
			if status.ServiceCode != 0 {
				return nil, &ServiceError{Code: status.ServiceCode}
			}
			if status.StatusCode == http.StatusUnauthorized || status.StatusCode == http.StatusForbidden {
				return nil, &ServiceError{Code: int64(status.StatusCode)}
			}
		}
		return nil, errors.New("音乐合伙人积分读取失败，请稍后重试")
	}
	if response == nil {
		return nil, errors.New("音乐合伙人积分暂不可用")
	}
	if response.Code != 200 {
		return nil, &ServiceError{Code: response.Code}
	}
	data := response.Data
	if data.Integral.Integral == nil && data.Integral.CurrentWeekIntegral == nil {
		return nil, errors.New("音乐合伙人积分暂不可用")
	}
	return &Snapshot{
		Period: copyNumber(data.Period), Week: copyNumber(data.Week),
		StartDate: copyNumber(data.StartDate), EndDate: copyNumber(data.EndDate),
		Integral: copyNumber(data.Integral.Integral), CurrentWeekIntegral: copyNumber(data.Integral.CurrentWeekIntegral),
		UpdatedAt: time.Now().UTC(),
	}, nil
}

// NewReadClient 使用已有 Cookie 建立独立查询会话，关闭时也不回写源文件。
func NewReadClient(network *api.Config, cookieFile string) (*api.Client, error) {
	if network == nil {
		return nil, errors.New("音乐合伙人积分读取网络配置不可用")
	}
	if strings.TrimSpace(cookieFile) == "" {
		return nil, errors.New("请先登录账号后读取音乐合伙人积分")
	}
	path, err := filepath.Abs(cookieFile)
	if err != nil {
		return nil, errors.New("音乐合伙人积分读取账号配置无效")
	}
	cfg := *network
	cfg.Cookie.Filepath = path
	cfg.Cookie.ReadOnly = true
	cfg.Debug = false
	cfg.Retry = 0
	if cfg.Timeout <= 0 || cfg.Timeout > 15*time.Second {
		cfg.Timeout = 15 * time.Second
	}
	client, err := api.NewClient(&cfg, log.Default)
	if err != nil {
		return nil, errors.New("账号 Cookie 无法读取，请重新登录后查看音乐合伙人积分")
	}
	transport, ok := client.GetClient().Transport.(*http.Transport)
	if !ok {
		_ = client.Close(context.Background())
		return nil, errors.New("音乐合伙人积分安全连接不可用")
	}
	transport = transport.Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		if transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
			transport.TLSClientConfig.MinVersion = tls.VersionTLS12
		}
	}
	transport.TLSClientConfig.InsecureSkipVerify = false
	client.GetClient().Transport = transport
	const endpoint = "https://interface.music.163.com"
	session, hasSession := client.Cookie(endpoint, "MUSIC_U")
	if !hasSession || strings.TrimSpace(session.Value) == "" {
		session, hasSession = client.Cookie("https://music.163.com", "MUSIC_U")
	}
	csrf, hasCSRF := client.GetCSRF(endpoint)
	if !hasSession || strings.TrimSpace(session.Value) == "" || !hasCSRF || strings.TrimSpace(csrf) == "" {
		_ = client.Close(context.Background())
		return nil, errors.New("账号 Cookie 不完整，请重新登录后查看音乐合伙人积分")
	}
	return client, nil
}

func copyNumber(value *int64) *int64 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

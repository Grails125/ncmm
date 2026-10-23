package eapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/3899/ncmm/api"
)

// PartnerPublishError 仅保存发布阶段和数值状态，不保存底层错误、请求或响应内容。
// NotSent 仅用于确定尚未调用发布请求的本地校验失败。
type PartnerPublishError struct {
	Phase       string
	Mode        api.CryptoMode
	NotSent     bool
	Rejected    bool
	HTTPStatus  int
	ServiceCode int64
	cancelled   bool
	deadline    bool
}

func (e *PartnerPublishError) Error() string {
	phase := "请求或响应处理"
	switch e.Phase {
	case "参数校验", "协议配置", "请求准备", "请求取消", "发布回执", "业务拒绝":
		phase = e.Phase
	}
	mode := "未知"
	if e.Mode == api.CryptoModeEAPI || e.Mode == api.CryptoModeXEAPI {
		mode = string(e.Mode)
	}
	status := "发布结果未确认"
	if e.NotSent {
		status = "尚未发送发布请求"
	} else if e.Rejected {
		status = "服务端明确拒绝发布"
	}
	return fmt.Sprintf("%s（阶段=%s，协议=%s，HTTP=%d，服务码=%d）", status, phase, mode, e.HTTPStatus, e.ServiceCode)
}

func (e *PartnerPublishError) Is(target error) bool {
	return (e.cancelled && target == context.Canceled) || (e.deadline && target == context.DeadlineExceeded)
}

func (e *PartnerPublishError) Unwrap() error {
	if e.HTTPStatus > 0 {
		return &api.HTTPStatusError{StatusCode: e.HTTPStatus, ServiceCode: e.ServiceCode}
	}
	return nil
}

func partnerPublishFailure(ctx context.Context, mode api.CryptoMode, phase string, notSent bool, err error) *PartnerPublishError {
	failure := &PartnerPublishError{Phase: "请求或响应处理", NotSent: notSent}
	switch phase {
	case "参数校验", "协议配置", "请求准备", "请求取消", "发布回执", "业务拒绝":
		failure.Phase = phase
	}
	if mode == api.CryptoModeEAPI || mode == api.CryptoModeXEAPI {
		failure.Mode = mode
	}
	failure.cancelled = errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled)
	failure.deadline = errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded)
	if failure.cancelled || failure.deadline {
		failure.Phase = "请求取消"
	}
	var status *api.HTTPStatusError
	if errors.As(err, &status) {
		failure.HTTPStatus = status.StatusCode
		failure.ServiceCode = status.ServiceCode
	}
	return failure
}

func partnerPublishRejection(ctx context.Context, mode api.CryptoMode) *PartnerPublishError {
	failure := partnerPublishFailure(ctx, mode, "业务拒绝", false, nil)
	failure.Phase = "业务拒绝"
	failure.Rejected = true
	failure.HTTPStatus = 200
	failure.ServiceCode = 400
	return failure
}

func partnerPublishPreflight(ctx context.Context, client *api.Client, mode api.CryptoMode) error {
	if err := ctx.Err(); err != nil {
		return partnerPublishFailure(ctx, mode, "请求取消", true, err)
	}
	if client == nil || (mode != api.CryptoModeEAPI && mode != api.CryptoModeXEAPI) {
		return partnerPublishFailure(ctx, mode, "协议配置", true, nil)
	}
	if strings.TrimSpace(client.UserAgent(mode)) == "" {
		return partnerPublishFailure(ctx, mode, "协议配置", true, nil)
	}
	return nil
}

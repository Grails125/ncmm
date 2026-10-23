package eapi

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/3899/ncmm/api"
)

// ImageUploadError 不保存图片路径、上传地址、令牌、响应正文或底层错误。
type ImageUploadError struct {
	Stage       string
	Kind        string
	HTTPStatus  int
	ServiceCode int64
	cancelled   bool
	deadline    bool
}

func imageUploadStage(stage string) string {
	switch stage {
	case "读取图片", "识别格式", "计算摘要", "分配凭据", "查询节点", "构造地址", "上传文件", "读取图片信息", "编码图片信息":
		return stage
	default:
		return "图片上传"
	}
}

func imageUploadKind(kind string) string {
	switch kind {
	case "文件读取失败", "图片格式不支持", "请求超时", "已取消", "HTTP拒绝", "服务拒绝", "凭据缺失", "节点无效", "响应格式无效", "元数据缺失", "上传不完整", "编码失败", "路径无效":
		return kind
	default:
		return "请求失败"
	}
}

func (e *ImageUploadError) Error() string {
	return fmt.Sprintf("图片上传失败（阶段=%s，分类=%s，HTTP=%d，服务码=%d）", imageUploadStage(e.Stage), imageUploadKind(e.Kind), e.HTTPStatus, e.ServiceCode)
}

func (e *ImageUploadError) Is(target error) bool {
	return (e.cancelled && target == context.Canceled) || (e.deadline && target == context.DeadlineExceeded)
}

func (e *ImageUploadError) Unwrap() error {
	if e.HTTPStatus > 0 {
		return &api.HTTPStatusError{StatusCode: e.HTTPStatus, ServiceCode: e.ServiceCode}
	}
	return nil
}

func imageUploadFailure(ctx context.Context, stage, kind string, status int, code int64, err error) *ImageUploadError {
	failure := &ImageUploadError{Stage: imageUploadStage(stage), Kind: imageUploadKind(kind), HTTPStatus: status, ServiceCode: code}
	var previous *ImageUploadError
	if errors.As(err, &previous) {
		failure.Kind = imageUploadKind(previous.Kind)
		failure.HTTPStatus, failure.ServiceCode = previous.HTTPStatus, previous.ServiceCode
	}
	var httpFailure *api.HTTPStatusError
	if errors.As(err, &httpFailure) {
		failure.HTTPStatus, failure.ServiceCode = httpFailure.StatusCode, httpFailure.ServiceCode
		if httpFailure.StatusCode != 200 && (kind == "" || kind == "请求失败") {
			failure.Kind = "HTTP拒绝"
		}
	}
	failure.cancelled = errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled)
	failure.deadline = errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded)
	var networkError net.Error
	switch {
	case failure.cancelled:
		failure.Kind = "已取消"
	case failure.deadline || (errors.As(err, &networkError) && networkError.Timeout()):
		failure.Kind = "请求超时"
	}
	return failure
}

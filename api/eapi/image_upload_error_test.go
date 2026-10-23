package eapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageUploadStageDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, target, body, stage, kind string
		status, wantHTTP                int
		code                            int64
	}{
		{"allocation rejection", "alloc", `{"code":301,"message":"private-response"}`, "分配凭据", "服务拒绝", 200, 200, 301},
		{"allocation missing fields", "alloc", `{"code":200,"result":{"docId":"123","token":"private-token"}}`, "分配凭据", "凭据缺失", 200, 200, 200},
		{"allocation http rejection", "alloc", `{"code":403,"message":"private-response"}`, "分配凭据", "HTTP拒绝", 403, 403, 403},
		{"node rejection", "node", `{}`, "查询节点", "HTTP拒绝", 403, 403, 0},
		{"storage rejection", "upload", `{"private":"response"}`, "上传文件", "HTTP拒绝", 400, 400, 0},
		{"storage service rejection", "upload", `{"errCode":"1234","private":"response"}`, "上传文件", "服务拒绝", 200, 200, 1234},
		{"storage malformed", "upload", `private-response`, "上传文件", "响应格式无效", 200, 200, 0},
		{"storage incomplete", "upload", `{"offset":0}`, "上传文件", "上传不完整", 200, 200, 0},
		{"metadata rejection", "info", `{"code":500,"message":"private-response"}`, "读取图片信息", "服务拒绝", 200, 200, 500},
		{"metadata missing", "info", `{"code":200,"picInfo":{}}`, "读取图片信息", "元数据缺失", 200, 200, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			image := nosTestImage(t, "jpg")
			imagePath := filepath.Join(t.TempDir(), "cover-without-extension")
			if err := os.WriteFile(imagePath, image, 0600); err != nil {
				t.Fatal(err)
			}
			client := newNOSTestAPI(t, func(request *http.Request) (*http.Response, error) {
				var target, body string
				switch request.URL.Host + request.URL.Path {
				case "music.163.com/eapi/nos/token/alloc":
					target, body = "alloc", `{"code":200,"result":{"bucket":"yyimgs","docId":"123","objectKey":"image.jpg","token":"private-token"}}`
				case "wanproxy.127.net/lbs":
					target, body = "node", `{"upload":["https://nosup-hz1.127.net"]}`
				case "nosup-hz1.127.net/yyimgs/image.jpg":
					target, body = "upload", fmt.Sprintf(`{"offset":%d}`, len(image))
				case "music.163.com/eapi/upload/event/img/v1":
					target, body = "info", `{"code":200,"picInfo":{"originId":123,"squareId":124,"rectangleId":125,"width":2,"height":2}}`
				default:
					t.Fatalf("意外的离线请求：%s", request.URL.Path)
				}
				if target == tc.target {
					return nosTestResponse(request, tc.status, tc.body), nil
				}
				return nosTestResponse(request, 200, body), nil
			})
			_, err := client.EventUploadImage(t.Context(), imagePath)
			var failure *ImageUploadError
			if !errors.As(err, &failure) || failure.Stage != tc.stage || failure.Kind != tc.kind || failure.HTTPStatus != tc.wantHTTP || failure.ServiceCode != tc.code {
				t.Fatalf("上传错误阶段或数值丢失：%v", err)
			}
			if strings.Contains(fmt.Sprintf("%#v %s", failure, failure), "private-") || strings.Contains(failure.Error(), imagePath) {
				t.Fatal("上传诊断泄露了路径、上传令牌或响应内容")
			}
		})
	}
}

func TestImageUploadErrorKeepsCancellationAndDropsUnsafeDetails(t *testing.T) {
	for _, target := range []error{context.Canceled, context.DeadlineExceeded} {
		failure := imageUploadFailure(t.Context(), "上传文件", "请求失败", 0, 0, fmt.Errorf("private-url-token: %w", target))
		if !errors.Is(failure, target) || strings.Contains(fmt.Sprintf("%#v %s", failure, failure), "private-") {
			t.Fatal("取消或超时语义丢失，或保留了敏感底层信息")
		}
		failure = imageUploadFailure(t.Context(), "读取图片信息", "请求失败", 0, 0, failure)
		if !errors.Is(failure, target) {
			t.Fatal("逐层包装后仍需保留取消或超时")
		}
	}
	failure := imageUploadFailure(t.Context(), "private-stage", "private-kind", 0, 0, errors.New("private-error"))
	if strings.Contains(fmt.Sprintf("%#v %s", failure, failure), "private-") {
		t.Fatal("未知阶段和分类不得被保存在诊断结构中")
	}
}

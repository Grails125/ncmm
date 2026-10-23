package eapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/pkg/cookie"
	"github.com/3899/ncmm/pkg/log"
)

type nosTestTransport func(*http.Request) (*http.Response, error)

func (f nosTestTransport) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func nosTestResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func newNOSTestAPI(t *testing.T, transport nosTestTransport) *Api {
	t.Helper()
	client, err := api.NewClient(&api.Config{Timeout: time.Second, Retry: 0, Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, log.Default)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	client.GetClient().Transport = transport
	for _, host := range []string{"music.163.com", "wanproxy.127.net", "nosup-hz1.127.net"} {
		origin, _ := url.Parse("https://" + host)
		client.SetCookies(origin, []*http.Cookie{{Name: "private-session", Value: "must-not-reach-nos", Path: "/"}})
	}
	return New(client)
}

func TestNOSNodeDiscoveryUsesHTTPSActualBucketAndIsolatedCookies(t *testing.T) {
	client := newNOSTestAPI(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" || request.URL.Host != "wanproxy.127.net" || request.URL.Query().Get("bucketname") != "yyimgs" {
			t.Errorf("节点发现协议错误: %s", request.URL)
		}
		if request.Header.Get("Cookie") != "" || request.Header.Get("X-Nos-Token") != "" {
			t.Error("节点发现不应携带凭据")
		}
		return nosTestResponse(request, 200, `{"upload":["http://nosup-hz1.127.net"]}`), nil
	})
	node, err := client.getUploadNode(context.Background(), "yyimgs")
	if err != nil || node != "https://nosup-hz1.127.net" {
		t.Fatalf("node=%q err=%v", node, err)
	}
}

func TestNOSNodeDiscoveryHandlesServerFailuresWithoutHidingRejections(t *testing.T) {
	for _, status := range []int{500, 502, 503, 401, 403, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			client := newNOSTestAPI(t, func(request *http.Request) (*http.Response, error) {
				return nosTestResponse(request, status, `{"error":"not available"}`), nil
			})
			node, err := client.getUploadNode(context.Background(), "yyimgs")
			if status >= 500 {
				if err != nil || node != "https://nosup-hz1.127.net" {
					t.Fatalf("临时节点故障不应阻断官方备用上传: node=%q err=%v", node, err)
				}
			} else if err == nil || node != "" {
				t.Fatalf("不能掩盖明确拒绝: node=%q err=%v", node, err)
			}
		})
	}
}

func TestNOSNodeDiscoveryRejectsUntrustedNodesAndCancellation(t *testing.T) {
	for _, node := range []string{"https://evil.invalid", "https://nosup-hz1.127.net.evil.invalid", "https://user:pass@nosup-hz1.127.net", "https://nosup-hz1.127.net/path", "https://nosup-hz1.127.net?token=secret", "https://nosup-hz1.127.net:444"} {
		t.Run(node, func(t *testing.T) {
			client := newNOSTestAPI(t, func(request *http.Request) (*http.Response, error) {
				body, _ := json.Marshal(map[string][]string{"upload": {node}})
				return nosTestResponse(request, 200, string(body)), nil
			})
			if got, err := client.getUploadNode(context.Background(), "yyimgs"); err == nil || got != "" {
				t.Fatalf("接受了不可信上传节点: %q", got)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	client := newNOSTestAPI(t, func(request *http.Request) (*http.Response, error) {
		called = true
		return nil, ctx.Err()
	})
	if _, err := client.getUploadNode(ctx, "yyimgs"); err == nil || called {
		t.Fatalf("取消后不能查询或启用备用节点: called=%t err=%v", called, err)
	}
}

func TestEventImageUploadsUseAllocatedBucketAndValidatedNOSPOST(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t", batch), func(t *testing.T) {
			image := nosTestImage(t, "jpg")
			imagePath := filepath.Join(t.TempDir(), "image.jpg")
			if err := os.WriteFile(imagePath, image, 0600); err != nil {
				t.Fatal(err)
			}
			var allocCalls, lbsCalls, uploads, imageInfoCalls int
			client := newNOSTestAPI(t, func(request *http.Request) (*http.Response, error) {
				switch request.URL.Host + request.URL.Path {
				case "music.163.com/eapi/nos/token/alloc":
					allocCalls++
					return nosTestResponse(request, 200, `{"code":200,"result":{"bucket":"yyimgs","docId":"123","objectKey":"folder%2Fimage.jpg","token":"nos-only-token"}}`), nil
				case "wanproxy.127.net/lbs":
					lbsCalls++
					if request.URL.Query().Get("bucketname") != "yyimgs" {
						t.Error("节点发现必须使用分配到的 bucket")
					}
					return nosTestResponse(request, 500, `{}`), nil
				case "nosup-hz1.127.net/yyimgs/folder/image.jpg":
					uploads++
					if request.Method != http.MethodPost || request.URL.Scheme != "https" || request.Header.Get("Cookie") != "" || request.Header.Get("X-Nos-Token") != "nos-only-token" {
						t.Errorf("NOS 上传协议或凭据隔离错误: method=%s scheme=%s cookies=%t", request.Method, request.URL.Scheme, request.Header.Get("Cookie") != "")
					}
					if request.URL.Query().Get("complete") != "true" || request.URL.Query().Get("offset") != "0" {
						t.Error("缺少完整上传参数")
					}
					if request.Header.Get("Content-Type") != "image/jpeg" || request.ContentLength != int64(len(image)) {
						t.Errorf("图片上传 MIME 或长度错误: mime=%s length=%d", request.Header.Get("Content-Type"), request.ContentLength)
					}
					body, _ := io.ReadAll(request.Body)
					if string(body) != string(image) {
						t.Error("上传图片内容改变")
					}
					return nosTestResponse(request, 200, fmt.Sprintf(`{"requestId":"ok","offset":%d}`, len(image))), nil
				case "music.163.com/eapi/upload/event/img/v1":
					imageInfoCalls++
					return nosTestResponse(request, 200, `{"code":200,"picInfo":{"originId":123,"squareId":124,"rectangleId":125,"width":800,"height":600}}`), nil
				default:
					t.Errorf("不应请求该地址: %s", request.URL)
					return nosTestResponse(request, 500, `{}`), nil
				}
			})
			var pics string
			var err error
			if batch {
				pics, err = client.EventUploadImages(context.Background(), []string{imagePath})
			} else {
				pics, err = client.EventUploadImage(context.Background(), imagePath)
			}
			if err != nil {
				t.Fatal(err)
			}
			var info []eventImgPicInfo
			if err := json.Unmarshal([]byte(pics), &info); err != nil || len(info) != 1 || info[0].Width != 800 || info[0].OriginId != "123" {
				t.Fatalf("图片元数据错误: %s err=%v", pics, err)
			}
			if allocCalls != 1 || lbsCalls != 1 || uploads != 1 || imageInfoCalls != 1 {
				t.Fatalf("调用次数错误: %d/%d/%d/%d", allocCalls, lbsCalls, uploads, imageInfoCalls)
			}
		})
	}
}

func TestNOSUploadRejectsRedirectsServerErrorsAndIncompleteWrites(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"redirect", 307, ""}, {"server", 500, `{"errMsg":"secret"}`}, {"logical", 200, `{"errCode":"InvalidToken"}`}, {"partial", 200, `{"offset":1}`}, {"empty-result", 200, `{}`}, {"malformed", 200, `not-json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := newNOSTestAPI(t, func(request *http.Request) (*http.Response, error) {
				calls++
				response := nosTestResponse(request, tc.status, tc.body)
				response.Header.Set("Location", "https://evil.invalid/receive")
				return response, nil
			})
			err := client.rawUpload(context.Background(), "https://nosup-hz1.127.net/yyimgs/image.jpg?complete=true", map[string]string{"X-Nos-Token": "nos-only-token"}, []byte("complete image"))
			if err == nil || calls != 1 || strings.Contains(err.Error(), "secret") {
				t.Fatalf("不应重定向、重试或误报成功: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestEventImageUploadDoesNotReturnPicsUntilStorageAndMetadataSucceed(t *testing.T) {
	for _, batch := range []bool{false, true} {
		for _, failure := range []string{"storage-rejected", "missing-metadata", "missing-token", "unsafe-key"} {
			t.Run(fmt.Sprintf("batch=%t/%s", batch, failure), func(t *testing.T) {
				imagePath := filepath.Join(t.TempDir(), "image.jpg")
				image := nosTestImage(t, "jpg")
				if err := os.WriteFile(imagePath, image, 0600); err != nil {
					t.Fatal(err)
				}
				uploads, metadata := 0, 0
				client := newNOSTestAPI(t, func(request *http.Request) (*http.Response, error) {
					status, body := 200, `{}`
					switch request.URL.Host + request.URL.Path {
					case "music.163.com/eapi/nos/token/alloc":
						token, key := "nos-only-token", "image.jpg"
						if failure == "missing-token" {
							token = ""
						}
						if failure == "unsafe-key" {
							key = "../another-bucket/image.jpg"
						}
						body = fmt.Sprintf(`{"code":200,"result":{"bucket":"yyimgs","docId":"123","objectKey":%q,"token":%q}}`, key, token)
					case "wanproxy.127.net/lbs":
						body = `{"upload":["https://nosup-hz1.127.net"]}`
					case "nosup-hz1.127.net/yyimgs/image.jpg":
						uploads++
						body = fmt.Sprintf(`{"requestId":"ok","offset":%d}`, len(image))
						if failure == "storage-rejected" {
							status = 403
						}
					case "music.163.com/eapi/upload/event/img/v1":
						metadata++
						body = `{"code":200,"picInfo":{}}`
					default:
						t.Errorf("不应请求该地址: %s", request.URL)
					}
					return nosTestResponse(request, status, body), nil
				})
				var pics string
				var err error
				if batch {
					pics, err = client.EventUploadImages(context.Background(), []string{imagePath})
				} else {
					pics, err = client.EventUploadImage(context.Background(), imagePath)
				}
				if pics != "" || err == nil {
					t.Fatalf("图片上传失败后返回了可发布数据: pics=%q err=%v", pics, err)
				}
				if (failure == "missing-token" || failure == "unsafe-key") && uploads != 0 {
					t.Fatal("无效凭据或路径仍然进行了文件上传")
				}
				if failure == "storage-rejected" && metadata != 0 {
					t.Fatal("NOS 拒绝后仍请求发布图片信息")
				}
			})
		}
	}
}

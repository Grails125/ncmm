package eapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/pkg/crypto"
)

func nosTestImage(t *testing.T, format string) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 2, 2))
	picture.Set(0, 0, color.RGBA{R: 180, G: 80, B: 30, A: 255})
	var buffer bytes.Buffer
	var err error
	switch format {
	case "jpg":
		err = jpeg.Encode(&buffer, picture, nil)
	case "png":
		err = png.Encode(&buffer, picture)
	case "gif":
		err = gif.Encode(&buffer, picture, nil)
	default:
		t.Fatalf("未知测试图片格式: %s", format)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func nosTestEAPIPayload(t *testing.T, request *http.Request, target any) {
	t.Helper()
	if err := request.ParseForm(); err != nil {
		t.Fatal(err)
	}
	plain, err := crypto.EApiDecrypt(request.PostForm.Get("params"), "hex")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(plain), "-36cd479b6b5-")
	if len(parts) != 3 {
		t.Fatal("EAPI 请求格式错误")
	}
	if err := json.Unmarshal([]byte(parts[1]), target); err != nil {
		t.Fatal(err)
	}
}

func TestEventImageUploadUsesContentFormatForMIMEAndMetadata(t *testing.T) {
	for _, batch := range []bool{false, true} {
		for _, tc := range []struct {
			format   string
			mime     string
			filename string
		}{
			{"jpg", "image/jpeg", "picture.jpg"},
			{"png", "image/png", "picture.png"},
			{"gif", "image/gif", "picture.gif"},
			{"jpg", "image/jpeg", "picture.png"},
			{"png", "image/png", "picture.jpg"},
			{"gif", "image/gif", "picture"},
		} {
			t.Run(fmt.Sprintf("batch=%t/%s/%s", batch, tc.format, tc.filename), func(t *testing.T) {
				data := nosTestImage(t, tc.format)
				filePath := filepath.Join(t.TempDir(), tc.filename)
				if err := os.WriteFile(filePath, data, 0600); err != nil {
					t.Fatal(err)
				}
				allocCalls, uploads, metadataCalls := 0, 0, 0
				client := newNOSTestAPI(t, func(request *http.Request) (*http.Response, error) {
					switch request.URL.Host + request.URL.Path {
					case "music.163.com/eapi/nos/token/alloc":
						allocCalls++
						var payload struct {
							Ext      string `json:"ext"`
							Type     string `json:"type"`
							FileSize int64  `json:"fileSize"`
						}
						nosTestEAPIPayload(t, request, &payload)
						if payload.Ext != tc.format || payload.Type != "image" || payload.FileSize != int64(len(data)) {
							t.Errorf("上传凭据必须按图片内容申请: ext=%s type=%s size=%d", payload.Ext, payload.Type, payload.FileSize)
						}
						return nosTestResponse(request, 200, `{"code":200,"result":{"bucket":"yyimgs","docId":"123","objectKey":"image","token":"nos-only-token"}}`), nil
					case "wanproxy.127.net/lbs":
						return nosTestResponse(request, 200, `{"upload":["https://nosup-hz1.127.net"]}`), nil
					case "nosup-hz1.127.net/yyimgs/image":
						uploads++
						if request.Header.Get("Content-Type") != tc.mime {
							t.Errorf("NOS 图片 MIME 错误: got=%s want=%s", request.Header.Get("Content-Type"), tc.mime)
						}
						if request.ContentLength != int64(len(data)) {
							t.Errorf("NOS 图片长度错误: got=%d want=%d", request.ContentLength, len(data))
						}
						body, err := io.ReadAll(request.Body)
						if err != nil || !bytes.Equal(body, data) {
							t.Fatal("NOS 上传必须保留图片原始字节")
						}
						return nosTestResponse(request, 200, fmt.Sprintf(`{"offset":%d}`, len(data))), nil
					case "music.163.com/eapi/upload/event/img/v1":
						metadataCalls++
						var payload struct {
							Format string `json:"format"`
						}
						nosTestEAPIPayload(t, request, &payload)
						if payload.Format != tc.format {
							t.Errorf("图片元数据必须使用实际格式: got=%s want=%s", payload.Format, tc.format)
						}
						return nosTestResponse(request, 200, `{"code":200,"picInfo":{"originId":123,"squareId":124,"rectangleId":125,"width":2,"height":2}}`), nil
					default:
						t.Errorf("不应请求该地址: %s", request.URL)
						return nosTestResponse(request, 500, `{}`), nil
					}
				})
				var pics string
				var err error
				if batch {
					pics, err = client.EventUploadImages(context.Background(), []string{filePath})
				} else {
					pics, err = client.EventUploadImage(context.Background(), filePath)
				}
				if err != nil || pics == "" || allocCalls != 1 || uploads != 1 || metadataCalls != 1 {
					t.Fatalf("上传失败: err=%v picsPresent=%t calls=%d/%d/%d", err, pics != "", allocCalls, uploads, metadataCalls)
				}
			})
		}
	}
}

func TestEventImageUploadRejectsNonImagesBeforeAllocatingToken(t *testing.T) {
	for _, batch := range []bool{false, true} {
		for _, body := range [][]byte{nil, []byte("<html>download failed</html>"), []byte("ID3audio-data")} {
			t.Run(fmt.Sprintf("batch=%t/bytes=%d", batch, len(body)), func(t *testing.T) {
				filePath := filepath.Join(t.TempDir(), "pretend.jpg")
				if err := os.WriteFile(filePath, body, 0600); err != nil {
					t.Fatal(err)
				}
				calls := 0
				client := newNOSTestAPI(t, func(request *http.Request) (*http.Response, error) {
					calls++
					return nosTestResponse(request, 400, `{"code":400}`), nil
				})
				var pics string
				var err error
				if batch {
					pics, err = client.EventUploadImages(context.Background(), []string{filePath})
				} else {
					pics, err = client.EventUploadImage(context.Background(), filePath)
				}
				if err == nil || pics != "" || calls != 0 {
					t.Fatalf("非图片内容不能申请上传凭据: err=%v picsPresent=%t calls=%d", err, pics != "", calls)
				}
			})
		}
	}
}

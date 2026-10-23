package ncmm

import (
	"bytes"
	"context"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/config"
	cookiepkg "github.com/3899/ncmm/pkg/cookie"
	"github.com/spf13/cobra"
)

func TestFansGroupImagePreparationFailureNeverPublishesTextFallback(t *testing.T) {
	for _, kind := range []string{"no-config", "no-images", "unreadable", "upload-rejected"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			cfg := &config.Config{}
			if kind != "no-config" {
				cfg.Note = &config.NoteConf{}
			}
			if kind == "unreadable" || kind == "upload-rejected" {
				image := filepath.Join(dir, "image.jpg")
				cfg.Note.ImageURLs = config.StringOrSlice{image}
				if kind == "upload-rejected" {
					if err := os.WriteFile(image, []byte("test image"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			client, err := api.NewClient(&api.Config{Cookie: cookiepkg.Config{Filepath: filepath.Join(dir, "cookie.json")}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close(context.Background())
			publishes := 0
			client.GetClient().Transport = fansTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/eapi/note/share/friends/resource" {
					publishes++
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":401}`)), Request: r}, nil
			})
			var output bytes.Buffer
			c := &FansGroup{root: &Root{Cfg: cfg}, cmd: &cobra.Command{}, rng: rand.New(rand.NewSource(1))}
			c.cmd.SetOut(&output)
			c.doPublishNote(context.Background(), eapi.New(client), "1", "测试乐迷团", eapi.FansGroupMissionItem{Title: "发布图文笔记", AllProgress: 1})
			if publishes != 0 {
				t.Fatalf("图片准备失败后发布了 %d 次无图动态", publishes)
			}
			if !strings.Contains(output.String(), "跳过") {
				t.Fatalf("缺少图文任务跳过诊断: %s", output.String())
			}
		})
	}
}

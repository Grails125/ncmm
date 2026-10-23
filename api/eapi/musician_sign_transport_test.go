package eapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/pkg/log"
)

func TestMusicianSignWritesDoNotRetryOrExposePrivateData(t *testing.T) {
	for _, scenario := range []string{"access-http", "reward-http", "access-network", "reward-network"} {
		t.Run(scenario, func(t *testing.T) {
			directory := t.TempDir()
			logPath := filepath.Join(directory, "musician.log")
			logConfig := &log.Config{Level: "debug"}
			logConfig.Rotate.Filename = logPath
			logger := log.New(logConfig)
			previous := log.Default
			log.Default = logger
			defer func() { log.Default = previous; _ = logger.Close() }()
			log.Info("musician transport regression")
			cfg := &api.Config{Retry: 2, Debug: true}
			cfg.Cookie.Filepath = filepath.Join(directory, "anonymous.json")
			client, err := api.NewClient(cfg, logger)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close(context.Background())
			calls := 0
			client.GetClient().Transport = qrLoginTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				if strings.HasSuffix(scenario, "network") {
					return nil, fmt.Errorf("synthetic-musician-response-private")
				}
				return &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":503,"message":"synthetic-musician-response-private"}`)), Request: request}, nil
			})
			common := MusicianEAPIReq{DeviceId: "synthetic-musician-device-private", Header: map[string]string{"MUSIC_U": "synthetic-musician-cookie-private"}}
			if strings.HasPrefix(scenario, "access") {
				_, err = New(client).MusicianSign(t.Context(), &MusicianSignReq{MusicianEAPIReq: common})
			} else {
				_, err = New(client).MusicianRewardObtain(t.Context(), &MusicianRewardObtainReq{MusicianEAPIReq: common, UserMissionId: 88, Period: 1})
			}
			if err == nil || calls != 1 {
				t.Errorf("签到写请求失败不能自动重发：calls=%d err=%v", calls, err)
			}
			content, readErr := os.ReadFile(logPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, private := range []string{"synthetic-musician-device-private", "synthetic-musician-cookie-private", "synthetic-musician-response-private"} {
				if strings.Contains(string(content), private) || (err != nil && strings.Contains(err.Error(), private)) {
					t.Error("签到请求或响应暴露了私密内容")
				}
			}
		})
	}
}

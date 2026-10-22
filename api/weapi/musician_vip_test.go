package weapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/pkg/cookie"
	"github.com/3899/ncmm/pkg/log"
	"gopkg.in/natefinch/lumberjack.v2"
)

func TestMusicianVipInfoResp_Unmarshal(t *testing.T) {
	raw := `{
		"code": 200,
		"data": {
			"hasOpen": true,
			"isMusician": true,
			"canOpen": true,
			"furtherVipGetTime": 1789315200000,
			"hasFurtherTask": true,
			"taskStatus": true,
			"maintainDays": 824,
			"recentPlayCount30": 2753,
			"furtherTask": {
				"totalCompleteNum": 2,
				"progressRate": 2,
				"missionStatus": 100,
				"children": [
					{
						"name": "近30天有效播放达650次",
						"totalCompleteNum": 650,
						"progressRate": 650,
						"missionStatus": 100,
						"missionCode": "mission_code_recently_play_count"
					},
					{
						"name": "近30天内发布图文笔记4篇",
						"totalCompleteNum": 4,
						"progressRate": 4,
						"missionStatus": 100,
						"missionCode": "mission_code_musician_notebook_publish"
					}
				]
			},
			"unlockVipRight": true
		},
		"message": ""
	}`

	var resp MusicianVipInfoResp
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if resp.Code != 200 {
		t.Errorf("expected code 200, got %d", resp.Code)
	}
	if !resp.Data.CanOpen {
		t.Errorf("expected canOpen = true")
	}
	if !resp.Data.TaskStatus {
		t.Errorf("expected taskStatus = true")
	}
	if resp.Data.FurtherVipGetTime != 1789315200000 {
		t.Errorf("expected furtherVipGetTime 1789315200000, got %d", resp.Data.FurtherVipGetTime)
	}
	if resp.Data.FurtherTask == nil || len(resp.Data.FurtherTask.Children) != 2 {
		t.Fatalf("expected 2 children tasks")
	}
	if resp.Data.FurtherTask.Children[0].MissionCode != "mission_code_recently_play_count" {
		t.Errorf("unexpected missionCode: %s", resp.Data.FurtherTask.Children[0].MissionCode)
	}
}

func TestMusicianVipGetResp_Unmarshal(t *testing.T) {
	raw := `{"code":200,"data":true,"message":""}`
	var resp MusicianVipGetResp
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if resp.Code != 200 {
		t.Errorf("expected code 200, got %d", resp.Code)
	}
	if !resp.Data {
		t.Errorf("expected data = true")
	}
}

func TestMusicianVipRequestsProtectCredentials(t *testing.T) {
	const secret = "private-musician-vip-token"
	logPath := filepath.Join(t.TempDir(), "debug.log")
	logger := log.New(&log.Config{Level: "debug", Rotate: lumberjack.Logger{Filename: logPath}})
	previous := log.Default
	log.Default = logger
	t.Cleanup(func() { log.Default = previous; _ = logger.Close() })
	client, err := api.NewClient(&api.Config{Debug: true, Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	for _, endpoint := range []string{"info", "get"} {
		for _, mode := range []string{"success", "http_error", "network_error", "invalid_response"} {
			t.Run(endpoint+"/"+mode, func(t *testing.T) {
				client.GetClient().Transport = partnerTestTransport(func(request *http.Request) (*http.Response, error) {
					if !strings.HasSuffix(request.URL.Path, "/vip/"+endpoint) {
						t.Fatalf("wrong endpoint: %s", request.URL.Path)
					}
					if endpoint == "get" && request.Header.Get("x-anticheattoken") != secret {
						t.Error("claim token header missing")
					}
					if mode == "network_error" {
						return nil, fmt.Errorf("transport failed with %s", secret)
					}
					status := http.StatusOK
					payload := `{"code":200,"message":"` + secret + `","data":true}`
					if endpoint == "info" {
						payload = `{"code":200,"message":"` + secret + `","data":{"canOpen":true}}`
					}
					if mode == "http_error" {
						status = http.StatusForbidden
					} else if mode == "invalid_response" {
						payload = `<html>` + secret + `</html>`
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(payload)), Request: request}, nil
				})
				var requestError error
				if endpoint == "info" {
					_, requestError = New(client).MusicianVipInfo(context.Background(), &MusicianVipInfoReq{})
				} else {
					_, requestError = New(client).MusicianVipGet(context.Background(), &MusicianVipGetReq{CheckToken: secret}, secret)
				}
				if mode == "success" {
					if requestError != nil {
						t.Fatal(requestError)
					}
				} else if requestError == nil || strings.Contains(requestError.Error(), secret) {
					t.Fatalf("missing or unsafe error: %v", requestError)
				}
			})
		}
	}
	_ = logger.Close()
	logged, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if strings.Contains(string(logged), secret) {
		t.Fatal("VIP request or response leaked credentials to diagnostic log")
	}
}

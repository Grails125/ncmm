package eapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/pkg/cookie"
	"github.com/3899/ncmm/pkg/crypto"
	"github.com/3899/ncmm/pkg/log"
)

func TestYunbeiBrowseSendsDynamicTaskAndOfficialFinishType(t *testing.T) {
	client, err := api.NewClient(&api.Config{Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, log.Default)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	clicks, finishes := 0, 0
	expectedTaskID := int64(4242)
	client.GetClient().Transport = nosTestTransport(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Scheme != "https" || request.URL.Host != "interface3.music.163.com" {
			t.Fatalf("云贝浏览协议错误: %s %s", request.Method, request.URL)
		}
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		plain, err := crypto.EApiDecrypt(request.PostForm.Get("params"), "hex")
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(string(plain), "-36cd479b6b5-")
		if len(parts) != 3 {
			t.Fatal("无效 EAPI 请求")
		}
		switch request.URL.Path {
		case "/eapi/yunbei/click/task":
			clicks++
			var payload YunbeiClickTaskReq
			if err := json.Unmarshal([]byte(parts[1]), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.TaskId != expectedTaskID || payload.Type != "" || payload.SubAction != "" || payload.CheckToken != "" {
				t.Fatalf("必须使用当次任务ID，不能混入无关平台标记: %+v", payload)
			}
		case "/eapi/vipnewcenter/app/level/task/external":
			finishes++
			var payload map[string]json.RawMessage
			if err := json.Unmarshal([]byte(parts[1]), &payload); err != nil {
				t.Fatal(err)
			}
			if len(payload) != 1 || string(payload["type"]) != "1" {
				t.Fatalf("浏览结束必须使用官方 type=1 载荷: %s", parts[1])
			}
		default:
			t.Fatalf("不应访问其他接口: %s", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":200,"data":true}`)), Request: request}, nil
	})
	request := New(client)
	for _, taskID := range []int64{4242, 9988} {
		expectedTaskID = taskID
		if _, err := request.YunbeiClickTask(t.Context(), &YunbeiClickTaskReq{TaskId: taskID}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := request.YunbeiVipBrowseFinish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if clicks != 2 || finishes != 1 {
		t.Fatalf("请求次数错误: clicks=%d finishes=%d", clicks, finishes)
	}
}

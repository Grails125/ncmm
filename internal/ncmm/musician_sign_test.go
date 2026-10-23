package ncmm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/types"
	"github.com/3899/ncmm/config"
	cookiepkg "github.com/3899/ncmm/pkg/cookie"
	"github.com/3899/ncmm/pkg/log"
)

func TestMusicianAccessReceiptDoesNotConfirmDailySign(t *testing.T) {
	var output bytes.Buffer
	command := NewMusician(&Root{Cfg: &config.Config{Musician: &config.MusicianConf{}}}, nil)
	command.Command().SetOut(&output)
	command.Command().SetErr(&output)
	client, err := api.NewClient(&api.Config{Cookie: cookiepkg.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, log.Default)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	var accesses, cycles int
	client.GetClient().Transport = musicianVipClaimTransport(func(request *http.Request) (*http.Response, error) {
		var body any
		switch request.URL.Path {
		case "/eapi/creator/user/access":
			accesses++
			body = eapi.MusicianSignResp{RespCommon: types.RespCommon[bool]{Code: 200, Data: true}}
		case "/eapi/nmusician/workbench/mission/cycle/list":
			cycles++
			var tasks []eapi.MusicianMissionTask
			if cycles%2 == 1 {
				tasks = []eapi.MusicianMissionTask{{UserMissionId: 88, MissionId: 3611300, ActionType: 102, Description: "音乐人中心签到", TargetCount: 1}}
			}
			body = eapi.MusicianMissionListResp{RespCommon: types.RespCommon[eapi.MusicianMissionListData]{Code: 200, Data: eapi.MusicianMissionListData{List: tasks}}}
		case "/eapi/nmusician/workbench/mission/stage/list":
			body = eapi.MusicianMissionListResp{RespCommon: types.RespCommon[eapi.MusicianMissionListData]{Code: 200}}
		default:
			t.Fatalf("签到验证不应发起其他请求：%s", request.URL.Path)
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(encoded)), Request: request}, nil
	})
	command.doSignPhase(t.Context(), &musicianContext{cli: client, eapiCli: eapi.New(client)}, "account.json")
	if accesses != 1 || cycles < 4 {
		t.Fatalf("应发送一次访问打点并复查任务，access=%d cycles=%d", accesses, cycles)
	}
	if strings.Contains(output.String(), "音乐人日常签到成功") {
		t.Fatalf("访问回执为 true 但签到任务仍未完成时不能报签到成功：%s", output.String())
	}
}

package ncmm

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/config"
	cookiepkg "github.com/3899/ncmm/pkg/cookie"
	"github.com/3899/ncmm/pkg/log"
)

func TestVipAppListenTaskDoesNotStartPlayback(t *testing.T) {
	for _, task := range []eapi.VipTaskListData{
		{MissionCode: "MRGSSVIPGQ", MainTitle: "每日歌曲任务"},
		{MainTitle: "每日听3首VIP歌曲"},
		{MainTitle: "每日听3首会员歌曲"},
	} {
		t.Run(task.MainTitle, func(t *testing.T) {
			command := NewSign(nil, nil)
			var output bytes.Buffer
			command.cmd.SetOut(&output)
			// 跳过路径不得访问播放 API，也不需要创建网络客户端。
			command.executeSingleVipTask(t.Context(), nil, nil, nil, task, "", 0, nil)
			if !strings.Contains(output.String(), "App") || !strings.Contains(output.String(), "跳过") {
				t.Fatalf("应提示到 App 内完成任务: %s", output.String())
			}
			if strings.Contains(output.String(), "成功") {
				t.Fatalf("跳过不能报告为完成: %s", output.String())
			}
		})
	}
}

func TestVipAppListenSkipKeepsSignAndRewardFlow(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		alreadySigned bool
		missingSign   bool
	}{
		{name: "仍执行乐签并领取奖励"},
		{name: "仅剩App任务时不重复轮询", alreadySigned: true},
		{name: "列表缺少乐签时保留兜底签到", missingSign: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			command := NewSign(&Root{Cfg: &config.Config{Sign: &config.SignConf{Automatic: true}}}, nil)
			var output bytes.Buffer
			command.cmd.SetOut(&output)
			client, err := api.NewClient(&api.Config{Cookie: cookiepkg.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, log.Default)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })
			listCalls, signCalls, rewardCalls := 0, 0, 0
			client.GetClient().Transport = musicianVipClaimTransport(func(request *http.Request) (*http.Response, error) {
				body := `{"code":200,"data":[]}`
				switch request.URL.Path {
				case "/eapi/vip-center-bff/task/list":
					listCalls++
					body = `{"code":200,"data":[{"missionId":-500,"mainTitle":"黑胶乐签","status":10},{"missionCode":"MRGSSVIPGQ","mainTitle":"每日听3首VIP歌曲","status":10}]}`
					if scenario.missingSign {
						body = `{"code":200,"data":[{"missionCode":"MRGSSVIPGQ","mainTitle":"每日听3首VIP歌曲","status":10}]}`
					}
				case "/weapi/middle/vip/mission/user/progress/list":
				case "/eapi/vipnewcenter/app/level/growhpoint/basic":
					body = `{"code":200,"data":{"userLevel":{"growthPoint":10,"extJson":"{\"todayScore\":3}"}}}`
				case "/eapi/vipnewcenter/app/user/sign/info":
					if scenario.alreadySigned || signCalls > 0 {
						body = `{"code":200,"data":[{"today":true,"recordId":123,"score":3}]}`
					}
				case "/eapi/vip-center-bff/task/sign":
					signCalls++
					body = `{"code":200,"data":true}`
				case "/eapi/vipnewcenter/app/level/task/reward/getall":
					rewardCalls++
					body = `{"code":200,"data":{"result":true}}`
				default:
					t.Fatalf("不应执行额外播放或其他 API: %s", request.URL.Path)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})
			growth := command.handleVipTasks(t.Context(), client, weapi.New(client), 0)
			wantSigns, wantLists := 1, 3
			if scenario.alreadySigned {
				wantSigns, wantLists = 0, 2
			}
			if scenario.missingSign {
				wantLists = 2
			}
			if signCalls != wantSigns || rewardCalls != 1 || listCalls != wantLists {
				t.Fatalf("任务调用异常: sign=%d reward=%d list=%d", signCalls, rewardCalls, listCalls)
			}
			if growth == nil || growth.GrowthPoint != 10 || !strings.Contains(output.String(), "需 App 完成（已跳过）") {
				t.Fatalf("最终成长值与跳过状态应保留: growth=%v output=%s", growth, output.String())
			}
		})
	}
}

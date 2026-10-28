package ncmm

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/config"
	cookiepkg "github.com/3899/ncmm/pkg/cookie"
)

func newYunbeiCapabilityClient(t *testing.T, respond func(*http.Request) string) *api.Client {
	t.Helper()
	client, err := api.NewClient(&api.Config{Cookie: cookiepkg.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	client.GetClient().Transport = musicianVipClaimTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(respond(r))), Request: r}, nil
	})
	return client
}

func TestYunbeiExecutionUsesFullListWhenTodoOmitsTask(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	clicks := 0
	client := newYunbeiCapabilityClient(t, func(r *http.Request) string {
		switch r.URL.Path {
		case "/weapi/usertool/task/list/all":
			return `{"code":200,"data":[{"taskId":4242,"taskName":"浏览会员中心","completed":false}]}`
		case "/eapi/yunbei/click/task":
			clicks++
			cancel()
			return `{"code":200,"data":true}`
		default:
			return `{"code":200,"data":[]}`
		}
	})
	command := NewSign(&Root{Cfg: &config.Config{Sign: &config.SignConf{YunbeiTask: &config.YunbeiTaskConf{EnableViewVipCenter: true}}}}, nil)
	var output bytes.Buffer
	command.cmd.SetOut(&output)
	command.handleYunbeiTasks(ctx, client, weapi.New(client), 1, "cookie.json", map[string]bool{"ViewVipCenter": true})
	if clicks != 1 {
		t.Fatalf("完整列表中的任务没有执行: clicks=%d output=%s", clicks, output.String())
	}
}

func TestYunbeiVipBrowseReportsOnlyAfterSuccessfulWait(t *testing.T) {
	for _, scenario := range []string{"success", "click-rejected", "cancelled", "finish-rejected"} {
		t.Run(scenario, func(t *testing.T) {
			finishes, waits := 0, 0
			client := newYunbeiCapabilityClient(t, func(r *http.Request) string {
				switch r.URL.Path {
				case "/eapi/yunbei/click/task":
					if scenario == "click-rejected" {
						return `{"code":403,"data":false}`
					}
					return `{"code":200,"data":true}`
				case "/eapi/vipnewcenter/app/level/task/external":
					finishes++
					if waits != 1 {
						t.Error("未等待就上报浏览完成")
					}
					if scenario == "finish-rejected" {
						return `{"code":403}`
					}
					return `{"code":200}`
				default:
					t.Errorf("额外请求: %s", r.URL.Path)
					return `{"code":500}`
				}
			})
			err := runYunbeiVipBrowse(t.Context(), eapi.New(client), 4242, func(ctx context.Context) error {
				waits++
				if scenario == "cancelled" {
					return context.Canceled
				}
				return nil
			})
			if (err == nil) != (scenario == "success") {
				t.Fatalf("错误结果不符: %v", err)
			}
			want := 1
			if scenario == "click-rejected" || scenario == "cancelled" {
				want = 0
			}
			if finishes != want {
				t.Fatalf("错误上报次数: got=%d want=%d", finishes, want)
			}
		})
	}
}

func TestYunbeiWaitRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := waitYunbeiAction(ctx, time.Hour); err != context.Canceled {
		t.Fatalf("取消后仍等待: %v", err)
	}
}

func TestYunbeiRewardTasksIncludesFullListAndRejectsInvalidClaims(t *testing.T) {
	todo := []eapi.YunBeiTaskTodoRespData{{Completed: true, UserTaskId: 1, TaskName: "旧任务"}, {Completed: false, UserTaskId: 2}}
	full := []weapi.YunBeiTaskListRespData{{Completed: true, UserTaskId: 1, TaskName: "旧任务"}, {Completed: true, UserTaskId: 3, TaskName: "分享歌曲", TaskId: 42}, {Completed: false, UserTaskId: 4}, {Completed: true, UserTaskId: 0}}
	got := yunbeiRewardTasks(todo, full, map[string]bool{"42/0": true})
	if len(got) != 2 || got[0].UserTaskId != 1 || got[1].UserTaskId != 3 {
		t.Fatalf("领奖列表重复或遗漏: %+v", got)
	}
}

func TestYunbeiClaimsTodoCandidateWithoutReportingRejectedReward(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		t.Run(map[bool]string{true: "accepted", false: "rejected"}[accepted], func(t *testing.T) {
			claims := 0
			client := newYunbeiCapabilityClient(t, func(r *http.Request) string {
				switch r.URL.Path {
				case "/weapi/usertool/task/list/all":
					return `{"code":200,"data":[{"completed":true,"taskId":42,"userTaskId":987,"taskName":"分享歌曲","taskPoint":100}]}`
				case "/eapi/usertool/task/todo/query":
					return `{"code":200,"data":[{"completed":true,"taskId":42,"userTaskId":987,"taskName":"分享歌曲","taskPoint":100}]}`
				case "/weapi/usertool/task/point/receive":
					claims++
					if accepted {
						return `{"code":200,"data":true}`
					}
					return `{"code":200,"data":false}`
				default:
					t.Errorf("不应发送额外请求: %s", r.URL.Path)
					return `{"code":500}`
				}
			})
			command := NewSign(&Root{Cfg: &config.Config{Sign: &config.SignConf{YunbeiTask: &config.YunbeiTaskConf{}}}}, nil)
			var output bytes.Buffer
			command.cmd.SetOut(&output)
			command.handleYunbeiTasks(t.Context(), client, weapi.New(client), 1, "cookie.json", map[string]bool{"ShareSong": true})
			if claims != 1 {
				t.Fatalf("没有待做任务时仍应领取一次已完成奖励: %d", claims)
			}
			if strings.Contains(output.String(), "成功领取云贝") != accepted {
				t.Fatalf("领奖确认不准确: %s", output.String())
			}
			if !accepted && !strings.Contains(output.String(), "领奖来源=待办接口，depositCode=无，完整列表status=0") {
				t.Fatalf("业务 false 应展示领奖证据，不能误报到账: %s", output.String())
			}
		})
	}
}

func TestYunbeiShareRejectsMissingSongAndRejectedTrigger(t *testing.T) {
	for _, scenario := range []string{"no-song", "rejected-trigger"} {
		t.Run(scenario, func(t *testing.T) {
			triggers, unrelated := 0, 0
			client := newYunbeiCapabilityClient(t, func(r *http.Request) string {
				switch r.URL.Path {
				case "/eapi/yunbei/click/task":
					return `{"code":200,"data":true}`
				case "/weapi/v1/play/record":
					if scenario == "rejected-trigger" {
						return `{"code":200,"weekData":[{"song":{"id":42,"name":"测试歌曲"}}]}`
					}
					return `{"code":200,"weekData":[],"allData":[]}`
				case "/weapi/user/playlist/":
					return `{"code":200,"playlist":[]}`
				case "/eapi/v3/discovery/recommend/songs":
					return `{"code":200,"data":{"dailySongs":[]}}`
				case "/eapi/ad/power/yunbei/distribution/recommend/song":
					return `{"code":200,"data":[]}`
				case "/eapi/music/song/share/trigger":
					triggers++
					return `{"code":200,"data":false}`
				default:
					unrelated++
					return `{"code":200,"data":true}`
				}
			})
			command := NewSign(nil, nil)
			var output bytes.Buffer
			command.cmd.SetOut(&output)
			command.doShareSong(t.Context(), eapi.New(client), weapi.New(client), 1, 42, 0)
			if unrelated != 0 {
				t.Fatalf("云贝分享调用了无关接口: %d", unrelated)
			}
			if scenario == "no-song" && triggers != 0 {
				t.Fatal("没有歌曲仍触发分享")
			}
			if strings.Contains(output.String(), "操作完成") || strings.Contains(output.String(), "成功触发") {
				t.Fatalf("失败被报告为成功: %s", output.String())
			}
		})
	}
}

func TestYunbeiRewardClaimsAttemptOnceAcrossRounds(t *testing.T) {
	for _, tc := range []struct {
		name        string
		rejects     int
		wantClaims  int
		multiPeriod bool
	}{
		{name: "successful-first-round", wantClaims: 1},
		{name: "do-not-repeat-unconfirmed", rejects: 1, wantClaims: 1},
		{name: "rejected-once-only", rejects: 3, wantClaims: 1},
		{name: "same-user-task-different-period", wantClaims: 2, multiPeriod: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := 0
			client := newYunbeiCapabilityClient(t, func(r *http.Request) string {
				switch r.URL.Path {
				case "/weapi/usertool/task/list/all":
					// 缺少编号的待做任务保持未完成，保证执行三轮；已完成任务领取后仍保留在完整列表。
					if tc.multiPeriod {
						return `{"code":200,"data":[{"completed":false,"taskName":"分享歌曲"},{"completed":true,"taskId":42,"userTaskId":987,"period":1,"taskName":"浏览会员中心","taskPoint":100},{"completed":true,"taskId":42,"userTaskId":987,"period":2,"taskName":"浏览会员中心","taskPoint":100}]}`
					}
					return `{"code":200,"data":[{"completed":false,"taskName":"分享歌曲"},{"completed":true,"taskId":42,"userTaskId":987,"period":1,"taskName":"浏览会员中心","taskPoint":100}]}`
				case "/eapi/usertool/task/todo/query":
					if tc.multiPeriod {
						return `{"code":200,"data":[{"completed":true,"taskId":42,"userTaskId":987,"period":1,"taskName":"浏览会员中心","taskPoint":100},{"completed":true,"taskId":42,"userTaskId":987,"period":2,"taskName":"浏览会员中心","taskPoint":100}]}`
					}
					return `{"code":200,"data":[{"completed":true,"taskId":42,"userTaskId":987,"period":1,"taskName":"浏览会员中心","taskPoint":100}]}`
				case "/weapi/usertool/task/point/receive":
					claims++
					if claims <= tc.rejects {
						return `{"code":200,"data":false}`
					}
					return `{"code":200,"data":true}`
				default:
					t.Errorf("不应发送额外请求: %s", r.URL.Path)
					return `{"code":500}`
				}
			})
			command := NewSign(&Root{Cfg: &config.Config{Sign: &config.SignConf{YunbeiTask: &config.YunbeiTaskConf{EnableShareSong: true}}}}, nil)
			var output bytes.Buffer
			command.cmd.SetOut(&output)
			command.handleYunbeiTasks(t.Context(), client, weapi.New(client), 1, "cookie.json", map[string]bool{"ShareSong": true})
			if claims != tc.wantClaims {
				t.Fatalf("无论响应结果，同一凭据均应跨轮去重: got=%d want=%d", claims, tc.wantClaims)
			}
			wantSuccess := max(0, tc.wantClaims-tc.rejects)
			if got := strings.Count(output.String(), "成功领取云贝"); got != wantSuccess {
				t.Fatalf("领取成功次数不符: got=%d want=%d output=%s", got, wantSuccess, output.String())
			}
		})
	}
}

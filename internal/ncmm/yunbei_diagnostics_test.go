package ncmm

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/config"
	cookiepkg "github.com/3899/ncmm/pkg/cookie"
	"github.com/3899/ncmm/pkg/notify"
)

func TestYunbeiDoesNotClaimHistoricalCompletedFullTask(t *testing.T) {
	got := yunbeiRewardTasks(nil, []weapi.YunBeiTaskListRespData{{Completed: true, TaskId: 42, UserTaskId: 987, Period: 1}})
	if len(got) != 0 {
		t.Fatalf("仅 completed 不能证明历史任务待领奖: %+v", got)
	}
}

func TestYunbeiRewardAttemptSharedAcrossTaskGroups(t *testing.T) {
	for _, response := range []string{`{"code":200,"data":true}`, `{"code":200,"data":false}`, `{"code":403,"data":false}`} {
		t.Run(response, func(t *testing.T) {
			claims := 0
			client := newYunbeiCapabilityClient(t, func(r *http.Request) string {
				switch r.URL.Path {
				case "/weapi/usertool/task/list/all":
					return `{"code":200,"data":[]}`
				case "/weapi/usertool/task/list/all/v3":
					return `{"code":200,"data":{}}`
				case "/eapi/usertool/task/todo/query":
					return `{"code":200,"data":[{"completed":true,"taskId":42,"userTaskId":987,"period":1,"taskName":"分享歌曲","taskPoint":100}]}`
				case "/weapi/usertool/task/point/receive":
					claims++
					return response
				default:
					t.Errorf("意外请求: %s", r.URL.Path)
					return `{"code":500}`
				}
			})
			root := &Root{Cfg: &config.Config{Sign: &config.SignConf{YunbeiTask: &config.YunbeiTaskConf{}}}, Report: notify.NewReport("test")}
			var output bytes.Buffer
			for range 2 {
				command := NewSign(root, nil)
				command.cmd.SetOut(&output)
				command.handleYunbeiTasks(t.Context(), client, weapi.New(client), 1, "account-a", map[string]bool{"ShareSong": true})
			}
			if claims != 1 {
				t.Fatalf("同一凭据跨快慢组重复领取: %d\n%s", claims, output.String())
			}
			if strings.Contains(output.String(), "没有可领取的任务奖励") {
				t.Fatalf("零新增成功不能等同没有奖励: %s", output.String())
			}
			failures, _, unconfirmed := root.Report.OutcomeCounts()
			switch response {
			case `{"code":200,"data":true}`:
				if failures != 0 || unconfirmed != 0 {
					t.Fatalf("成功被误报异常: %+v", root.Report.Items())
				}
			case `{"code":200,"data":false}`:
				if failures != 0 || unconfirmed != 1 {
					t.Fatalf("业务 false 未被单独分类: %+v", root.Report.Items())
				}
			default:
				if failures != 1 || unconfirmed != 0 {
					t.Fatalf("服务拒绝未被单独分类: %+v", root.Report.Items())
				}
			}
			otherAccount := NewSign(root, nil)
			otherAccount.cmd.SetOut(&output)
			otherAccount.handleYunbeiTasks(t.Context(), client, weapi.New(client), 2, "account-b", map[string]bool{"ShareSong": true})
			if claims != 2 {
				t.Fatalf("不能跨账号复用领奖状态: %d", claims)
			}
		})
	}
}

func TestYunbeiFullListFallbackRequiresSameTaskPeriodTransition(t *testing.T) {
	full := []weapi.YunBeiTaskListRespData{{Completed: true, TaskId: 42, UserTaskId: 987, Period: 2}}
	if got := yunbeiRewardTasks(nil, full, map[string]bool{"42/1": true}); len(got) != 0 {
		t.Fatalf("不应把旧周期进度当成本周期完成变化: %+v", got)
	}
	if got := yunbeiRewardTasks(nil, full, map[string]bool{"42/2": true}); len(got) != 1 {
		t.Fatalf("遗漏完整列表本轮完成的奖励: %+v", got)
	}
}

func TestYunbeiFullListCompletionTransitionIsClaimed(t *testing.T) {
	queries, claims := 0, 0
	client := newYunbeiCapabilityClient(t, func(r *http.Request) string {
		switch r.URL.Path {
		case "/weapi/usertool/task/list/all":
			queries++
			return fmt.Sprintf(`{"code":200,"data":[{"taskId":42,"userTaskId":987,"period":1,"taskName":"分享歌曲","completed":%t}]}`, queries > 1)
		case "/eapi/usertool/task/todo/query":
			return `{"code":200,"data":[]}`
		case "/weapi/usertool/task/point/receive":
			claims++
			return `{"code":200,"data":true}`
		default:
			t.Errorf("意外请求: %s", r.URL.Path)
			return `{"code":500}`
		}
	})
	command := NewSign(&Root{Cfg: &config.Config{Sign: &config.SignConf{YunbeiTask: &config.YunbeiTaskConf{}}}}, nil)
	var output bytes.Buffer
	command.cmd.SetOut(&output)
	command.handleYunbeiTasks(t.Context(), client, weapi.New(client), 1, "account", map[string]bool{"ShareSong": true})
	if claims != 1 {
		t.Fatalf("完整列表新完成奖励未领取: %d\n%s", claims, output.String())
	}
}

func TestYunbeiDailyRecommendationFilterReasons(t *testing.T) {
	for _, tc := range []struct {
		name, tasks, want string
		enabled, queued   bool
	}{
		{"queue", `[{"taskId":42,"taskName":"听音乐30分钟"}]`, "不在当前任务组队列（PlayDailyRecommend）", true, false},
		{"disabled", `[{"taskId":42,"taskName":"听音乐30分钟"}]`, "子任务开关关闭（PlayDailyRecommend）", false, true},
		{"completed", `[{"taskId":42,"taskName":"听音乐30分钟","completed":true}]`, "服务端标记已完成", true, true},
		{"missing", `[]`, "未返回可识别的日推播放任务", true, true},
		{"unknown-name", `[{"taskId":42,"taskName":"新的播放任务"}]`, "当前没有自动执行实现", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newYunbeiCapabilityClient(t, func(r *http.Request) string {
				switch r.URL.Path {
				case "/weapi/usertool/task/list/all":
					return `{"code":200,"data":` + tc.tasks + `}`
				case "/weapi/usertool/task/list/all/v3":
					return `{"code":200,"data":{}}`
				case "/eapi/usertool/task/todo/query":
					return `{"code":200,"data":[]}`
				default:
					t.Errorf("筛选掉的任务不应调用执行接口: %s", r.URL.Path)
					return `{"code":500}`
				}
			})
			command := NewSign(&Root{Cfg: &config.Config{Sign: &config.SignConf{YunbeiTask: &config.YunbeiTaskConf{EnablePlayDailyRecommend: tc.enabled}}}}, nil)
			var output bytes.Buffer
			command.cmd.SetOut(&output)
			command.handleYunbeiTasks(t.Context(), client, weapi.New(client), 1, "account", map[string]bool{"PlayDailyRecommend": tc.queued})
			if !strings.Contains(output.String(), tc.want) || !strings.Contains(output.String(), "[云贝调度]") {
				t.Fatalf("遗漏筛选原因或配置快照: %s", output.String())
			}
		})
	}
}

func TestYunbeiRewardRequestDoesNotRetryOrExposeSensitiveError(t *testing.T) {
	client, err := api.NewClient(&api.Config{Retry: 2, Cookie: cookiepkg.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	requests := 0
	client.GetClient().Transport = musicianVipClaimTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		return nil, &url.Error{Op: "POST", URL: "https://offline.invalid/?csrf_token=PRIVATE_TEST_VALUE", Err: context.DeadlineExceeded}
	})
	root := &Root{Report: notify.NewReport("test")}
	command := NewSign(root, nil)
	var output bytes.Buffer
	command.cmd.SetOut(&output)
	state := command.yunbeiRewardState("account")
	candidate := []eapi.YunBeiTaskTodoRespData{{Completed: true, UserTaskId: 987, Period: 1, TaskName: "任务"}}
	for range 2 {
		command.claimYunbeiRewards(t.Context(), weapi.New(client), "account", state, candidate, nil)
	}
	if requests != 1 {
		t.Fatalf("响应丢失时重复提交领奖: %d", requests)
	}
	if strings.Contains(output.String(), "PRIVATE_TEST_VALUE") || strings.Contains(output.String(), "offline.invalid") {
		t.Fatalf("诊断泄漏请求凭据: %s", output.String())
	}
	_, _, unconfirmed := root.Report.OutcomeCounts()
	if unconfirmed != 1 {
		t.Fatalf("响应丢失不能记作成功或反复上报: %+v", root.Report.Items())
	}
}

func TestYunbeiFinalConfirmationMatchesTaskPeriod(t *testing.T) {
	for _, finalPeriod := range []int{1, 2} {
		t.Run(fmt.Sprint(finalPeriod), func(t *testing.T) {
			queries := 0
			client := newYunbeiCapabilityClient(t, func(r *http.Request) string {
				switch r.URL.Path {
				case "/weapi/usertool/task/list/all":
					queries++
					period := 1
					if queries == 7 {
						period = finalPeriod
					}
					return fmt.Sprintf(`{"code":200,"data":[{"taskId":42,"userTaskId":987,"period":%d,"taskName":"分享歌曲","completed":%t}]}`, period, queries == 7)
				case "/eapi/usertool/task/todo/query":
					return `{"code":200,"data":[]}`
				case "/eapi/yunbei/click/task":
					return `{"code":200,"data":false}`
				default:
					t.Errorf("意外请求: %s", r.URL.Path)
					return `{"code":500}`
				}
			})
			root := &Root{Cfg: &config.Config{Sign: &config.SignConf{YunbeiTask: &config.YunbeiTaskConf{EnableShareSong: true}}}, Report: notify.NewReport("test")}
			command := NewSign(root, nil)
			var output bytes.Buffer
			command.cmd.SetOut(&output)
			command.handleYunbeiTasks(t.Context(), client, weapi.New(client), 1, "account", map[string]bool{"ShareSong": true})
			_, _, unconfirmed := root.Report.OutcomeCounts()
			if unconfirmed != finalPeriod-1 {
				t.Fatalf("不能用另一个周期的同名任务确认本次执行: %+v", root.Report.Items())
			}
		})
	}
}

func TestYunbeiTaskNamesTrimWhitespace(t *testing.T) {
	command := NewSign(&Root{Cfg: &config.Config{Sign: &config.SignConf{YunbeiTask: &config.YunbeiTaskConf{EnablePlayDailyRecommend: true, EnableShareSong: true}}}}, nil)
	allowed := map[string]bool{"PlayDailyRecommend": true, "ShareSong": true}
	for _, name := range []string{" 听音乐30分钟 ", "\t分享歌曲\n"} {
		if !command.isYunbeiTaskEnabled(name, allowed) {
			t.Fatalf("前后空白导致任务被过滤: %q", name)
		}
	}
	if !command.isPlayDailyRecommendTask(" 听音乐30分钟 ") {
		t.Fatal("日推分支没有识别带空白名称")
	}
}

func TestYunbeiRewardSourcePrefersTodoCredential(t *testing.T) {
	todo := []eapi.YunBeiTaskTodoRespData{{Completed: true, UserTaskId: 987, Period: 1, DepositCode: 123}}
	full := []weapi.YunBeiTaskListRespData{{Completed: true, TaskId: 42, UserTaskId: 987, Period: 1}}
	got := yunbeiRewardTasks(todo, full)
	if len(got) != 1 || got[0].DepositCode != 123 {
		t.Fatalf("应保留 todo 的领奖凭据: %+v", got)
	}
}

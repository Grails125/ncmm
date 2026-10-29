package ncmm

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/pkg/notify"
)

func yunbeiParentFixture(t *testing.T) []weapi.YunBeiTaskListRespData {
	t.Helper()
	var tasks []weapi.YunBeiTaskListRespData
	err := json.Unmarshal([]byte(`[{"taskId":42,"userTaskId":1010,"period":1,"completed":true,"status":100,"taskName":"发布图文笔记","taskPoint":300,"taskPointDetail":[{"userMissionId":1011,"status":100,"worth":300}]}]`), &tasks)
	if err != nil {
		t.Fatal(err)
	}
	return tasks
}

func TestYunbeiClaimsChildCredentialOnceWithoutSubmittingParent(t *testing.T) {
	full := yunbeiParentFixture(t)
	todo := []eapi.YunBeiTaskTodoRespData{{Completed: true, UserTaskId: 1011, Period: 1, DepositCode: 123, TaskName: "发笔记", TaskPoint: 300}}
	got := yunbeiRewardTasks(todo, full, map[string]bool{"42/1": true})
	if len(got) != 1 || got[0].UserTaskId != 1011 || got[0].TaskId != 42 || got[0].DepositCode != 123 {
		t.Fatalf("必须按奖励子任务去重并保留真实凭据，不能补发父任务：%+v", got)
	}
	claims := 0
	client := newYunbeiCapabilityClient(t, func(r *http.Request) string {
		if r.URL.Path != "/weapi/usertool/task/point/receive" {
			t.Fatalf("意外接口：%s", r.URL.Path)
		}
		claims++
		return `{"code":200,"data":true}`
	})
	root := &Root{Cfg: &config.Config{}, Report: notify.NewReport("test")}
	c := NewSign(root, nil)
	var output bytes.Buffer
	c.cmd.SetOut(&output)
	state := c.yunbeiRewardState("account")
	state.incomplete["42/1"] = true
	for range 2 {
		c.claimYunbeiRewards(t.Context(), weapi.New(client), "account", state, todo, full)
	}
	failures, _, unknown := root.Report.OutcomeCounts()
	if claims != 1 || failures != 0 || unknown != 0 || strings.Contains(output.String(), "云贝历史任务") {
		t.Fatalf("同一奖励跨接口和快慢组不得重复领取或误报未确认：claims=%d 日志=%s report=%+v", claims, output.String(), root.Report.Items())
	}
}

func TestYunbeiDoesNotInventParentCredentialWhenChildListExists(t *testing.T) {
	full := yunbeiParentFixture(t)
	if got := yunbeiRewardTasks(nil, full, map[string]bool{"42/1": true}); len(got) != 0 {
		t.Fatalf("没有待办领奖凭据时不能把父任务 ID 当成奖励 ID：%+v", got)
	}
}

func TestYunbeiDistinctRewardStagesRemainIndependent(t *testing.T) {
	full := yunbeiParentFixture(t)
	full[0].TaskPointDetail = append(full[0].TaskPointDetail, full[0].TaskPointDetail[0])
	full[0].TaskPointDetail[1].UserMissionId = 1012
	todo := []eapi.YunBeiTaskTodoRespData{{Completed: true, UserTaskId: 1011, Period: 1}, {Completed: true, UserTaskId: 1012, Period: 1}, {Completed: true, UserTaskId: 1011, Period: 2}}
	got := yunbeiRewardTasks(todo, full, map[string]bool{"42/1": true})
	if len(got) != 3 || got[0].TaskId != 42 || got[1].TaskId != 42 || got[2].TaskId != 0 {
		t.Fatalf("同一任务的不同奖励或周期不能合并，不能跨周期补齐身份：%+v", got)
	}
}

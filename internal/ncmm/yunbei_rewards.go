package ncmm

import (
	"fmt"

	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/weapi"
)

// 每次批量运行按账号保留领奖证据；快、慢任务组使用同一份状态。
type yunbeiRewardRunState struct {
	accounts map[string]*yunbeiRewardAccountState
}

type yunbeiRewardAccountState struct {
	attempted  map[string]string
	incomplete map[string]bool
	diagnosed  map[string]bool
}

func (c *SignIn) yunbeiRewardState(account string) *yunbeiRewardAccountState {
	if c.root.yunbeiRewards == nil {
		c.root.yunbeiRewards = &yunbeiRewardRunState{accounts: make(map[string]*yunbeiRewardAccountState)}
	}
	state := c.root.yunbeiRewards.accounts[account]
	if state == nil {
		state = &yunbeiRewardAccountState{attempted: make(map[string]string), incomplete: make(map[string]bool), diagnosed: make(map[string]bool)}
		c.root.yunbeiRewards.accounts[account] = state
	}
	return state
}

func yunbeiTaskPeriodKey(taskID, period int64) string {
	return fmt.Sprintf("%d/%d", taskID, period)
}

// 完成变化可作为尝试领奖的证据，但不能证明奖励尚未领取；只记录来源和非敏感状态。
func yunbeiRewardEvidence(task eapi.YunBeiTaskTodoRespData, todo []eapi.YunBeiTaskTodoRespData, full []weapi.YunBeiTaskListRespData) string {
	source := "本轮完成变化"
	for _, item := range todo {
		if item.UserTaskId == task.UserTaskId && item.Period == task.Period && item.Completed {
			source = "待办接口"
			break
		}
	}
	status := "未知"
	for _, item := range full {
		if item.UserTaskId == task.UserTaskId && item.Period == task.Period && item.TaskId == task.TaskId {
			status = fmt.Sprint(item.Status)
			break
		}
	}
	deposit := "无"
	if task.DepositCode != 0 {
		deposit = "有"
	}
	return fmt.Sprintf("领奖来源=%s，depositCode=%s，完整列表status=%s", source, deposit, status)
}

// 待办 userTaskId 对应 taskPointDetail.userMissionId；完整列表的父任务 ID 不能用于领取子奖励。
func yunbeiRewardParent(task eapi.YunBeiTaskTodoRespData, full []weapi.YunBeiTaskListRespData) *weapi.YunBeiTaskListRespData {
	var parent *weapi.YunBeiTaskListRespData
	for i := range full {
		item := &full[i]
		if item.Period != task.Period || task.UserTaskId <= 0 || item.TaskId <= 0 || task.TaskId > 0 && task.TaskId != item.TaskId {
			continue
		}
		for _, reward := range item.TaskPointDetail {
			if reward.UserMissionId != task.UserTaskId {
				continue
			}
			if parent != nil && parent.TaskId != item.TaskId {
				return nil
			}
			parent = item
		}
	}
	return parent
}

// completed 仅证明任务已完成。只有没有奖励子项的旧版平面任务，才用本轮完成变化补齐候选。
func yunbeiRewardTasks(todo []eapi.YunBeiTaskTodoRespData, full []weapi.YunBeiTaskListRespData, observedIncomplete ...map[string]bool) []eapi.YunBeiTaskTodoRespData {
	var tasks []eapi.YunBeiTaskTodoRespData
	seen := make(map[string]bool)
	appendTask := func(task eapi.YunBeiTaskTodoRespData) {
		if !task.Completed || task.UserTaskId <= 0 {
			return
		}
		key := fmt.Sprintf("%d/%d", task.UserTaskId, task.Period)
		if seen[key] {
			return
		}
		seen[key] = true
		tasks = append(tasks, task)
	}
	for _, task := range todo {
		if parent := yunbeiRewardParent(task, full); parent != nil {
			task.TaskId = parent.TaskId
			if parent.TaskName != "" {
				task.TaskName = parent.TaskName
			}
		}
		appendTask(task)
	}
	for _, task := range full {
		if len(task.TaskPointDetail) > 0 || task.TaskId <= 0 || len(observedIncomplete) == 0 || !observedIncomplete[0][yunbeiTaskPeriodKey(task.TaskId, task.Period)] {
			continue
		}
		appendTask(eapi.YunBeiTaskTodoRespData{Completed: task.Completed, UserTaskId: task.UserTaskId, Period: task.Period, TaskId: task.TaskId, TaskName: task.TaskName, TaskPoint: task.TaskPoint})
	}
	return tasks
}

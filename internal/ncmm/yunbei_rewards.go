package ncmm

import (
	"fmt"

	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/weapi"
)

// 完整列表补齐旧待办接口遗漏的已完成任务；同一领取凭据只提交一次。
func yunbeiRewardTasks(todo []eapi.YunBeiTaskTodoRespData, full []weapi.YunBeiTaskListRespData) []eapi.YunBeiTaskTodoRespData {
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
		appendTask(task)
	}
	for _, task := range full {
		appendTask(eapi.YunBeiTaskTodoRespData{Completed: task.Completed, UserTaskId: task.UserTaskId, Period: task.Period, TaskId: task.TaskId, TaskName: task.TaskName, TaskPoint: task.TaskPoint})
	}
	return tasks
}

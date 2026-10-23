package ncmm

import (
	"context"
	"fmt"
	"time"

	"github.com/3899/ncmm/api/eapi"
)

func waitYunbeiAction(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func runYunbeiVipBrowse(ctx context.Context, request *eapi.Api, taskID int64, wait func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if taskID <= 0 {
		return fmt.Errorf("会员中心任务缺少有效的任务编号")
	}
	clicked, err := request.YunbeiClickTask(ctx, &eapi.YunbeiClickTaskReq{TaskId: taskID})
	if err != nil {
		return fmt.Errorf("触发浏览任务失败: %w", err)
	}
	if clicked == nil || clicked.Code != 200 || !clicked.Data {
		return fmt.Errorf("网易云未接受浏览任务")
	}
	if err := wait(ctx); err != nil {
		return err
	}
	finished, err := request.YunbeiVipBrowseFinish(ctx)
	if err != nil {
		return fmt.Errorf("提交浏览时长失败: %w", err)
	}
	if finished == nil || finished.Code != 200 {
		return fmt.Errorf("网易云未接受浏览完成请求")
	}
	return nil
}

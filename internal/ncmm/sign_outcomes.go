package ncmm

import "fmt"

func (c *SignIn) finishSignRun() error {
	if c.root == nil || c.root.Report == nil {
		return nil
	}
	failures, skips, unconfirmed := c.root.Report.OutcomeCounts()
	if failures > 0 || unconfirmed > 0 {
		return fmt.Errorf("日常签到结束：%d 项失败，%d 项跳过，%d 项未确认，请查看子任务日志", failures, skips, unconfirmed)
	}
	return nil
}

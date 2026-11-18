package webui

import "testing"

func TestYunbeiActivitySwitchesAreVisibleAndDisabledByDefault(t *testing.T) {
	fields := configurationSchema().Targets["config"].Fields
	for _, path := range []string{"sign.yunbeiTask.enableRedPacket", "sign.yunbeiTask.enableLottery", "sign.yunbeiTask.enableLotteryWatchVideo", "sign.yunbeiTask.enableWatchVideoYunbei"} {
		found := false
		for _, field := range fields {
			if field.Path == path {
				found = true
				if field.Group != "yunbei" || field.Type != "boolean" || field.Default != false || field.Description == "" {
					t.Fatalf("活动开关字段不完整或默认启用：%+v", field)
				}
			}
		}
		if !found {
			t.Fatalf("配置表单缺少活动字段：%s", path)
		}
	}
}

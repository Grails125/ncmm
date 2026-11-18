package ncmm

import "testing"

func TestSignOutcomesPreserveSkipsAndRealFailures(t *testing.T) {
	for _, outcome := range []string{"none", "skip", "failure", "unconfirmed"} {
		t.Run(outcome, func(t *testing.T) {
			c, _ := activityTestSign()
			c.reportYunbeiActivity("account-a", "活动", outcome, "测试结果")
			err := c.finishSignRun()
			wantError := outcome == "failure" || outcome == "unconfirmed"
			if (err != nil) != wantError {
				t.Fatalf("outcome=%s err=%v", outcome, err)
			}
		})
	}
}

package ncmm

import (
	"context"
	"testing"

	"github.com/3899/ncmm/api/weapi"
)

func TestMusicPartnerZeroExtraTargetSkipsExtraRequests(t *testing.T) {
	command, cfg := partnerTestCommand(t)
	cfg.ExtraLimitMin, cfg.ExtraLimitMax = 0, 0
	fake := &partnerFake{extra: func(context.Context) (*weapi.PartnerExtraTaskResp, error) {
		t.Fatal("每日额外目标为0时不得请求额外任务")
		return nil, nil
	}}
	if err := command.extra(context.Background(), fake, cfg, 10, "account.json"); err != nil {
		t.Fatal(err)
	}
	if len(fake.evaluations) != 0 || len(fake.reports) != 0 {
		t.Fatal("每日额外目标为0时仍有额外评分写请求")
	}
}

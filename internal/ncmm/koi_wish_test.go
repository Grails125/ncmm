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
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/weapi"
	cookiepkg "github.com/3899/ncmm/pkg/cookie"
	"github.com/3899/ncmm/pkg/notify"
)

type koiWishFake struct {
	states        []*weapi.VipLotteryModuleListResp
	readErrors    []error
	response      *eapi.VipLotteryFreeClickResp
	writeError    error
	reads, writes int
	onRead        func(int)
	onWrite       func()
	request       *eapi.VipLotteryFreeClickReq
}

func (f *koiWishFake) VipLotteryModuleList(ctx context.Context) (*weapi.VipLotteryModuleListResp, error) {
	i := f.reads
	f.reads++
	if f.onRead != nil {
		f.onRead(f.reads)
	}
	var err error
	if i < len(f.readErrors) {
		err = f.readErrors[i]
	}
	if i >= len(f.states) {
		return nil, err
	}
	return f.states[i], err
}

func (f *koiWishFake) VipLotteryFreeClick(ctx context.Context, request *eapi.VipLotteryFreeClickReq) (*eapi.VipLotteryFreeClickResp, error) {
	f.writes++
	f.request = request
	if f.onWrite != nil {
		f.onWrite()
	}
	return f.response, f.writeError
}

func koiWishTestPointer[T any](value T) *T { return &value }

func koiWishTestState() *weapi.VipLotteryModuleListResp {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, neteaseLocation).UnixMilli()
	return &weapi.VipLotteryModuleListResp{Code: 200, Data: &weapi.VipLotteryModule{
		VIP: koiWishTestPointer(true), ThisDayCanUseFreeCount: koiWishTestPointer(int64(1)), UseEveryDayFreeCount: koiWishTestPointer(false),
		ActivityBaseInfo:    &weapi.VipLotteryActivity{PrizeName: "测试活动", ServiceTimestamp: now, StartTime: now - 60_000, EndTime: now + 60_000, AwardTimestamp: now + 120_000},
		ActivityProcessInfo: &weapi.VipLotteryProcess{PrizeTableID: 123, Status: koiWishTestPointer(0), VisitorParticipateNum: koiWishTestPointer(int64(0))},
	}}
}

func koiWishTestCompletedState() *weapi.VipLotteryModuleListResp {
	state := koiWishTestState()
	state.Data.ThisDayCanUseFreeCount = koiWishTestPointer(int64(0))
	state.Data.UseEveryDayFreeCount = koiWishTestPointer(true)
	state.Data.ActivityProcessInfo.VisitorParticipateNum = koiWishTestPointer(int64(1))
	state.Data.ActivityBaseInfo.ServiceTimestamp++
	return state
}

func koiWishTestReceipt() *eapi.VipLotteryFreeClickResp {
	return &eapi.VipLotteryFreeClickResp{Code: 200, Data: &eapi.VipLotteryFreeClickData{Success: koiWishTestPointer(true), Privilege: koiWishTestPointer(true), Count: koiWishTestPointer(int64(1))}}
}

func newKoiWishTestCommand() (*SignIn, *bytes.Buffer) {
	command := NewSign(&Root{Report: notify.NewReport("sign")}, nil)
	var output bytes.Buffer
	command.cmd.SetOut(&output)
	return command, &output
}

func TestKoiWishPreflightNeverWritesInvalidOrFinishedState(t *testing.T) {
	for _, test := range []struct {
		name    string
		change  func(*weapi.VipLotteryModuleListResp)
		failure bool
	}{
		{"已使用", func(s *weapi.VipLotteryModuleListResp) { s.Data.UseEveryDayFreeCount = koiWishTestPointer(true) }, false},
		{"次数为零", func(s *weapi.VipLotteryModuleListResp) { s.Data.ThisDayCanUseFreeCount = koiWishTestPointer(int64(0)) }, false},
		{"非VIP", func(s *weapi.VipLotteryModuleListResp) { s.Data.VIP = koiWishTestPointer(false) }, false},
		{"缺数据", func(s *weapi.VipLotteryModuleListResp) { s.Data = nil }, true},
		{"缺VIP", func(s *weapi.VipLotteryModuleListResp) { s.Data.VIP = nil }, true},
		{"缺次数", func(s *weapi.VipLotteryModuleListResp) { s.Data.ThisDayCanUseFreeCount = nil }, true},
		{"缺已用状态", func(s *weapi.VipLotteryModuleListResp) { s.Data.UseEveryDayFreeCount = nil }, true},
		{"缺活动", func(s *weapi.VipLotteryModuleListResp) { s.Data.ActivityBaseInfo = nil }, true},
		{"缺进度", func(s *weapi.VipLotteryModuleListResp) { s.Data.ActivityProcessInfo = nil }, true},
		{"缺状态", func(s *weapi.VipLotteryModuleListResp) { s.Data.ActivityProcessInfo.Status = nil }, true},
		{"缺参与次数", func(s *weapi.VipLotteryModuleListResp) { s.Data.ActivityProcessInfo.VisitorParticipateNum = nil }, true},
		{"负次数", func(s *weapi.VipLotteryModuleListResp) { s.Data.ThisDayCanUseFreeCount = koiWishTestPointer(int64(-1)) }, true},
		{"负参与次数", func(s *weapi.VipLotteryModuleListResp) {
			s.Data.ActivityProcessInfo.VisitorParticipateNum = koiWishTestPointer(int64(-1))
		}, true},
		{"无活动ID", func(s *weapi.VipLotteryModuleListResp) { s.Data.ActivityProcessInfo.PrizeTableID = 0 }, true},
		{"无服务端时间", func(s *weapi.VipLotteryModuleListResp) { s.Data.ActivityBaseInfo.ServiceTimestamp = 0 }, true},
		{"活动结束", func(s *weapi.VipLotteryModuleListResp) {
			s.Data.ActivityBaseInfo.EndTime = s.Data.ActivityBaseInfo.ServiceTimestamp
		}, false},
		{"活动开奖", func(s *weapi.VipLotteryModuleListResp) {
			s.Data.ActivityBaseInfo.AwardTimestamp = s.Data.ActivityBaseInfo.ServiceTimestamp
		}, false},
		{"尚未开始", func(s *weapi.VipLotteryModuleListResp) {
			s.Data.ActivityBaseInfo.StartTime = s.Data.ActivityBaseInfo.ServiceTimestamp + 1
		}, false},
		{"状态非开放", func(s *weapi.VipLotteryModuleListResp) { s.Data.ActivityProcessInfo.Status = koiWishTestPointer(1) }, false},
		{"读取被拒绝", func(s *weapi.VipLotteryModuleListResp) { s.Code = 403 }, true},
		{"活动暂不可参与", func(s *weapi.VipLotteryModuleListResp) { s.Data.ActivityProcessInfo.Status = koiWishTestPointer(-1) }, false},
		{"未知负活动状态", func(s *weapi.VipLotteryModuleListResp) { s.Data.ActivityProcessInfo.Status = koiWishTestPointer(-2) }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := koiWishTestState()
			test.change(state)
			fake := &koiWishFake{states: []*weapi.VipLotteryModuleListResp{state}}
			command, _ := newKoiWishTestCommand()
			tokenCalls := 0
			err := command.runKoiWish(t.Context(), fake, "account", nil, func() string { tokenCalls++; return "secret-token" })
			if err != nil || fake.writes != 0 || fake.reads != 1 || tokenCalls != 0 {
				t.Fatalf("unexpected action: err=%v reads=%d writes=%d token=%d", err, fake.reads, fake.writes, tokenCalls)
			}
			failed, skipped, unconfirmed := command.root.Report.OutcomeCounts()
			if unconfirmed != 0 || (test.failure && (failed != 1 || skipped != 0)) || (!test.failure && (failed != 0 || skipped != 1)) {
				t.Fatalf("wrong outcomes: %d/%d/%d", failed, skipped, unconfirmed)
			}
		})
	}
}

func TestKoiWishMissingTokenSkipsAfterRead(t *testing.T) {
	command, output := newKoiWishTestCommand()
	fake := &koiWishFake{states: []*weapi.VipLotteryModuleListResp{koiWishTestState()}}
	err := command.runKoiWish(t.Context(), fake, "account", nil, func() string { return " " })
	_, skipped, _ := command.root.Report.OutcomeCounts()
	if err != nil || skipped != 1 || fake.writes != 0 || fake.reads != 1 || !strings.Contains(output.String(), "移动端") {
		t.Fatal("missing token did not safely skip")
	}
}

func TestKoiWishDiagnosticsContainOnlySafeState(t *testing.T) {
	command, output := newKoiWishTestCommand()
	fake := &koiWishFake{states: []*weapi.VipLotteryModuleListResp{koiWishTestState(), koiWishTestCompletedState()}, response: koiWishTestReceipt(), writeError: fmt.Errorf("secret-token")}
	fake.states[0].Data.ActivityBaseInfo.PrizeName = "活动\n名称"
	if err := command.runKoiWish(t.Context(), fake, "account", nil, func() string { return "secret-token" }); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"提交前查询", "提交后查询", `活动名="活动\n名称"`, "活动ID=123", "免费次数=1", "免费次数=0", "参与次数=0", "参与次数=1", "code=200", "success=true", "privilege=true", "count=1", "错误类型=*errors.errorString"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("diagnostics missing %q: %s", expected, output.String())
		}
	}
	if strings.Contains(output.String(), "secret-token") || strings.Contains(output.String(), "活动\n名称") {
		t.Fatal("diagnostic leaked error or unescaped control characters")
	}
}

func TestKoiWishSubmissionIsConfirmedOnlyByMatchingOfficialState(t *testing.T) {
	for _, test := range []struct {
		name                         string
		modify                       func(*koiWishFake)
		wantFailure, wantUnconfirmed int
		wantText                     string
	}{
		{"确认成功", func(f *koiWishFake) {}, 0, 0, "已确认本次免费许愿"},
		{"响应丢失但已消耗", func(f *koiWishFake) { f.response = nil; f.writeError = fmt.Errorf("secret-token response lost") }, 0, 0, "不能确认归因"},
		{"拒绝但已消耗", func(f *koiWishFake) { f.response.Code = 403 }, 0, 0, "不能确认归因"},
		{"明确拒绝未消耗", func(f *koiWishFake) { f.response.Code = 403; f.states[1] = koiWishTestState() }, 1, 0, "拒绝"},
		{"响应丢失未消耗", func(f *koiWishFake) {
			f.response = nil
			f.writeError = fmt.Errorf("secret-token")
			f.states[1] = koiWishTestState()
		}, 0, 1, "未确认"},
		{"后查失败", func(f *koiWishFake) { f.readErrors = []error{nil, fmt.Errorf("secret-token")} }, 0, 1, "未确认"},
		{"后查无数据", func(f *koiWishFake) { f.states[1] = nil }, 0, 1, "未确认"},
		{"缺成功回执", func(f *koiWishFake) { f.response.Data.Success = nil }, 0, 1, "未确认"},
		{"缺权益回执", func(f *koiWishFake) { f.response.Data.Privilege = nil }, 0, 1, "未确认"},
		{"缺次数回执", func(f *koiWishFake) { f.response.Data.Count = nil }, 0, 1, "未确认"},
		{"回执否认成功", func(f *koiWishFake) { f.response.Data.Success = koiWishTestPointer(false) }, 0, 1, "未确认"},
		{"回执无权益", func(f *koiWishFake) { f.response.Data.Privilege = koiWishTestPointer(false) }, 0, 1, "未确认"},
		{"回执次数超免费", func(f *koiWishFake) { f.response.Data.Count = koiWishTestPointer(int64(2)) }, 0, 1, "未确认"},
		{"回执次数为零", func(f *koiWishFake) { f.response.Data.Count = koiWishTestPointer(int64(0)) }, 0, 1, "未确认"},
		{"没有消耗", func(f *koiWishFake) { f.states[1] = koiWishTestState() }, 0, 1, "未确认"},
		{"活动变更", func(f *koiWishFake) { f.states[1].Data.ActivityProcessInfo.PrizeTableID++ }, 0, 1, "未确认"},
		{"拒绝但核对活动已变", func(f *koiWishFake) { f.response.Code = 403; f.states[1].Data.ActivityProcessInfo.PrizeTableID++ }, 0, 1, "未确认"},
		{"消耗与成功回执不匹配", func(f *koiWishFake) {
			f.states[0].Data.ThisDayCanUseFreeCount = koiWishTestPointer(int64(2))
			f.states[1].Data.ActivityProcessInfo.VisitorParticipateNum = koiWishTestPointer(int64(2))
		}, 0, 1, "未确认"},
		{"跨服务端日期", func(f *koiWishFake) { f.states[1].Data.ActivityBaseInfo.ServiceTimestamp += 24 * 60 * 60 * 1000 }, 0, 1, "未确认"},
		{"服务端时间倒退", func(f *koiWishFake) { f.states[1].Data.ActivityBaseInfo.ServiceTimestamp -= 2 }, 0, 1, "未确认"},
		{"已用标志未变", func(f *koiWishFake) { f.states[1].Data.UseEveryDayFreeCount = koiWishTestPointer(false) }, 0, 1, "未确认"},
		{"参与次数未增长", func(f *koiWishFake) {
			f.states[1].Data.ActivityProcessInfo.VisitorParticipateNum = koiWishTestPointer(int64(0))
		}, 0, 1, "未确认"},
		{"参与差值不匹配", func(f *koiWishFake) {
			f.states[1].Data.ActivityProcessInfo.VisitorParticipateNum = koiWishTestPointer(int64(2))
		}, 0, 1, "未确认"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &koiWishFake{states: []*weapi.VipLotteryModuleListResp{koiWishTestState(), koiWishTestCompletedState()}, response: koiWishTestReceipt()}
			test.modify(fake)
			command, output := newKoiWishTestCommand()
			err := command.runKoiWish(t.Context(), fake, "account", nil, func() string { return "secret-token" })
			failed, skipped, unknown := command.root.Report.OutcomeCounts()
			if err != nil || failed != test.wantFailure || unknown != test.wantUnconfirmed || skipped != 0 || fake.reads != 2 || fake.writes != 1 || !strings.Contains(output.String(), test.wantText) {
				t.Fatalf("err=%v outcomes=%d/%d/%d reads=%d writes=%d output=%s", err, failed, skipped, unknown, fake.reads, fake.writes, output.String())
			}
			if fake.request == nil || fake.request.PrizeTableID != 123 || fake.request.Token != "secret-token" {
				t.Fatal("wrong request identity")
			}
			if strings.Contains(output.String(), "secret-token") {
				t.Fatal("token/error leaked into log")
			}
			for _, item := range command.root.Report.Items() {
				if strings.Contains(fmt.Sprint(item), "secret-token") {
					t.Fatal("token/error leaked into report")
				}
			}
		})
	}
}

func TestKoiWishInitialReadFailureDoesNotLeakOrSubmit(t *testing.T) {
	command, output := newKoiWishTestCommand()
	fake := &koiWishFake{readErrors: []error{fmt.Errorf("secret-token")}}
	err := command.runKoiWish(t.Context(), fake, "account", nil, func() string { t.Fatal("token lookup before valid status"); return "" })
	failed, _, _ := command.root.Report.OutcomeCounts()
	if err != nil || failed != 1 || fake.writes != 0 || fake.reads != 1 || strings.Contains(output.String(), "secret-token") {
		t.Fatal("initial read failure was mishandled")
	}
}

func TestKoiWishSharedAccountReportPreventsSecondQueueAttempt(t *testing.T) {
	for _, readFails := range []bool{false, true} {
		t.Run(fmt.Sprint(readFails), func(t *testing.T) {
			command, output := newKoiWishTestCommand()
			fake := &koiWishFake{states: []*weapi.VipLotteryModuleListResp{koiWishTestState(), koiWishTestState(), koiWishTestState(), koiWishTestState()}, writeError: fmt.Errorf("response lost")}
			if readFails {
				fake.readErrors = []error{fmt.Errorf("query failed")}
			}
			report := &accountEarningsReport{}
			getToken := func() string { return "secret-token" }
			if err := command.runKoiWish(t.Context(), fake, "account", report, getToken); err != nil {
				t.Fatal(err)
			}
			reads, writes := fake.reads, fake.writes
			if err := command.runKoiWish(t.Context(), fake, "account", report, getToken); err != nil {
				t.Fatal(err)
			}
			if fake.reads != reads || fake.writes != writes || !strings.Contains(output.String(), "同批次已处理") {
				t.Fatal("second queue repeated an uncertain attempt")
			}
			if err := command.runKoiWish(t.Context(), fake, "account", &accountEarningsReport{}, getToken); err != nil {
				t.Fatal(err)
			}
			if fake.reads <= reads {
				t.Fatal("a new batch did not read official state")
			}
		})
	}
}

func TestKoiWishProtocolRequiresActualMatchingMobileSession(t *testing.T) {
	for _, test := range []struct {
		name, os, iosUA, androidUA string
		want                       api.CryptoMode
	}{
		{"iOS", "iPhone OS", "NeteaseMusic 9.4.95/6806 (iPhone; iOS 16.6.1; zh_CN)", "", api.CryptoModeEAPI},
		{"Android", "android", "", "NeteaseMusic/9.6.13.1234 (Android 16)", api.CryptoModeXEAPI},
		{"未知Cookie不借用iOS默认", "", "neteasemusic/9.6.05 (iPhone; iOS 16.6.1; Scale/2.00)", "", ""},
		{"网页Cookie不借用Android配置", "pc", "", "NeteaseMusic/9.6.13.1234 (Android 16)", ""},
		{"Android缺UA", "android", "", "", ""},
		{"Android配错UA", "android", "", "NeteaseMusic (iPhone; iOS 16)", ""},
		{"iOS配错UA", "iPhone OS", "NeteaseMusic (Android 16)", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, err := api.NewClient(&api.Config{Cookie: cookiepkg.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}, UserAgent: api.UserAgentConfig{EAPI: test.iosUA, XEAPI: test.androidUA}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })
			base, _ := url.Parse("https://music.163.com/")
			if test.os != "" {
				client.SetCookies(base, []*http.Cookie{{Name: "os", Value: test.os, Path: "/"}})
			}
			if got := koiWishProtocol(client); got != test.want {
				t.Fatalf("got %q want %q", got, test.want)
			}
			stored, exists := client.Cookie(base.String(), "os")
			if (test.os == "" && exists) || (test.os != "" && (!exists || stored.Value != test.os)) {
				t.Fatal("protocol selection changed cookie identity")
			}
		})
	}
}

func TestKoiWishAdapterSetsExplicitModeWithoutMutatingRequest(t *testing.T) {
	for _, mode := range []api.CryptoMode{api.CryptoModeEAPI, api.CryptoModeXEAPI} {
		request := &eapi.VipLotteryFreeClickReq{PrizeTableID: 123, Token: "secret-token"}
		calls := 0
		client := &koiWishClient{mode: mode, write: func(ctx context.Context, got *eapi.VipLotteryFreeClickReq) (*eapi.VipLotteryFreeClickResp, error) {
			calls++
			if got.CryptoMode != mode || got.PrizeTableID != request.PrizeTableID || got.Token != request.Token {
				t.Fatal("adapter lost protocol or identity")
			}
			return koiWishTestReceipt(), nil
		}}
		if _, err := client.VipLotteryFreeClick(t.Context(), request); err != nil || calls != 1 || request.CryptoMode != "" {
			t.Fatal("adapter changed original request or write count")
		}
	}
}

func TestKoiWishCancellationStopsSubsequentRequests(t *testing.T) {
	for _, point := range []string{"before", "after-read", "token", "after-write"} {
		t.Run(point, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fake := &koiWishFake{states: []*weapi.VipLotteryModuleListResp{koiWishTestState(), koiWishTestCompletedState()}, response: koiWishTestReceipt()}
			if point == "before" {
				cancel()
			}
			if point == "after-read" {
				fake.onRead = func(int) { cancel() }
			}
			if point == "after-write" {
				fake.onWrite = cancel
			}
			command, _ := newKoiWishTestCommand()
			err := command.runKoiWish(ctx, fake, "account", nil, func() string {
				if point == "token" {
					cancel()
				}
				return "secret-token"
			})
			wantReads, wantWrites := 1, 0
			if point == "before" {
				wantReads = 0
			}
			if point == "after-write" {
				wantWrites = 1
			}
			if err != context.Canceled || fake.reads != wantReads || fake.writes != wantWrites {
				t.Fatalf("err=%v reads=%d writes=%d", err, fake.reads, fake.writes)
			}
		})
	}
}

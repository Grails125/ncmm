package ncmm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/types"
	"github.com/3899/ncmm/config"
	cookiepkg "github.com/3899/ncmm/pkg/cookie"
	"github.com/3899/ncmm/pkg/log"
)

type musicianSignConfirmationFixture struct {
	command                  *Musician
	ctx                      *musicianContext
	output                   bytes.Buffer
	task                     eapi.MusicianMissionTask
	finalStatus              int64
	finalPeriod              int64
	finalActionType          int64
	claimError               error
	claimCode                int64
	missing                  bool
	finalMissing             bool
	finalReadFailure         bool
	stageReadFailure         bool
	compatReadFailure        bool
	conflictingSources       bool
	cycles, accesses, claims int
}

func newMusicianSignConfirmationFixture(t *testing.T) *musicianSignConfirmationFixture {
	t.Helper()
	f := &musicianSignConfirmationFixture{
		task:        eapi.MusicianMissionTask{Business: "musician", UserMissionId: 88, MissionId: 3611300, Description: "音乐人中心签到", ActionType: 102, Status: 20, ProgressRate: 1, TargetCount: 1, RewardId: 100000, RewardWorth: "1", Period: 1},
		finalStatus: 100, claimCode: 200,
	}
	f.command = NewMusician(&Root{Cfg: &config.Config{Musician: &config.MusicianConf{}}}, nil)
	f.command.Command().SetOut(&f.output)
	f.command.Command().SetErr(&f.output)
	cookiePath := filepath.Join(t.TempDir(), "cookie.json")
	if err := os.WriteFile(cookiePath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := api.NewClient(&api.Config{Retry: 0, Cookie: cookiepkg.Config{Filepath: cookiePath, ReadOnly: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	f.ctx = &musicianContext{cli: client, eapiCli: eapi.New(client), db: newPlayStatsMemoryDB()}
	client.GetClient().Transport = musicianVipClaimTransport(func(request *http.Request) (*http.Response, error) {
		var response any
		switch request.URL.Path {
		case "/eapi/creator/user/access":
			f.accesses++
			response = eapi.MusicianSignResp{RespCommon: types.RespCommon[bool]{Code: 200, Data: true}}
		case "/eapi/nmusician/workbench/mission/cycle/list":
			f.cycles++
			if f.compatReadFailure && f.cycles%2 == 0 {
				return giftJSONResponse(request, `{"code":500}`), nil
			}
			current := f.task
			if f.conflictingSources && f.cycles%2 == 0 {
				current.Status = 100
			}
			postClaim := f.claims > 0
			if postClaim {
				current.Status = f.finalStatus
				if f.finalActionType != 0 {
					current.ActionType = f.finalActionType
				}
				if f.finalPeriod != 0 {
					current.Period = f.finalPeriod
				}
				if f.finalReadFailure {
					return giftJSONResponse(request, `{"code":500,"message":"token=must-not-log"}`), nil
				}
			}
			tasks := []eapi.MusicianMissionTask{current}
			if f.missing || postClaim && f.finalMissing {
				tasks = nil
			}
			response = eapi.MusicianMissionListResp{RespCommon: types.RespCommon[eapi.MusicianMissionListData]{Code: 200, Data: eapi.MusicianMissionListData{List: tasks}}}
		case "/eapi/nmusician/workbench/mission/stage/list":
			if f.stageReadFailure {
				return giftJSONResponse(request, `{"code":500}`), nil
			}
			response = eapi.MusicianMissionListResp{RespCommon: types.RespCommon[eapi.MusicianMissionListData]{Code: 200}}
		case "/eapi/nmusician/workbench/mission/reward/obtain/new":
			f.claims++
			if f.claimError != nil {
				return nil, f.claimError
			}
			response = eapi.MusicianRewardObtainResp{RespCommon: types.RespCommon[any]{Code: f.claimCode}}
		default:
			t.Fatalf("签到确认不应调用其他接口：%s", request.URL.Path)
		}
		body, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
	})
	return f
}

func TestMusicianSignConfirmsOnlyFreshClaimedTask(t *testing.T) {
	for _, tc := range []struct {
		name      string
		modify    func(*musicianSignConfirmationFixture)
		wantError bool
	}{
		{"达标20领取后100", nil, false},
		{"领取返回200但仍20", func(f *musicianSignConfirmationFixture) { f.finalStatus = 20 }, true},
		{"领取超时但复查100", func(f *musicianSignConfirmationFixture) { f.claimError = context.DeadlineExceeded }, false},
		{"领取超时且仍20", func(f *musicianSignConfirmationFixture) { f.claimError = context.DeadlineExceeded; f.finalStatus = 20 }, true},
		{"复查缺少同一任务", func(f *musicianSignConfirmationFixture) { f.finalMissing = true }, true},
		{"复查任务周期已变", func(f *musicianSignConfirmationFixture) { f.finalPeriod = 2 }, true},
		{"其他任务的100不能确认签到", func(f *musicianSignConfirmationFixture) { f.finalActionType = 999 }, true},
		{"复查业务失败", func(f *musicianSignConfirmationFixture) { f.finalReadFailure = true }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMusicianSignConfirmationFixture(t)
			if tc.modify != nil {
				tc.modify(f)
			}
			err := f.command.doSignPhase(t.Context(), f.ctx, "account.json")
			if (err != nil) != tc.wantError || f.claims != 1 {
				t.Fatalf("err=%v，领奖次数=%d，日志=%s", err, f.claims, f.output.String())
			}
			cached, _ := f.ctx.db.Exists(t.Context(), musicianRewardClaimCacheKey("account.json", f.task.UserMissionId, f.task.Period))
			if cached == tc.wantError {
				t.Fatalf("只有复查确认100后可保存成功缓存，cached=%t", cached)
			}
			if tc.wantError && strings.Contains(f.output.String(), "音乐人日常签到成功") {
				t.Fatalf("未确认结果不能宣告签到成功：%s", f.output.String())
			}
			if strings.Contains(f.output.String(), "must-not-log") || strings.Contains(f.output.String(), "deviceId") {
				t.Fatal("任务日志泄漏请求或原始响应")
			}
		})
	}
}

func TestMusicianSignAlreadyClaimedAndPendingStates(t *testing.T) {
	for _, status := range []int64{0, 10, 100} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := newMusicianSignConfirmationFixture(t)
			f.task.Status = status
			err := f.command.doSignPhase(t.Context(), f.ctx, "account.json")
			if (err != nil) != (status != 100) || f.claims != 0 {
				t.Fatalf("状态=%d err=%v claims=%d，日志=%s", status, err, f.claims, f.output.String())
			}
		})
	}
}

func TestMusicianSignServerPendingIgnoresOldClaimCache(t *testing.T) {
	f := newMusicianSignConfirmationFixture(t)
	if err := f.ctx.db.Set(t.Context(), musicianRewardClaimCacheKey("account.json", f.task.UserMissionId, f.task.Period), "1"); err != nil {
		t.Fatal(err)
	}
	if err := f.command.doSignPhase(t.Context(), f.ctx, "account.json"); err != nil || f.claims != 1 {
		t.Fatalf("旧错误缓存不应挡住平台状态20：err=%v claims=%d", err, f.claims)
	}
}

func TestMusicianSignMissingTaskNeverSucceeds(t *testing.T) {
	f := newMusicianSignConfirmationFixture(t)
	f.missing = true
	if err := f.command.doSignPhase(t.Context(), f.ctx, "account.json"); err == nil || f.claims != 0 {
		t.Fatalf("缺少签到任务不能视为成功，err=%v claims=%d", err, f.claims)
	}
}

func TestMusicianSignFallbackAndConflictingSources(t *testing.T) {
	f := newMusicianSignConfirmationFixture(t)
	f.compatReadFailure, f.stageReadFailure = true, true
	if err := f.command.doSignPhase(t.Context(), f.ctx, "account.json"); err != nil || f.claims != 1 {
		t.Fatalf("主移动端明确状态可在兼容和阶段源失败时确认签到：err=%v claims=%d", err, f.claims)
	}
	f = newMusicianSignConfirmationFixture(t)
	f.conflictingSources = true
	if err := f.command.doSignPhase(t.Context(), f.ctx, "account.json"); err == nil || f.claims != 0 {
		t.Fatalf("同一任务多源20/100冲突不能盲领或报成功：err=%v claims=%d", err, f.claims)
	}
}

func TestMusicianSignCacheReadFailureDoesNotBlockPlatformPending(t *testing.T) {
	f := newMusicianSignConfirmationFixture(t)
	f.ctx.db = &musicianVipClaimFailingDB{playStatsMemoryDB: newPlayStatsMemoryDB(), readError: fmt.Errorf("不应依赖旧缓存")}
	if err := f.command.doSignPhase(t.Context(), f.ctx, "account.json"); err != nil || f.claims != 1 {
		t.Fatalf("缓存读取失败不能阻挡平台明确的20：err=%v claims=%d", err, f.claims)
	}
}

func TestMusicianRewardStatusContract(t *testing.T) {
	for _, tc := range []struct {
		status    int64
		text      string
		claimable bool
	}{{0, "未完成", false}, {10, "进行中", false}, {20, "达标待领奖", true}, {100, "已领取", false}} {
		task := eapi.MusicianMissionTask{UserMissionId: 88, Status: tc.status, NeedToReceive: 1}
		if got := isMusicianRewardClaimable(task); got != tc.claimable {
			t.Errorf("status=%d 可领=%t，预期=%t", tc.status, got, tc.claimable)
		}
		if text := musicianTaskStatusText(task); !strings.Contains(text, tc.text) || !strings.Contains(text, fmt.Sprint(tc.status)) {
			t.Errorf("状态提示不明确：%s", text)
		}
	}
	for _, stages := range []json.RawMessage{json.RawMessage(`[]`), json.RawMessage(`null`), json.RawMessage(`[{"status":20}]`)} {
		task := eapi.MusicianMissionTask{UserMissionId: 88, Status: 20, UserStageTargetList: stages}
		if isMusicianRewardClaimable(task) {
			t.Error("带 userStageTargetList 的阶段聚合任务不能直接领奖")
		}
	}
}

func TestMusicianExecuteSignContinuesAccountsAndReturnsFailures(t *testing.T) {
	command := NewMusician(&Root{Cfg: &config.Config{Musician: &config.MusicianConf{EnableMain: true, EnableSecondaries: true}, Accounts: &config.AccountsConf{Main: "main.json", Secondary: []string{"other.json"}}, Network: &api.Config{}}}, nil)
	var output bytes.Buffer
	command.Command().SetOut(&output)
	command.Command().SetErr(&output)
	var calls int
	command.newClient = func(*api.Config, *log.Logger) (*api.Client, error) {
		calls++
		return nil, fmt.Errorf("合成账号初始化失败")
	}
	if err := command.executeSign(t.Context()); err == nil || calls != 2 {
		t.Fatalf("单账号失败后应继续其他账号并返回失败：err=%v calls=%d", err, calls)
	}
}

func TestMusicianExecuteReturnsAccountFailure(t *testing.T) {
	command := NewMusician(&Root{Cfg: &config.Config{Musician: &config.MusicianConf{EnableMain: true}, Accounts: &config.AccountsConf{Main: "main.json"}, Network: &api.Config{}}}, nil)
	var output bytes.Buffer
	command.Command().SetOut(&output)
	command.Command().SetErr(&output)
	command.newClient = func(*api.Config, *log.Logger) (*api.Client, error) {
		return nil, fmt.Errorf("合成账号初始化失败")
	}
	if err := command.execute(t.Context()); err == nil {
		t.Fatal("全套音乐人命令不能吞掉账号失败")
	}
}

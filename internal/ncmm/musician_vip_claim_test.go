package ncmm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/types"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/config"
	cookiepkg "github.com/3899/ncmm/pkg/cookie"
	"github.com/3899/ncmm/pkg/log"
)

type musicianVipClaimTransport func(*http.Request) (*http.Response, error)

func (f musicianVipClaimTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type musicianVipClaimFixture struct {
	command    *Musician
	ctx        *musicianContext
	db         *playStatsMemoryDB
	output     bytes.Buffer
	info       weapi.MusicianVipInfoResp
	claim      weapi.MusicianVipGetResp
	infoError  error
	claimError error
	infoCalls  int
	claimCalls int
}

func newMusicianVipClaimFixture(t *testing.T) *musicianVipClaimFixture {
	t.Helper()
	f := &musicianVipClaimFixture{
		db: newPlayStatsMemoryDB(),
		info: weapi.MusicianVipInfoResp{RespCommon: types.RespCommon[weapi.MusicianVipInfoData]{
			Code: 200,
			Data: weapi.MusicianVipInfoData{CanOpen: true, TaskStatus: true, FurtherVipGetTime: time.Now().Add(-24 * time.Hour).UnixMilli()},
		}},
		claim: weapi.MusicianVipGetResp{RespCommon: types.RespCommon[bool]{Code: 200, Data: true}},
	}
	f.command = NewMusician(&Root{Cfg: &config.Config{
		Musician: &config.MusicianConf{},
		Accounts: &config.AccountsConf{AntiCheatTokens: map[string]string{"account-a.json": "test-token", "account-b.json": "test-token"}},
	}}, nil)
	f.command.Command().SetOut(&f.output)
	f.command.Command().SetErr(&f.output)
	client, err := api.NewClient(&api.Config{Cookie: cookiepkg.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, log.Default)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	client.GetClient().Transport = musicianVipClaimTransport(func(request *http.Request) (*http.Response, error) {
		var payload any
		if request.Method != http.MethodPost || request.Host != "interface.music.163.com" {
			t.Errorf("unexpected request: %s %s", request.Method, request.Host)
		}
		switch request.URL.Path {
		case "/weapi/nmusician/workbench/special/right/vip/info":
			f.infoCalls++
			if f.infoError != nil {
				return nil, f.infoError
			}
			payload = f.info
		case "/weapi/nmusician/workbench/special/right/vip/get":
			f.claimCalls++
			if f.claimError != nil {
				return nil, f.claimError
			}
			payload = f.claim
		default:
			t.Fatalf("unexpected request path: %s", request.URL.Path)
		}
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
	})
	f.ctx = &musicianContext{weapiCli: weapi.New(client), db: f.db}
	return f
}

func (f *musicianVipClaimFixture) run(account string) {
	f.command.doVipClaimPhase(context.Background(), f.ctx, account)
}

func TestMusicianVipClaimReservationDoesNotCacheSuccess(t *testing.T) {
	f := newMusicianVipClaimFixture(t)
	f.info.Data.CanOpen = false
	f.info.Data.FurtherVipGetTime = time.Now().Add(24 * time.Hour).UnixMilli()
	f.run("account-a.json")
	if f.infoCalls != 1 || f.claimCalls != 0 || len(f.db.values) != 0 {
		t.Fatalf("reservation must only read status: info=%d claim=%d cache=%v", f.infoCalls, f.claimCalls, f.db.values)
	}
	if strings.Contains(f.output.String(), "已成功领取") || !strings.Contains(f.output.String(), "开放领取") {
		t.Fatalf("reservation output must explain waiting: %s", f.output.String())
	}
	// 到达领取窗口后，同一个任务可以正常提交，不能受预约期的旧状态影响。
	f.info.Data.CanOpen = true
	f.info.Data.FurtherVipGetTime = time.Now().Add(-time.Hour).UnixMilli()
	f.run("account-a.json")
	if f.claimCalls != 1 || len(f.db.values) != 1 {
		t.Fatalf("opened cycle was not claimed: claim=%d cache=%v", f.claimCalls, f.db.values)
	}
}

func TestMusicianVipClaimIgnoresLegacyMonthCache(t *testing.T) {
	f := newMusicianVipClaimFixture(t)
	legacyKey := fmt.Sprintf("musician:vip:claim:account-a.json:%s", time.Now().Format("2006-01"))
	f.db.values[legacyKey] = "1"
	f.run("account-a.json")
	if f.infoCalls != 1 || f.claimCalls != 1 {
		t.Fatalf("legacy cache blocked current eligibility: info=%d claim=%d", f.infoCalls, f.claimCalls)
	}
}

func TestMusicianVipClaimCacheUsesOfficialCycleAndAccount(t *testing.T) {
	f := newMusicianVipClaimFixture(t)
	// 上月开始、延续至本月的同一官方周期，重复执行仍应去重。
	cycle := time.Now().AddDate(0, -1, 0).UnixMilli()
	f.info.Data.FurtherVipGetTime = cycle
	f.run("account-a.json")
	f.run("account-a.json")
	if f.infoCalls != 2 || f.claimCalls != 1 {
		t.Fatalf("repeat must refresh status but not claim twice: info=%d claim=%d", f.infoCalls, f.claimCalls)
	}
	cycleKey := fmt.Sprintf("musician:vip:claim:account-a.json:%d", cycle)
	if f.db.values[cycleKey] != "1" {
		t.Fatalf("success not stored by official cycle: %v", f.db.values)
	}
	// 同一个自然月发生周期变化，旧周期不得阻止新周期领取。
	f.info.Data.FurtherVipGetTime = time.Now().Add(-time.Hour).UnixMilli()
	f.run("account-a.json")
	if f.claimCalls != 2 {
		t.Fatalf("new cycle did not claim: calls=%d", f.claimCalls)
	}
	f.run("account-b.json")
	if f.claimCalls != 3 || len(f.db.values) != 3 {
		t.Fatalf("account caches were not isolated: calls=%d cache=%v", f.claimCalls, f.db.values)
	}
}

func TestMusicianVipClaimOnlyCachesConfirmedSuccess(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*musicianVipClaimFixture)
		claims  int
	}{
		{"unfinished", func(f *musicianVipClaimFixture) { f.info.Data.TaskStatus = false }, 0},
		{"not_open", func(f *musicianVipClaimFixture) { f.info.Data.CanOpen = false }, 0},
		{"future_despite_can_open", func(f *musicianVipClaimFixture) {
			f.info.Data.FurtherVipGetTime = time.Now().Add(time.Hour).UnixMilli()
		}, 0},
		{"info_business_failure", func(f *musicianVipClaimFixture) { f.info.Code = 500 }, 0},
		{"info_network_failure", func(f *musicianVipClaimFixture) { f.infoError = fmt.Errorf("mock info failure") }, 0},
		{"claim_false", func(f *musicianVipClaimFixture) { f.claim.Data = false }, 1},
		{"claim_business_failure", func(f *musicianVipClaimFixture) { f.claim.Code = 500 }, 1},
		{"claim_response_lost", func(f *musicianVipClaimFixture) { f.claimError = fmt.Errorf("mock response lost") }, 1},
		{"missing_token", func(f *musicianVipClaimFixture) { f.command.root.Cfg.Accounts = nil }, 0},
		{"missing_cycle", func(f *musicianVipClaimFixture) { f.info.Data.FurtherVipGetTime = 0 }, 1},
		{"invalid_cycle", func(f *musicianVipClaimFixture) { f.info.Data.FurtherVipGetTime = -1 }, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMusicianVipClaimFixture(t)
			tc.prepare(f)
			f.run("account-a.json")
			if f.claimCalls != tc.claims || len(f.db.values) != 0 {
				t.Fatalf("unconfirmed cycle must not cache: claims=%d want=%d cache=%v", f.claimCalls, tc.claims, f.db.values)
			}
		})
	}
}

func TestMusicianVipClaimFurtherTaskAndCacheUnavailable(t *testing.T) {
	f := newMusicianVipClaimFixture(t)
	f.info.Data.TaskStatus = false
	f.info.Data.FurtherTask = &weapi.MusicianVipFurtherTask{MissionStatus: 100}
	f.ctx.db = nil
	f.run("account-a.json")
	if f.claimCalls != 1 {
		t.Fatalf("completed further task without cache must claim: %d", f.claimCalls)
	}
}

func TestMusicianVipClaimDisabledDoesNotContactService(t *testing.T) {
	f := newMusicianVipClaimFixture(t)
	disabled := false
	f.command.root.Cfg.Musician.EnableVipClaim = &disabled
	f.run("account-a.json")
	if f.infoCalls != 0 || f.claimCalls != 0 {
		t.Fatalf("disabled claim contacted service: info=%d claim=%d", f.infoCalls, f.claimCalls)
	}
}

type musicianVipClaimFailingDB struct {
	*playStatsMemoryDB
	readError  error
	writeError error
}

func (db *musicianVipClaimFailingDB) Exists(ctx context.Context, key string) (bool, error) {
	if db.readError != nil {
		return false, db.readError
	}
	return db.playStatsMemoryDB.Exists(ctx, key)
}

func (db *musicianVipClaimFailingDB) Set(ctx context.Context, key, value string, ttl ...time.Duration) error {
	if db.writeError != nil {
		return db.writeError
	}
	return db.playStatsMemoryDB.Set(ctx, key, value, ttl...)
}

func TestMusicianVipClaimCacheErrorsRemainVisible(t *testing.T) {
	for _, writeFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("write_failure_%t", writeFailure), func(t *testing.T) {
			f := newMusicianVipClaimFixture(t)
			db := &musicianVipClaimFailingDB{playStatsMemoryDB: f.db}
			wantRecords := 1
			wantLog := "读取音乐人 VIP 本期成功记录失败"
			if writeFailure {
				db.writeError = fmt.Errorf("mock cache write failure")
				wantRecords = 0
				wantLog = "领取已成功，但保存本期成功记录失败"
			} else {
				db.readError = fmt.Errorf("mock cache read failure")
			}
			f.ctx.db = db
			f.run("account-a.json")
			if f.claimCalls != 1 || len(f.db.values) != wantRecords || !strings.Contains(f.output.String(), wantLog) {
				t.Fatalf("cache failure lost result or diagnostic: claim=%d cache=%v log=%s", f.claimCalls, f.db.values, f.output.String())
			}
		})
	}
}

func TestMusicianVipClaimRefreshFailureDoesNotTrustCachedState(t *testing.T) {
	f := newMusicianVipClaimFixture(t)
	f.db.values[musicianVipClaimCacheKey("account-a.json", f.info.Data.FurtherVipGetTime)] = "1"
	f.infoError = fmt.Errorf("mock refresh failure")
	f.run("account-a.json")
	if f.infoCalls != 1 || f.claimCalls != 0 || strings.Contains(f.output.String(), "已领取") {
		t.Fatalf("cached state masked refresh failure: info=%d claim=%d output=%s", f.infoCalls, f.claimCalls, f.output.String())
	}
}

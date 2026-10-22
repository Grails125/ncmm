package ncmm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/types"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/pkg/log"
)

type partnerFake struct {
	daily       func(context.Context) (*weapi.PartnerTaskResp, error)
	extra       func(context.Context) (*weapi.PartnerExtraTaskResp, error)
	evaluate    func(context.Context, *weapi.PartnerEvaluateReq) (*weapi.PartnerEvaluateResp, error)
	report      func(context.Context, *weapi.PartnerExtraReportReq) (*weapi.PartnerExtraReportResp, error)
	evaluations []weapi.PartnerEvaluateReq
	reports     []weapi.PartnerExtraReportReq
}

func (f *partnerFake) PartnerDailyTask(ctx context.Context, _ *weapi.PartnerTaskReq) (*weapi.PartnerTaskResp, error) {
	return f.daily(ctx)
}

func (f *partnerFake) PartnerExtraTask(ctx context.Context, _ *weapi.PartnerExtraTaskReq) (*weapi.PartnerExtraTaskResp, error) {
	if f.extra != nil {
		return f.extra(ctx)
	}
	return &weapi.PartnerExtraTaskResp{RespCommon: types.RespCommon[[]weapi.PartnerExtraTaskRespData]{Code: 200, Data: []weapi.PartnerExtraTaskRespData{}}}, nil
}

func (f *partnerFake) PartnerEvaluate(ctx context.Context, req *weapi.PartnerEvaluateReq) (*weapi.PartnerEvaluateResp, error) {
	f.evaluations = append(f.evaluations, *req)
	if f.evaluate != nil {
		return f.evaluate(ctx, req)
	}
	return &weapi.PartnerEvaluateResp{RespCommon: types.RespCommon[weapi.PartnerEvaluateRespData]{Code: 200, Data: weapi.PartnerEvaluateRespData{EvaluateRes: true}}}, nil
}

func (f *partnerFake) PartnerExtraReport(ctx context.Context, req *weapi.PartnerExtraReportReq) (*weapi.PartnerExtraReportResp, error) {
	f.reports = append(f.reports, *req)
	if f.report != nil {
		return f.report(ctx, req)
	}
	return &weapi.PartnerExtraReportResp{RespCommon: types.RespCommon[weapi.PartnerExtraReportRespData]{Code: 200, Data: weapi.PartnerExtraReportRespData{InteractResult: true}}}, nil
}

func partnerTestDaily(completed ...bool) *weapi.PartnerTaskResp {
	works := make([]string, len(completed))
	finished := 0
	for i, done := range completed {
		if done {
			finished++
		}
		works[i] = fmt.Sprintf(`{"work":{"id":%d,"resourceId":%d,"name":"中文歌曲","authorName":"作者"},"completed":%t}`, i+1, i+100, done)
	}
	var response weapi.PartnerTaskResp
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"code":200,"data":{"id":10,"count":%d,"completedCount":%d,"works":[%s]}}`, len(completed), finished, strings.Join(works, ","))), &response); err != nil {
		panic(err)
	}
	return &response
}

func partnerTestExtra(count, completed int) *weapi.PartnerExtraTaskResp {
	result := &weapi.PartnerExtraTaskResp{RespCommon: types.RespCommon[[]weapi.PartnerExtraTaskRespData]{Code: 200, Data: make([]weapi.PartnerExtraTaskRespData, count)}}
	for i := range result.Data {
		result.Data[i] = weapi.PartnerExtraTaskRespData{Work: weapi.PartnerWork{Id: int64(i + 100), ResourceId: int64(i + 1000), Name: "英文Song", AuthorName: "作者"}, Completed: i < completed}
	}
	return result
}

func partnerTestCommand(t *testing.T) (*MusicPartner, *config.MusicPartnerConf) {
	t.Helper()
	cfg := &config.MusicPartnerConf{Score: 3, ExtraLimit: 7, WaitMin: 15, WaitMax: 20}
	command := NewMusicPartner(&Root{Cfg: &config.Config{MusicPartner: cfg}}, nil)
	command.Command().SetOut(io.Discard)
	command.Command().SetErr(io.Discard)
	command.wait = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	return command, cfg
}

func TestMusicPartnerScoreStrategies(t *testing.T) {
	for strategy := 1; strategy <= 4; strategy++ {
		for _, tt := range []struct {
			name, author string
			english      bool
		}{{"中文", "作者", false}, {"中文Song", "作者", true}, {"中文", "Artist", true}, {"１２３é", "作者", false}} {
			want := strategy
			if strategy < 4 && tt.english {
				want++
			}
			score, tag := musicPartnerScore(weapi.PartnerWork{Name: tt.name, AuthorName: tt.author}, strategy)
			if score != fmt.Sprint(want) || string(tag) != fmt.Sprintf("%d-A-1", want) {
				t.Fatalf("strategy=%d work=%q/%q: %q %q", strategy, tt.name, tt.author, score, tag)
			}
		}
	}
}

func TestMusicPartnerAccountBlockClassification(t *testing.T) {
	for _, tt := range []struct {
		code    int64
		message string
		blocked bool
	}{{301, "", true}, {401, "", true}, {403, "", true}, {419, "", true}, {429, "", true}, {460, "", true}, {405, "频繁", true}, {500, "captcha", true}, {703, "", false}, {405, "资源状态异常", false}, {500, "", false}} {
		err := musicPartnerResponseError("评分", tt.code, tt.message)
		var failure *musicPartnerFailure
		if !errors.As(err, &failure) || failure.accountBlocked != tt.blocked {
			t.Errorf("code=%d message=%q: blocked classification differs", tt.code, tt.message)
		}
	}
}

func TestMusicPartnerDayContext(t *testing.T) {
	// UTC 15:59 对应北京时间 23:59；截止必须是下一分钟，而非本地时区午夜。
	date := time.Now().UTC().Add(48 * time.Hour)
	now := time.Date(date.Year(), date.Month(), date.Day(), 15, 59, 0, 0, time.UTC)
	want := now.Add(time.Minute)
	ctx, cancel := musicPartnerDayContext(context.Background(), now)
	defer cancel()
	if got, ok := ctx.Deadline(); !ok || !got.Equal(want) {
		t.Fatalf("deadline=%v, want=%v", got, want)
	}
	early, stopEarly := context.WithDeadline(context.Background(), time.Now().Add(time.Minute))
	defer stopEarly()
	child, stopChild := musicPartnerDayContext(early, now)
	defer stopChild()
	parentDeadline, _ := early.Deadline()
	if got, _ := child.Deadline(); !got.Equal(parentDeadline) {
		t.Fatalf("parent deadline=%v, child=%v", parentDeadline, got)
	}
	stopEarly()
	if !errors.Is(child.Err(), context.Canceled) {
		t.Fatalf("parent cancellation not propagated: %v", child.Err())
	}
	expired, stopExpired := musicPartnerDayContext(context.Background(), time.Now().Add(-48*time.Hour))
	defer stopExpired()
	if !errors.Is(expired.Err(), context.DeadlineExceeded) {
		t.Fatalf("expired day not canceled: %v", expired.Err())
	}
}

func TestMusicPartnerReconcilePreservesAccountBlock(t *testing.T) {
	for _, extra := range []bool{false, true} {
		command, _ := partnerTestCommand(t)
		fake := &partnerFake{
			daily: func(context.Context) (*weapi.PartnerTaskResp, error) {
				if extra {
					return partnerTestDaily(), nil
				}
				return nil, &api.HTTPStatusError{StatusCode: http.StatusUnauthorized}
			},
			extra: func(context.Context) (*weapi.PartnerExtraTaskResp, error) {
				return nil, &api.HTTPStatusError{StatusCode: http.StatusTooManyRequests}
			},
		}
		err := command.confirmScore(context.Background(), fake, 10, 100, extra)
		var failure *musicPartnerFailure
		if !errors.As(err, &failure) || !failure.accountBlocked {
			t.Fatalf("extra=%t: reconciliation lost account block: %v", extra, err)
		}
	}
}

func TestMusicPartnerConfiguredCookiePathsUseHome(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint("explicit=", explicit), func(t *testing.T) {
			command, cfg := partnerTestCommand(t)
			command.root.Opts.Home = t.TempDir()
			command.root.Cfg.Network = &api.Config{}
			cfg.EnableMain, cfg.EnableSecondaries = true, true
			absolute := filepath.Join(t.TempDir(), "absolute.json")
			command.root.Cfg.Accounts = &config.AccountsConf{Main: "./main.json", Secondary: []string{"./secondary.json", absolute}}
			want := []string{filepath.Join(command.root.Opts.Home, "main.json"), filepath.Join(command.root.Opts.Home, "secondary.json"), absolute}
			if explicit {
				command.cookieFile = "./explicit.json"
				path, err := filepath.Abs(command.cookieFile)
				if err != nil {
					t.Fatal(err)
				}
				want = []string{path}
			}
			var seen []string
			command.newClient = func(network *api.Config, _ *log.Logger) (*api.Client, error) {
				seen = append(seen, network.Cookie.Filepath)
				return nil, errors.New("test: stop before reading cookies or sending HTTP")
			}
			if err := command.execute(context.Background()); err == nil {
				t.Fatal("test client failure must be returned")
			}
			if fmt.Sprint(seen) != fmt.Sprint(want) {
				t.Fatalf("cookie paths=%v, want=%v", seen, want)
			}
		})
	}
}

func TestMusicPartnerSkipsCompletedDailyAndUsesNativeRequest(t *testing.T) {
	command, cfg := partnerTestCommand(t)
	fake := &partnerFake{daily: func(context.Context) (*weapi.PartnerTaskResp, error) { return partnerTestDaily(true, false), nil }}
	if err := command.run(context.Background(), fake, cfg); err != nil {
		t.Fatal(err)
	}
	if len(fake.evaluations) != 1 {
		t.Fatalf("evaluations=%d", len(fake.evaluations))
	}
	got := fake.evaluations[0]
	if got.TaskId != "10" || got.WorkId != "2" || got.Score != "3" || got.Tags != "3-A-1" || got.CustomTags != "[]" || got.SyncYunCircle || got.SyncComment || got.ExtraResource {
		t.Fatalf("unexpected request: %+v", got)
	}
}

func TestMusicPartnerExtraLimitAndFullList(t *testing.T) {
	for _, tt := range []struct {
		name string
		full bool
		want int
	}{{"daily_limit_includes_previous", false, 5}, {"all_remaining", true, 8}} {
		t.Run(tt.name, func(t *testing.T) {
			command, cfg := partnerTestCommand(t)
			cfg.ExtraTasks, cfg.FullExtraTasks = true, tt.full
			fake := &partnerFake{daily: func(context.Context) (*weapi.PartnerTaskResp, error) { return partnerTestDaily(), nil }, extra: func(context.Context) (*weapi.PartnerExtraTaskResp, error) { return partnerTestExtra(10, 2), nil }}
			fake.evaluate = func(_ context.Context, req *weapi.PartnerEvaluateReq) (*weapi.PartnerEvaluateResp, error) {
				if len(fake.reports) != len(fake.evaluations) {
					t.Fatal("评分必须在对应PLAY_END上报后")
				}
				if !req.ExtraResource || req.Score != "4" {
					t.Fatalf("unexpected extra request: %+v", req)
				}
				return &weapi.PartnerEvaluateResp{RespCommon: types.RespCommon[weapi.PartnerEvaluateRespData]{Code: 200, Data: weapi.PartnerEvaluateRespData{EvaluateRes: true}}}, nil
			}
			if err := command.run(context.Background(), fake, cfg); err != nil {
				t.Fatal(err)
			}
			if len(fake.evaluations) != tt.want || len(fake.reports) != tt.want {
				t.Fatalf("evaluations=%d reports=%d want=%d", len(fake.evaluations), len(fake.reports), tt.want)
			}
			for _, report := range fake.reports {
				if report.InteractType != "PLAY_END" || report.ResourceId == "" || report.BizResourceId != "" {
					t.Fatalf("bad report: %+v", report)
				}
			}
		})
	}
}

func TestMusicPartnerRejectsInvalidDailyBeforeWriting(t *testing.T) {
	for _, tt := range []struct {
		name  string
		alter func(*weapi.PartnerTaskResp)
	}{
		{"no_permission", func(r *weapi.PartnerTaskResp) { r.Code = 703 }},
		{"missing_data", func(r *weapi.PartnerTaskResp) { r.Data = weapi.PartnerTaskRespData{} }},
		{"negative_count", func(r *weapi.PartnerTaskResp) { r.Data.Count = -1 }},
		{"completed_exceeds_total", func(r *weapi.PartnerTaskResp) { r.Data.CompletedCount = 3 }},
		{"missing_works", func(r *weapi.PartnerTaskResp) { r.Data.Works = nil }},
		{"invalid_work", func(r *weapi.PartnerTaskResp) { r.Data.Works[1].Work.Id = 0 }},
		{"duplicate_work", func(r *weapi.PartnerTaskResp) { r.Data.Works[1].Work.Id = 1 }},
		{"inconsistent_completed", func(r *weapi.PartnerTaskResp) { r.Data.CompletedCount = 1 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			command, cfg := partnerTestCommand(t)
			response := partnerTestDaily(false, false)
			tt.alter(response)
			fake := &partnerFake{daily: func(context.Context) (*weapi.PartnerTaskResp, error) { return response, nil }}
			if err := command.run(context.Background(), fake, cfg); err == nil {
				t.Fatal("expected invalid response error")
			}
			if len(fake.evaluations) > 0 {
				t.Fatal("invalid daily response caused write")
			}
		})
	}
}

func TestMusicPartnerCancelInterruptsWait(t *testing.T) {
	command, cfg := partnerTestCommand(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.wait = func(ctx context.Context, d time.Duration) error { cancel(); return musicPartnerWait(ctx, d) }
	fake := &partnerFake{daily: func(context.Context) (*weapi.PartnerTaskResp, error) { return partnerTestDaily(false), nil }}
	if err := command.run(ctx, fake, cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if len(fake.evaluations) > 0 {
		t.Fatal("cancelled wait submitted a score")
	}
}

func TestMusicPartnerStopsOnRateLimitAndExpiredLogin(t *testing.T) {
	for _, code := range []int64{301, 401, 403, 429, 460} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			command, cfg := partnerTestCommand(t)
			fake := &partnerFake{daily: func(context.Context) (*weapi.PartnerTaskResp, error) { return partnerTestDaily(false, false), nil }, evaluate: func(context.Context, *weapi.PartnerEvaluateReq) (*weapi.PartnerEvaluateResp, error) {
				return &weapi.PartnerEvaluateResp{RespCommon: types.RespCommon[weapi.PartnerEvaluateRespData]{Code: code}}, nil
			}}
			if err := command.run(context.Background(), fake, cfg); err == nil {
				t.Fatal("expected account stop error")
			}
			if len(fake.evaluations) != 1 {
				t.Fatalf("evaluations=%d", len(fake.evaluations))
			}
		})
	}
}

func TestMusicPartnerResourceUnavailableIsNotSuccess(t *testing.T) {
	command, cfg := partnerTestCommand(t)
	fake := &partnerFake{daily: func(context.Context) (*weapi.PartnerTaskResp, error) { return partnerTestDaily(false, false), nil }}
	fake.evaluate = func(_ context.Context, req *weapi.PartnerEvaluateReq) (*weapi.PartnerEvaluateResp, error) {
		if req.WorkId == "1" {
			return &weapi.PartnerEvaluateResp{RespCommon: types.RespCommon[weapi.PartnerEvaluateRespData]{Code: 405, Message: "资源状态异常"}}, nil
		}
		return &weapi.PartnerEvaluateResp{RespCommon: types.RespCommon[weapi.PartnerEvaluateRespData]{Code: 200, Data: weapi.PartnerEvaluateRespData{EvaluateRes: true}}}, nil
	}
	if err := command.run(context.Background(), fake, cfg); err == nil {
		t.Fatal("resource skip incorrectly reported success")
	}
	if len(fake.evaluations) != 2 {
		t.Fatal("resource skip should allow the next work")
	}
}

func TestMusicPartnerRechecksUncertainScoreOnceWithoutResubmitting(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(fmt.Sprint(confirmed), func(t *testing.T) {
			command, cfg := partnerTestCommand(t)
			reads := 0
			fake := &partnerFake{daily: func(context.Context) (*weapi.PartnerTaskResp, error) {
				reads++
				return partnerTestDaily(reads > 1 && confirmed), nil
			}, evaluate: func(context.Context, *weapi.PartnerEvaluateReq) (*weapi.PartnerEvaluateResp, error) {
				return nil, context.DeadlineExceeded
			}}
			err := command.run(context.Background(), fake, cfg)
			if (err == nil) != confirmed {
				t.Fatalf("confirmed=%v err=%v", confirmed, err)
			}
			if len(fake.evaluations) != 1 || reads != 2 {
				t.Fatalf("writes=%d reads=%d", len(fake.evaluations), reads)
			}
		})
	}
}

func TestMusicPartnerRecheckRejectsChangedTask(t *testing.T) {
	command, cfg := partnerTestCommand(t)
	reads := 0
	fake := &partnerFake{daily: func(context.Context) (*weapi.PartnerTaskResp, error) {
		reads++
		r := partnerTestDaily(reads > 1)
		if reads > 1 {
			r.Data.Id = 11
		}
		return r, nil
	}, evaluate: func(context.Context, *weapi.PartnerEvaluateReq) (*weapi.PartnerEvaluateResp, error) {
		return nil, io.ErrUnexpectedEOF
	}}
	if err := command.run(context.Background(), fake, cfg); err == nil {
		t.Fatal("a different daily task cannot confirm the old write")
	}
	if len(fake.evaluations) != 1 {
		t.Fatal("uncertain request resubmitted")
	}
}

func TestMusicPartnerFailedListenDoesNotScoreThatWork(t *testing.T) {
	command, cfg := partnerTestCommand(t)
	cfg.ExtraTasks = true
	fake := &partnerFake{daily: func(context.Context) (*weapi.PartnerTaskResp, error) { return partnerTestDaily(), nil }, extra: func(context.Context) (*weapi.PartnerExtraTaskResp, error) { return partnerTestExtra(2, 0), nil }, report: func(_ context.Context, req *weapi.PartnerExtraReportReq) (*weapi.PartnerExtraReportResp, error) {
		return &weapi.PartnerExtraReportResp{RespCommon: types.RespCommon[weapi.PartnerExtraReportRespData]{Code: 200, Data: weapi.PartnerExtraReportRespData{InteractResult: req.WorkId != "100"}}}, nil
	}}
	if err := command.run(context.Background(), fake, cfg); err == nil {
		t.Fatal("partial report failure should fail the task")
	}
	if len(fake.evaluations) != 1 || fake.evaluations[0].WorkId != "101" {
		t.Fatalf("evaluations=%+v", fake.evaluations)
	}
}

func TestMusicPartnerNetworkConfigIsIsolatedAndDoesNotRetryWrites(t *testing.T) {
	original := &api.Config{Debug: true, Retry: 9, Timeout: 0}
	original.Cookie.Filepath = "original.json"
	a, err := musicPartnerNetworkConfig(original, "account-a.json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := musicPartnerNetworkConfig(original, "account-b.json")
	if err != nil {
		t.Fatal(err)
	}
	if a.Cookie.Filepath == b.Cookie.Filepath || original.Cookie.Filepath != "original.json" {
		t.Fatal("account cookie path leaked")
	}
	if a.Retry != 0 || a.Debug || a.Timeout <= 0 || original.Retry != 9 || !original.Debug {
		t.Fatal("unsafe or mutating network config")
	}
}

func TestMusicPartnerInvalidExtraListNeverWrites(t *testing.T) {
	for _, tt := range []struct {
		name  string
		alter func(*weapi.PartnerExtraTaskResp)
	}{
		{"missing_data", func(r *weapi.PartnerExtraTaskResp) { r.Data = nil }},
		{"missing_resource", func(r *weapi.PartnerExtraTaskResp) { r.Data[0].Work.ResourceId = 0 }},
		{"duplicate_work", func(r *weapi.PartnerExtraTaskResp) { r.Data[1].Work.Id = r.Data[0].Work.Id }},
		{"no_permission", func(r *weapi.PartnerExtraTaskResp) { r.Code = 703 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			command, cfg := partnerTestCommand(t)
			cfg.ExtraTasks = true
			list := partnerTestExtra(2, 0)
			tt.alter(list)
			fake := &partnerFake{daily: func(context.Context) (*weapi.PartnerTaskResp, error) { return partnerTestDaily(), nil }, extra: func(context.Context) (*weapi.PartnerExtraTaskResp, error) { return list, nil }}
			if err := command.run(context.Background(), fake, cfg); err == nil {
				t.Fatal("invalid extra response accepted")
			}
			if len(fake.evaluations) != 0 || len(fake.reports) != 0 {
				t.Fatal("invalid extra response caused writes")
			}
		})
	}
}

func TestMusicPartnerExtraUncertainResultRechecksWithoutReplay(t *testing.T) {
	command, cfg := partnerTestCommand(t)
	cfg.ExtraTasks = true
	reads := 0
	fake := &partnerFake{daily: func(context.Context) (*weapi.PartnerTaskResp, error) { return partnerTestDaily(), nil }, extra: func(context.Context) (*weapi.PartnerExtraTaskResp, error) {
		reads++
		if reads > 1 {
			return partnerTestExtra(1, 1), nil
		}
		return partnerTestExtra(1, 0), nil
	}, evaluate: func(context.Context, *weapi.PartnerEvaluateReq) (*weapi.PartnerEvaluateResp, error) {
		return nil, io.ErrUnexpectedEOF
	}}
	if err := command.run(context.Background(), fake, cfg); err != nil {
		t.Fatal(err)
	}
	if len(fake.evaluations) != 1 || len(fake.reports) != 1 || reads != 2 {
		t.Fatalf("writes=%d reports=%d reads=%d", len(fake.evaluations), len(fake.reports), reads)
	}
}

func TestMusicPartnerDoesNotAcceptFalseEvaluationSuccess(t *testing.T) {
	command, cfg := partnerTestCommand(t)
	fake := &partnerFake{daily: func(context.Context) (*weapi.PartnerTaskResp, error) { return partnerTestDaily(false), nil }, evaluate: func(context.Context, *weapi.PartnerEvaluateReq) (*weapi.PartnerEvaluateResp, error) {
		return &weapi.PartnerEvaluateResp{RespCommon: types.RespCommon[weapi.PartnerEvaluateRespData]{Code: 200}}, nil
	}}
	if err := command.run(context.Background(), fake, cfg); err == nil {
		t.Fatal("missing evaluateRes incorrectly marked success")
	}
	if len(fake.evaluations) != 1 {
		t.Fatal("ambiguous write repeated")
	}
}

type musicPartnerTestTransport func(*http.Request) (*http.Response, error)

func (f musicPartnerTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestMusicPartnerExecuteKeepsCookieSessionsSeparate(t *testing.T) {
	command, _ := partnerTestCommand(t)
	network := &api.Config{Retry: 5, Debug: true}
	network.Cookie.Filepath = "untouched.json"
	command.root.Cfg.Network = network
	host, _ := url.Parse("https://music.163.com/")
	files := []string{filepath.Join(t.TempDir(), "first.json"), filepath.Join(t.TempDir(), "second.json")}
	for i, path := range files {
		seedCfg := &api.Config{}
		seedCfg.Cookie.Filepath = path
		seed, err := api.NewClient(seedCfg, log.Default)
		if err != nil {
			t.Fatal(err)
		}
		seed.SetCookies(host, []*http.Cookie{{Name: "MUSIC_U", Value: fmt.Sprintf("test-session-%d", i), Path: "/"}, {Name: "__csrf", Value: fmt.Sprintf("test-csrf-%d", i), Path: "/"}})
		if err := seed.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	seen := 0
	command.newClient = func(cfg *api.Config, l *log.Logger) (*api.Client, error) {
		client, err := api.NewClient(cfg, l)
		if err != nil {
			return nil, err
		}
		index := 0
		if cfg.Cookie.Filepath == files[1] {
			index = 1
		}
		client.GetClient().Transport = musicPartnerTestTransport(func(req *http.Request) (*http.Response, error) {
			seen++
			cookie, err := req.Cookie("MUSIC_U")
			if err != nil || cookie.Value != fmt.Sprintf("test-session-%d", index) {
				t.Error("wrong account cookie sent")
			}
			if req.URL.Query().Get("csrf_token") != fmt.Sprintf("test-csrf-%d", index) {
				t.Error("host-only CSRF not propagated")
			}
			if req.URL.Path != "/weapi/music/partner/daily/task/get" {
				t.Fatalf("unexpected request: %s", req.URL.Path)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":200,"data":{"id":10,"count":0,"completedCount":0,"works":[]}}`)), Request: req}, nil
		})
		return client, nil
	}
	for _, path := range files {
		if err := command.ExecuteForCookie(context.Background(), path); err != nil {
			t.Fatal(err)
		}
	}
	if seen != 2 || network.Cookie.Filepath != "untouched.json" || network.Retry != 5 || !network.Debug {
		t.Fatal("shared config or request count changed")
	}
}

func TestMusicPartnerCancellationStopsPendingHTTP(t *testing.T) {
	command, cfg := partnerTestCommand(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	fake := &partnerFake{daily: func(ctx context.Context) (*weapi.PartnerTaskResp, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	done := make(chan error, 1)
	go func() { done <- command.run(ctx, fake, cfg) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP cancellation was not propagated")
	}
}

func TestMusicPartnerSkippedExtraDoesNotConsumeSuccessLimit(t *testing.T) {
	command, cfg := partnerTestCommand(t)
	cfg.ExtraTasks = true
	cfg.ExtraLimit = 1
	fake := &partnerFake{daily: func(context.Context) (*weapi.PartnerTaskResp, error) { return partnerTestDaily(), nil }, extra: func(context.Context) (*weapi.PartnerExtraTaskResp, error) { return partnerTestExtra(3, 0), nil }}
	fake.evaluate = func(_ context.Context, req *weapi.PartnerEvaluateReq) (*weapi.PartnerEvaluateResp, error) {
		if req.WorkId == "100" {
			return &weapi.PartnerEvaluateResp{RespCommon: types.RespCommon[weapi.PartnerEvaluateRespData]{Code: 405, Message: "资源状态异常"}}, nil
		}
		return &weapi.PartnerEvaluateResp{RespCommon: types.RespCommon[weapi.PartnerEvaluateRespData]{Code: 200, Data: weapi.PartnerEvaluateRespData{EvaluateRes: true}}}, nil
	}
	if err := command.run(context.Background(), fake, cfg); err == nil {
		t.Fatal("partial failure should be returned")
	}
	if len(fake.evaluations) != 2 || len(fake.reports) != 2 {
		t.Fatalf("expected skipped work plus one success, got %d scores/%d reports", len(fake.evaluations), len(fake.reports))
	}
}

func TestMusicPartnerEmptyReportFailureReasonIsAllowed(t *testing.T) {
	for _, tt := range []struct {
		reason any
		want   bool
	}{{nil, false}, {"", false}, {"  ", false}, {"denied", true}, {true, true}} {
		if got := musicPartnerReportFailed(tt.reason); got != tt.want {
			t.Fatalf("reason type %T: got %v want %v", tt.reason, got, tt.want)
		}
	}
}

func TestMusicPartnerHTTPRejectionStopsWithoutRecheck(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			command, cfg := partnerTestCommand(t)
			reads := 0
			fake := &partnerFake{daily: func(context.Context) (*weapi.PartnerTaskResp, error) {
				reads++
				return partnerTestDaily(false, false), nil
			}, evaluate: func(context.Context, *weapi.PartnerEvaluateReq) (*weapi.PartnerEvaluateResp, error) {
				return nil, &api.HTTPStatusError{StatusCode: status}
			}}
			if err := command.run(context.Background(), fake, cfg); err == nil {
				t.Fatal("HTTP rejection accepted")
			}
			if reads != 1 || len(fake.evaluations) != 1 {
				t.Fatalf("HTTP rejection caused extra requests: reads=%d writes=%d", reads, len(fake.evaluations))
			}
		})
	}
}

package ncmm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/types"
	"github.com/3899/ncmm/api/weapi"
	cookiepkg "github.com/3899/ncmm/pkg/cookie"
)

type recommendFake struct {
	items                            []weapi.PartnerRecommendResource
	notes, comments, audio, prepared int
	points                           int64
	unknownNote                      bool
	noteErr                          error
	reject                           string
	reports                          []weapi.PartnerExtraReportReq
	dailyCalls                       int
}

func (f *recommendFake) PartnerDailyTask(context.Context, *weapi.PartnerTaskReq) (*weapi.PartnerTaskResp, error) {
	f.dailyCalls++
	return &weapi.PartnerTaskResp{RespCommon: types.RespCommon[weapi.PartnerTaskRespData]{Code: 200, Data: weapi.PartnerTaskRespData{Id: 1, CanInteract: true, CurRcmdScore: f.points, RecResources: append([]weapi.PartnerRecommendResource{}, f.items...)}}}, nil
}
func (f *recommendFake) PartnerRecommendResources(context.Context) (*weapi.PartnerRecommendResourcesResp, error) {
	return &weapi.PartnerRecommendResourcesResp{RespCommon: types.RespCommon[weapi.PartnerRecommendResourcesData]{Code: 200, Data: weapi.PartnerRecommendResourcesData{Resources: append([]weapi.PartnerRecommendResource{}, f.items...)}}}, nil
}
func (f *recommendFake) PrepareAudio(context.Context, weapi.PartnerWork) error { f.audio++; return nil }
func (f *recommendFake) PrepareNote(_ context.Context, item weapi.PartnerRecommendResource, content string) (*eapi.PartnerNotePublishReq, error) {
	f.prepared++
	return &eapi.PartnerNotePublishReq{SongID: item.Work.ResourceId, Content: content}, nil
}
func (f *recommendFake) PublishNote(_ context.Context, req *eapi.PartnerNotePublishReq) (int64, error) {
	f.notes++
	if f.noteErr != nil {
		return 0, f.noteErr
	}
	if f.unknownNote {
		return 0, errors.New("response lost")
	}
	return req.SongID + 1000, nil
}
func (f *recommendFake) PublishComment(_ context.Context, item weapi.PartnerRecommendResource, _ string) (int64, error) {
	f.comments++
	return item.Work.ResourceId + 2000, nil
}
func (f *recommendFake) PartnerExtraReport(_ context.Context, req *weapi.PartnerExtraReportReq) (*weapi.PartnerExtraReportResp, error) {
	f.reports = append(f.reports, *req)
	ok := req.InteractType != f.reject
	if ok {
		for i := range f.items {
			if strconv.FormatInt(f.items[i].Work.Id, 10) != req.WorkId {
				continue
			}
			switch req.InteractType {
			case "PUBLISH_EVENT":
				if !f.items[i].PublishEvent {
					f.points += 2
				}
				f.items[i].PublishEvent = true
			case "PUBLISH_COMMENT":
				if !f.items[i].PublishComment {
					f.points++
				}
				f.items[i].PublishComment = true
			}
		}
	}
	return &weapi.PartnerExtraReportResp{RespCommon: types.RespCommon[weapi.PartnerExtraReportRespData]{Code: 200, Data: weapi.PartnerExtraReportRespData{InteractResult: ok, FailedReason: "发布成功，积分已增加"}}}, nil
}
func recommendFixture(count int) *recommendFake {
	f := &recommendFake{}
	for i := 1; i <= count; i++ {
		f.items = append(f.items, weapi.PartnerRecommendResource{Work: weapi.PartnerWork{Id: int64(i), ResourceId: int64(i + 100), Name: "中文歌曲", AuthorName: "作者"}, CanInteract: true, TotalTaskNum: 3, FinishTaskNum: 2})
	}
	return f
}

func TestPartnerRecommendSuccessResumeAndDailyCap(t *testing.T) {
	c, cfg := partnerTestCommand(t)
	cfg.RecommendCountMin, cfg.RecommendCountMax = 2, 2
	cfg.RecommendWaitMin = 16
	cfg.RecommendWaitMax = 16
	cfg.RecommendNotes = []string{"推荐{name}"}
	cfg.RecommendComments = []string{"喜欢{artist}的作品"}
	f := recommendFixture(5)
	f.items[0].PublishEvent = true
	f.items[0].PublishComment = true
	f.points = 3
	path := filepath.Join(t.TempDir(), "state.json")
	if err := c.recommend(context.Background(), f, cfg, path); err != nil {
		t.Fatal(err)
	}
	if f.notes != 1 || f.comments != 1 || f.audio != 1 || f.points != 6 {
		t.Fatalf("wrong daily delta: %+v", f)
	}
	if len(f.reports) != 3 || f.reports[1].BizResourceId != "1102" || f.reports[2].BizResourceId != "2102" {
		t.Fatal("must report actual returned IDs")
	}
	if err := c.recommend(context.Background(), f, cfg, path); err != nil {
		t.Fatal(err)
	}
	if f.notes != 1 || f.comments != 1 {
		t.Fatal("server-completed work was republished")
	}
	j, err := loadPartnerRecommendJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Works) != 1 || j.Works["2"].Note.CreatedAt.IsZero() || j.Works["2"].Comment.CreatedAt.IsZero() {
		t.Fatal("missing durable receipt timestamp")
	}
}

func TestPartnerRecommendReportRetryUsesReceipt(t *testing.T) {
	c, cfg := partnerTestCommand(t)
	cfg.RecommendCountMin, cfg.RecommendCountMax = 1, 1
	cfg.RecommendWaitMin = 16
	cfg.RecommendWaitMax = 16
	cfg.RecommendNotes = []string{"笔记"}
	cfg.RecommendComments = []string{"乐评"}
	f := recommendFixture(2)
	f.reject = "PUBLISH_EVENT"
	path := filepath.Join(t.TempDir(), "state.json")
	if err := c.recommend(context.Background(), f, cfg, path); err == nil {
		t.Fatal("unconfirmed report must fail")
	}
	f.reject = ""
	if err := c.recommend(context.Background(), f, cfg, path); err != nil {
		t.Fatal(err)
	}
	if f.notes != 1 || f.comments != 1 || f.audio != 1 {
		t.Fatal("resuming must only report the existing IDs")
	}
}

func TestPartnerRecommendUnknownPublishNeverRepeated(t *testing.T) {
	c, cfg := partnerTestCommand(t)
	cfg.RecommendCountMin, cfg.RecommendCountMax = 1, 1
	cfg.RecommendWaitMin = 16
	cfg.RecommendWaitMax = 16
	cfg.RecommendNotes = []string{"笔记"}
	cfg.RecommendComments = []string{"乐评"}
	f := recommendFixture(2)
	f.unknownNote = true
	path := filepath.Join(t.TempDir(), "state.json")
	for range 2 {
		if err := c.recommend(context.Background(), f, cfg, path); err == nil {
			t.Fatal("unknown publication must be visible as failure")
		}
	}
	if f.notes != 1 || f.comments != 1 {
		t.Fatal("unknown write was retried or daily cap exceeded")
	}
	// 用户在 App 确认成功且服务端更新标记后，可以恢复，不依赖虚构 ID。
	f.items[0].PublishEvent = true
	if err := c.recommend(context.Background(), f, cfg, path); err != nil {
		t.Fatal(err)
	}
}

func TestPartnerRecommendWaitAndPlayReportGate(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			c, cfg := partnerTestCommand(t)
			cfg.RecommendCountMin, cfg.RecommendCountMax = 1, 1
			cfg.RecommendWaitMin = 16
			cfg.RecommendWaitMax = 20
			cfg.RecommendNotes = []string{"笔记"}
			cfg.RecommendComments = []string{"乐评"}
			f := recommendFixture(1)
			f.reject = "PLAY_END"
			c.wait = func(ctx context.Context, d time.Duration) error {
				if d < 16*time.Second || d > 20*time.Second {
					t.Fatal("invalid wait")
				}
				if cancelled {
					return context.Canceled
				}
				return nil
			}
			if err := c.recommend(context.Background(), f, cfg, filepath.Join(t.TempDir(), "state.json")); err == nil {
				t.Fatal("must fail before publishing")
			}
			if f.notes != 0 || f.comments != 0 || f.prepared != 0 {
				t.Fatal("must not publish before wait and positive play report")
			}
		})
	}
}

func TestPartnerRecommendDisabledAndTextLimits(t *testing.T) {
	c, cfg := partnerTestCommand(t)
	f := recommendFixture(1)
	if err := c.recommend(context.Background(), f, cfg, filepath.Join(t.TempDir(), "state.json")); err != nil || f.dailyCalls != 0 {
		t.Fatal("disabled must not make requests")
	}
	work := weapi.PartnerWork{Name: strings.Repeat("歌", 500), AuthorName: "作者"}
	if _, err := partnerRecommendText([]string{"{name}额外"}, work); err == nil {
		t.Fatal("expanded length must be checked")
	}
}

func TestPartnerRecommendMediaURLPolicy(t *testing.T) {
	for _, raw := range []string{"https://127.0.0.1/a", "https://p1.music.126.net.evil.test/a", "https://user:pass@p1.music.126.net/a", "http://p1.music.126.net/a", "https://p1.music.126.net:9000/a"} {
		u, _ := url.Parse(raw)
		if neteaseMediaURL(u) {
			t.Fatalf("unsafe media URL accepted: %s", raw)
		}
	}
	for _, raw := range []string{"https://p1.music.126.net/a", "https://m801.music.126.net/a", "https://music.163.com/a"} {
		u, _ := url.Parse(raw)
		if !neteaseMediaURL(u) {
			t.Fatal("NetEase media rejected")
		}
	}
}

func TestPartnerRecommendAccountBlockSurvivesJoinedErrors(t *testing.T) {
	err := errors.Join(musicPartnerResponseError("普通失败", 500, ""), partnerRecommendRequestError(context.Background(), "发布笔记", &api.HTTPStatusError{StatusCode: 403, ServiceCode: 301}))
	if !musicPartnerMustStop(context.Background(), err) || !errors.Is(err, errMusicPartnerAccountBlocked) {
		t.Fatal("account block hidden by earlier partial failure")
	}
	err = partnerRecommendRequestError(context.Background(), "发布笔记", &url.Error{URL: "https://invalid/?secret=never-log", Err: context.DeadlineExceeded})
	if err != context.DeadlineExceeded || strings.Contains(err.Error(), "secret") {
		t.Fatal("cancellation must survive without leaking URL")
	}
}

func TestPartnerRecommendPartialSongsRespectRemainingTarget(t *testing.T) {
	c, cfg := partnerTestCommand(t)
	cfg.RecommendCountMin, cfg.RecommendCountMax = 3, 3
	cfg.RecommendWaitMin = 16
	cfg.RecommendWaitMax = 16
	cfg.RecommendNotes = []string{"笔记"}
	cfg.RecommendComments = []string{"乐评"}
	f := recommendFixture(5)
	for i := 0; i < 4; i++ {
		f.items[i].PublishEvent = true
		f.items[i].PublishComment = i < 2
	}
	if err := c.recommend(context.Background(), f, cfg, filepath.Join(t.TempDir(), "state.json")); err != nil {
		t.Fatal(err)
	}
	if f.notes != 0 || f.comments != 1 || f.items[3].PublishComment {
		t.Fatal("partial songs exceeded remaining target")
	}
}

func TestPartnerRecommendSkipsUnavailableSong(t *testing.T) {
	c, cfg := partnerTestCommand(t)
	cfg.RecommendCountMin, cfg.RecommendCountMax = 1, 1
	cfg.RecommendWaitMin = 16
	cfg.RecommendWaitMax = 16
	cfg.RecommendNotes = []string{"笔记"}
	cfg.RecommendComments = []string{"乐评"}
	f := recommendFixture(2)
	f.items[0].CanInteract = false
	path := filepath.Join(t.TempDir(), "state.json")
	if err := c.recommend(context.Background(), f, cfg, path); err != nil {
		t.Fatal(err)
	}
	journal, err := loadPartnerRecommendJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.notes != 1 || f.comments != 1 || f.items[0].PublishEvent || f.items[0].PublishComment || journal.Works["1"] != nil || journal.Works["2"] == nil {
		t.Fatal("must only publish and record the interactable song")
	}
}

func TestPartnerRecommendMergeKeepsInteractionRestriction(t *testing.T) {
	for _, listAllows := range []bool{false, true} {
		t.Run(fmt.Sprint(listAllows), func(t *testing.T) {
			list := recommendFixture(1).items
			daily := recommendFixture(1).items
			list[0].CanInteract = listAllows
			daily[0].CanInteract = !listAllows
			list[0].PublishEvent = true
			daily[0].PublishComment = true
			merged := mergePartnerRecommendations(list, daily)
			if len(merged) != 1 || merged[0].CanInteract || !merged[0].PublishEvent || !merged[0].PublishComment {
				t.Fatal("must preserve either source's restriction and both completion flags")
			}
		})
	}
}

func TestPartnerRecommendPreparationFailureCanResume(t *testing.T) {
	for _, failure := range []*eapi.PartnerPublishError{
		{Phase: "协议配置", Mode: api.CryptoModeXEAPI, NotSent: true},
		{Phase: "业务拒绝", Mode: api.CryptoModeEAPI, Rejected: true, HTTPStatus: 200, ServiceCode: 400},
	} {
		t.Run(failure.Phase, func(t *testing.T) {
			c, cfg := partnerTestCommand(t)
			cfg.RecommendCountMin, cfg.RecommendCountMax = 1, 1
			cfg.RecommendWaitMin, cfg.RecommendWaitMax = 16, 16
			cfg.RecommendNotes, cfg.RecommendComments = []string{"笔记"}, []string{"乐评"}
			f := recommendFixture(1)
			f.noteErr = failure
			path := filepath.Join(t.TempDir(), "state.json")
			if err := c.recommend(context.Background(), f, cfg, path); err == nil {
				t.Fatal("the failed publication must be reported")
			}
			journal, err := loadPartnerRecommendJournal(path)
			if err != nil || journal.Works["1"].Note.Attempted || journal.Works["1"].Note.ID != 0 {
				t.Fatal("an unsent or explicitly rejected publication must remain resumable")
			}
			f.noteErr = nil
			if err := c.recommend(context.Background(), f, cfg, path); err != nil {
				t.Fatal(err)
			}
			if f.notes != 2 || f.comments != 1 {
				t.Fatal("must resume the note and retain the published comment")
			}
		})
	}
}

func TestPartnerRecommendPublishModeMatchesMobileSession(t *testing.T) {
	for _, tc := range []struct {
		name, os, eapiUA, xeapiUA string
		want                      api.CryptoMode
	}{
		{name: "current iPhone config", eapiUA: "NeteaseMusic (iPhone; iOS 16.6.1)", want: api.CryptoModeEAPI},
		{name: "iPhone cookie preferred", os: "iPhone OS", eapiUA: "NeteaseMusic (iPhone)", xeapiUA: "NeteaseMusic (Android)", want: api.CryptoModeEAPI},
		{name: "Android", os: "android", xeapiUA: "NeteaseMusic (Android)", want: api.CryptoModeXEAPI},
		{name: "Android missing UA", os: "android", eapiUA: "NeteaseMusic (iPhone)"},
		{name: "unknown platform"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			network := &api.Config{UserAgent: api.UserAgentConfig{EAPI: tc.eapiUA, XEAPI: tc.xeapiUA}, Cookie: cookiepkg.Config{Filepath: filepath.Join(t.TempDir(), "anonymous.json")}}
			client, err := api.NewClient(network, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close(context.Background())
			if tc.os != "" {
				u, _ := url.Parse("https://music.163.com")
				client.SetCookies(u, []*http.Cookie{{Name: "os", Value: tc.os, Path: "/"}})
			}
			mode, err := partnerRecommendPublishMode(client, network)
			if mode != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("unexpected publication protocol: mode=%s err=%v", mode, err)
			}
		})
	}
}

func TestPartnerRecommendKeepsSafeUploadDiagnostic(t *testing.T) {
	original := &eapi.ImageUploadError{Stage: "上传文件", Kind: "HTTP拒绝", HTTPStatus: 400}
	err := partnerRecommendRequestError(context.Background(), "上传歌曲封面", original)
	var detail *eapi.ImageUploadError
	if !errors.As(err, &detail) || detail != original || !strings.Contains(err.Error(), "HTTP=400") || !strings.Contains(err.Error(), "阶段=上传文件") {
		t.Fatal("upload stage and numeric status must survive the runtime boundary")
	}
}

func TestPartnerRecommendCoverUsesBoundedCDNSize(t *testing.T) {
	raw, err := partnerRecommendCoverURL("http://p1.music.126.net/album.jpg?param=5044y5044&quality=90")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	if u.Scheme != "https" || u.Path != "/album.jpg" || u.Query().Get("param") != "1080y1080" || u.Query().Get("quality") != "90" {
		t.Fatal("must request a suitable CDN cover size while preserving other parameters")
	}
	for _, raw := range []string{"https://evil.test/cover.jpg", "https://p1.music.126.net.evil.test/cover.jpg", "https://user:pass@p1.music.126.net/cover.jpg"} {
		if _, err := partnerRecommendCoverURL(raw); err == nil {
			t.Fatal("untrusted cover address accepted")
		}
	}
}

func TestPartnerRecommendDailyTargetIsStableAndBounded(t *testing.T) {
	_, cfg := partnerTestCommand(t)
	cfg.RecommendCountMin, cfg.RecommendCountMax = 5, 10
	for day := 1; day <= 31; day++ {
		name := fmt.Sprintf("123-2026-10-%02d.json", day)
		target := partnerRecommendDailyTarget(filepath.Join("first-home", name), cfg)
		if target < 5 || target > 10 || target != partnerRecommendDailyTarget(filepath.Join("second-home", name), cfg) {
			t.Fatal("daily target must stay within the range and survive moving home")
		}
	}
	cfg.RecommendCountMin, cfg.RecommendCountMax = 0, int(^uint(0)>>1)
	if target := partnerRecommendDailyTarget("123-2026-10-23.json", cfg); target < 0 {
		t.Fatal("large nonnegative range overflowed")
	}
}

func TestPartnerRecommendSupportsMoreThanThreeAndOrder(t *testing.T) {
	for _, order := range []string{"sequential", "random"} {
		t.Run(order, func(t *testing.T) {
			var first []string
			for run := 0; run < 2; run++ {
				c, cfg := partnerTestCommand(t)
				cfg.RecommendCountMin, cfg.RecommendCountMax = 5, 5
				cfg.RecommendOrder = order
				cfg.RecommendWaitMin, cfg.RecommendWaitMax = 16, 16
				cfg.RecommendNotes, cfg.RecommendComments = []string{"笔记"}, []string{"乐评"}
				f := recommendFixture(20)
				if run == 1 && order == "random" {
					for i, j := 0, len(f.items)-1; i < j; i, j = i+1, j-1 {
						f.items[i], f.items[j] = f.items[j], f.items[i]
					}
				}
				path := filepath.Join(t.TempDir(), "123-2026-10-23.json")
				if err := c.recommend(context.Background(), f, cfg, path); err != nil {
					t.Fatal(err)
				}
				journal, err := loadPartnerRecommendJournal(path)
				if err != nil || len(journal.Works) != 5 || f.notes != 5 || f.comments != 5 {
					t.Fatal("more than three songs must be supported")
				}
				var selected []string
				for _, report := range f.reports {
					if report.InteractType == "PUBLISH_EVENT" {
						selected = append(selected, report.WorkId)
					}
				}
				if order == "sequential" && strings.Join(selected, ",") != "1,2,3,4,5" {
					t.Fatal("sequential selection must follow the list")
				}
				if run == 0 {
					first = selected
				} else if strings.Join(first, ",") != strings.Join(selected, ",") {
					t.Fatal("the same candidates must retain their daily random order")
				}
				if err := c.recommend(context.Background(), f, cfg, path); err != nil || f.notes != 5 || f.comments != 5 {
					t.Fatal("completed songs must not be republished")
				}
			}
		})
	}
}

func TestPartnerRecommendResponseHintFiltersPrivateFields(t *testing.T) {
	hint := partnerRecommendResponseHint("参数错误", "", json.RawMessage(`{"message":"检查字段，token=private-value https://invalid.test/?key=private-value","cookie":"private-cookie","unknown":"private-other"}`))
	if !strings.Contains(hint, "检查字段") || strings.Contains(hint, "private-") || strings.Contains(hint, "invalid.test") {
		t.Fatal("response hints must retain useful reasons and omit private fields")
	}
}

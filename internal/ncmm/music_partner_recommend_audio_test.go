package ncmm

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api/weapi"
)

type recommendAudioFake struct {
	*recommendFake
	fail map[int64]error
	ids  []int64
}

func (f *recommendAudioFake) PrepareAudio(ctx context.Context, work weapi.PartnerWork) error {
	f.audio++
	f.ids = append(f.ids, work.Id)
	return f.fail[work.Id]
}

func TestPartnerRecommendAudioFailureAllowsNextCandidate(t *testing.T) {
	c, cfg := partnerTestCommand(t)
	cfg.RecommendCountMin, cfg.RecommendCountMax = 1, 1
	cfg.RecommendNotes, cfg.RecommendComments = []string{"笔记"}, []string{"乐评"}
	f := &recommendAudioFake{recommendFake: recommendFixture(3), fail: map[int64]error{1: fmt.Errorf("获取媒体失败（CDN=m804.music.126.net，分类=网络请求超时，HTTP=0，尝试=2/2）")}}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := c.recommend(context.Background(), f, cfg, path); err != nil {
		t.Fatalf("audio-only failure blocked usable replacement: %v", err)
	}
	if fmt.Sprint(f.ids) != "[1 2]" || f.notes != 1 || f.comments != 1 || f.points != 3 {
		t.Fatalf("wrong replacement or exceeded target: ids=%v notes=%d comments=%d points=%d", f.ids, f.notes, f.comments, f.points)
	}
	for _, report := range f.reports {
		if report.WorkId != "2" {
			t.Fatal("failed audio must not generate playback or publish reports")
		}
	}
	if err := c.recommend(context.Background(), f, cfg, path); err != nil || f.notes != 1 || f.comments != 1 || len(f.ids) != 2 {
		t.Fatalf("completed replacement was repeated: %v", err)
	}
}

func TestPartnerRecommendOldAudioPlaceholderDoesNotReserveTarget(t *testing.T) {
	c, cfg := partnerTestCommand(t)
	cfg.RecommendCountMin, cfg.RecommendCountMax = 1, 1
	cfg.RecommendNotes, cfg.RecommendComments = []string{"笔记"}, []string{"乐评"}
	f := recommendFixture(2)
	f.items[0].CanInteract = false
	path := filepath.Join(t.TempDir(), "state.json")
	journal := &partnerRecommendJournal{Version: 1, Works: map[string]*partnerRecommendReceipt{"1": {Name: "旧音频超时记录", ResourceID: 101}}}
	if err := savePartnerRecommendJournal(path, journal); err != nil {
		t.Fatal(err)
	}
	if err := c.recommend(context.Background(), f, cfg, path); err != nil || f.notes != 1 || f.comments != 1 {
		t.Fatalf("empty old receipt blocked new candidate: err=%v notes=%d comments=%d", err, f.notes, f.comments)
	}
}

func TestPartnerRecommendAllAudioFailuresRemainErrors(t *testing.T) {
	c, cfg := partnerTestCommand(t)
	cfg.RecommendCountMin, cfg.RecommendCountMax = 1, 1
	cfg.RecommendNotes, cfg.RecommendComments = []string{"笔记"}, []string{"乐评"}
	f := &recommendAudioFake{recommendFake: recommendFixture(2), fail: map[int64]error{1: fmt.Errorf("m804 音频超时"), 2: fmt.Errorf("m805 音频超时")}}
	err := c.recommend(context.Background(), f, cfg, filepath.Join(t.TempDir(), "state.json"))
	if err == nil || !strings.Contains(err.Error(), "m804") || !strings.Contains(err.Error(), "m805") || !strings.Contains(err.Error(), "每日目标") {
		t.Fatalf("all-unavailable result lost network diagnosis: %v", err)
	}
	if len(f.ids) != 2 || f.notes != 0 || f.comments != 0 || len(f.reports) != 0 {
		t.Fatalf("audio failure caused publication, fake reports, or repeated candidates: %+v", f)
	}
}

func TestPartnerRecommendAudioFailurePreservesUnknownPublicationSlot(t *testing.T) {
	c, cfg := partnerTestCommand(t)
	cfg.RecommendCountMin, cfg.RecommendCountMax = 1, 1
	cfg.RecommendNotes, cfg.RecommendComments = []string{"笔记"}, []string{"乐评"}
	f := &recommendAudioFake{recommendFake: recommendFixture(2), fail: map[int64]error{1: fmt.Errorf("音频超时")}}
	path := filepath.Join(t.TempDir(), "state.json")
	journal := &partnerRecommendJournal{Version: 1, Works: map[string]*partnerRecommendReceipt{"1": {ResourceID: 101, Note: partnerPublishReceipt{Attempted: true}}}}
	if err := savePartnerRecommendJournal(path, journal); err != nil {
		t.Fatal(err)
	}
	if err := c.recommend(context.Background(), f, cfg, path); err == nil {
		t.Fatal("unknown earlier note cannot be counted as completed")
	}
	loaded, err := loadPartnerRecommendJournal(path)
	if err != nil || !loaded.Works["1"].Note.Attempted || fmt.Sprint(f.ids) != "[1]" || f.notes != 0 || f.comments != 0 {
		t.Fatal("audio failure must not discard an unknown publication or publish an extra song")
	}
}

func TestPartnerRecommendAudioCancellationOrAccountBlockStops(t *testing.T) {
	for _, failure := range []error{context.Canceled, musicPartnerResponseError("音频", 403, "")} {
		c, cfg := partnerTestCommand(t)
		cfg.RecommendCountMin, cfg.RecommendCountMax = 1, 1
		cfg.RecommendNotes, cfg.RecommendComments = []string{"笔记"}, []string{"乐评"}
		f := &recommendAudioFake{recommendFake: recommendFixture(2), fail: map[int64]error{1: failure}}
		err := c.recommend(context.Background(), f, cfg, filepath.Join(t.TempDir(), "state.json"))
		if !errors.Is(err, failure) || len(f.ids) != 1 || f.notes != 0 || len(f.reports) != 0 {
			t.Fatalf("stop condition was hidden by fallback: err=%v ids=%v", err, f.ids)
		}
	}
}

func TestPartnerRecommendAudioFailureBudget(t *testing.T) {
	c, cfg := partnerTestCommand(t)
	cfg.RecommendCountMin, cfg.RecommendCountMax = 1, 1
	cfg.RecommendNotes, cfg.RecommendComments = []string{"笔记"}, []string{"乐评"}
	f := &recommendAudioFake{recommendFake: recommendFixture(20), fail: make(map[int64]error)}
	for i := int64(1); i <= 20; i++ {
		f.fail[i] = fmt.Errorf("CDN 超时")
	}
	if err := c.recommend(context.Background(), f, cfg, filepath.Join(t.TempDir(), "state.json")); err == nil {
		t.Fatal("unavailable network must remain a failure")
	}
	if len(f.ids) != 3 || f.notes != 0 || len(f.reports) != 0 {
		t.Fatalf("persistent outage must stop after three consecutive failed candidates: %v", f.ids)
	}
}

type recommendFinalCancelFake struct {
	*recommendFake
	cancel context.CancelFunc
	lists  int
}

func (f *recommendFinalCancelFake) PartnerRecommendResources(ctx context.Context) (*weapi.PartnerRecommendResourcesResp, error) {
	f.lists++
	if f.lists == 2 {
		f.cancel()
		return nil, ctx.Err()
	}
	return f.recommendFake.PartnerRecommendResources(ctx)
}

func TestPartnerRecommendFinalRefreshPreservesCancellation(t *testing.T) {
	c, cfg := partnerTestCommand(t)
	cfg.RecommendCountMin, cfg.RecommendCountMax = 1, 1
	cfg.RecommendNotes, cfg.RecommendComments = []string{"笔记"}, []string{"乐评"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &recommendFinalCancelFake{recommendFake: recommendFixture(1), cancel: cancel}
	if err := c.recommend(ctx, f, cfg, filepath.Join(t.TempDir(), "state.json")); !errors.Is(err, context.Canceled) {
		t.Fatalf("final refresh swallowed cancellation: %v", err)
	}
}

package ncmm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/internal/filelock"
)

type fakeFansComments struct {
	adds, deletes int
	deleteCode    int
	createCode    int
	createErr     error
	cancel        context.CancelFunc
}

func TestFansGroupCommentCorruptForeignAndLockedRecordsBlockWrites(t *testing.T) {
	for _, kind := range []string{"corrupt", "foreign", "locked"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "123.json")
			if kind == "corrupt" {
				_ = os.WriteFile(path, []byte("{"), 0600)
			}
			if kind == "foreign" {
				if err := saveFansGroupComment(path, &fansGroupCommentReceipt{Version: 1, UID: 456, ThreadID: "R_VI_62_abcdef", CommentID: 987}); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "locked" {
				lock, err := filelock.TryAcquire(path + ".lock")
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			}
			f := &fakeFansComments{deleteCode: 200}
			if runFansGroupComment(context.Background(), path, 123, "R_VI_62_abcdef", f, 0) == nil {
				t.Fatal("unsafe record accepted")
			}
			if recoverFansGroupComment(context.Background(), path, 123, f) == nil {
				t.Fatal("unsafe cleanup accepted")
			}
			if f.adds != 0 || f.deletes != 0 {
				t.Fatalf("unexpected side effects: %+v", f)
			}
		})
	}
}

func TestFansGroupCommentExplicitRejectionDoesNotLeaveUnknownReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "123.json")
	f := &fakeFansComments{createCode: 405}
	if runFansGroupComment(context.Background(), path, 123, "R_VI_62_abcdef", f, 0) == nil {
		t.Fatal("rejection accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("known rejection retained as unknown")
	}
	if f.deletes != 0 {
		t.Fatal("attempted to delete rejected comment")
	}
}

func (f *fakeFansComments) ResourceCommentAdd(ctx context.Context, r *weapi.ResourceCommentReq) (*weapi.ResourceCommentResp, error) {
	f.adds++
	if f.cancel != nil {
		f.cancel()
	}
	code := f.createCode
	if code == 0 {
		code = 200
	}
	return &weapi.ResourceCommentResp{Code: code, Comment: weapi.ResourceComment{CommentID: 987}}, f.createErr
}

func TestFansGroupComment407IsNotAssumedRejected(t *testing.T) {
	f := &fakeFansComments{createCode: 407, deleteCode: 200}
	path := filepath.Join(t.TempDir(), "123.json")
	err := runFansGroupComment(context.Background(), path, 123, "R_VI_62_abcdef", f, 0)
	if err != nil || f.deletes != 1 {
		t.Fatalf("official 407 success response was not cleaned: %v %+v", err, f)
	}
}
func (f *fakeFansComments) ResourceCommentDelete(ctx context.Context, r *weapi.ResourceCommentReq) (*weapi.ResourceCommentResp, error) {
	f.deletes++
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if r.ThreadID != "R_VI_62_abcdef" || r.CommentID != 987 {
		return nil, errors.New("wrong comment target")
	}
	return &weapi.ResourceCommentResp{Code: f.deleteCode}, nil
}

func TestFansGroupCommentDeletionFailureRecoversWithoutReposting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "123.json")
	f := &fakeFansComments{deleteCode: 500}
	if err := runFansGroupComment(context.Background(), path, 123, "R_VI_62_abcdef", f, 0); err == nil {
		t.Fatal("delete rejection silently accepted")
	}
	var pending fansGroupCommentReceipt
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &pending); err != nil {
		t.Fatal(err)
	}
	if pending.CommentID != 987 || pending.Attempts != 1 || pending.LastCode != 500 {
		t.Fatalf("missing durable receipt: %+v", pending)
	}
	f.deleteCode = 200
	if err := recoverFansGroupComment(context.Background(), path, 123, f); err != nil {
		t.Fatal(err)
	}
	if f.adds != 1 || f.deletes != 2 {
		t.Fatalf("adds=%d deletes=%d", f.adds, f.deletes)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("successful cleanup receipt retained")
	}
}

func TestFansGroupCommentCancellationStillDeletes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeFansComments{deleteCode: 200, cancel: cancel}
	if err := runFansGroupComment(ctx, filepath.Join(t.TempDir(), "123.json"), 123, "R_VI_62_abcdef", f, time.Hour); err != nil {
		t.Fatal(err)
	}
	if f.deletes != 1 {
		t.Fatal("cancellation abandoned visible comment")
	}
}

func TestFansGroupUnknownCreateDoesNotRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "123.json")
	f := &fakeFansComments{createErr: errors.New("response lost")}
	if runFansGroupComment(context.Background(), path, 123, "R_VI_62_abcdef", f, 0) == nil {
		t.Fatal("uncertain create accepted")
	}
	if runFansGroupComment(context.Background(), path, 123, "R_VI_62_abcdef", f, 0) == nil {
		t.Fatal("unknown receipt ignored")
	}
	if f.adds != 1 || f.deletes != 0 {
		t.Fatalf("ambiguous creation repeated: %+v", f)
	}
}

func TestFansGroupCommentTargetDoesNotGuessGenericID(t *testing.T) {
	for _, tt := range []struct{ raw, thread, mlog string }{
		{`{"threadId":"R_VI_62_abcdef"}`, "R_VI_62_abcdef", ""},
		{`{"url":"orpheus://video/abcdef"}`, "R_VI_62_abcdef", ""},
		{`{"mlogId":"aBC123"}`, "", "aBC123"},
		{`{"id":123,"songId":456}`, "", ""},
		{`{"url":"https://evil.invalid/video/abcdef"}`, "", ""},
		{`{"threadId":"R_SO_4_123"}`, "", ""},
	} {
		var orig eapi.FansGroupMissionOriginality
		orig.Button.Url = tt.raw
		target, err := fansGroupCommentTarget(orig)
		if tt.thread == "" && tt.mlog == "" {
			if err == nil {
				t.Fatalf("unsafe target accepted: %s", tt.raw)
			}
			continue
		}
		if err != nil || target.ThreadID != tt.thread || target.MlogID != tt.mlog {
			t.Fatalf("%s: %+v %v", tt.raw, target, err)
		}
	}
}

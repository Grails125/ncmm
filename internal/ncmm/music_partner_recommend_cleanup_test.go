package ncmm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3899/ncmm/internal/filelock"
)

type partnerCleanupFake struct {
	notes    []int64
	comments [][2]int64
	note     func(context.Context, int64) error
	comment  func(context.Context, int64, int64) error
}

func (f *partnerCleanupFake) DeleteNote(ctx context.Context, id int64) error {
	f.notes = append(f.notes, id)
	if f.note != nil {
		return f.note(ctx, id)
	}
	return nil
}

func (f *partnerCleanupFake) DeleteComment(ctx context.Context, resourceID, id int64) error {
	f.comments = append(f.comments, [2]int64{resourceID, id})
	if f.comment != nil {
		return f.comment(ctx, resourceID, id)
	}
	return nil
}

func partnerCleanupRecord(t *testing.T, directory, name string, note, comment partnerPublishReceipt) string {
	t.Helper()
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	journal := &partnerRecommendJournal{Version: 1, Works: map[string]*partnerRecommendReceipt{
		"100": {ResourceID: 200, Name: "测试歌曲", Note: note, Comment: comment},
	}}
	if err := savePartnerRecommendJournal(path, journal); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPartnerRecommendCleanupRetainsAtLeastSevenDays(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		age  time.Duration
		due  bool
	}{
		{"not yet seven days", partnerRecommendationRetention - time.Nanosecond, false},
		{"exactly seven days", partnerRecommendationRetention, true},
		{"older", partnerRecommendationRetention + time.Hour, true},
		{"future clock", -time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command, _ := partnerTestCommand(t)
			directory := t.TempDir()
			created := now.Add(-tc.age)
			path := partnerCleanupRecord(t, directory, "12-2026-10-16.json", partnerPublishReceipt{Attempted: true, ID: 301, CreatedAt: created}, partnerPublishReceipt{Attempted: true, ID: 302, CreatedAt: created})
			fake := &partnerCleanupFake{}
			if err := command.cleanupRecommendationFiles(context.Background(), directory, 12, fake, now); err != nil {
				t.Fatal(err)
			}
			journal, err := loadPartnerRecommendJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			item := journal.Works["100"]
			if (len(fake.notes) == 1) != tc.due || (len(fake.comments) == 1) != tc.due || item.Note.Deleted != tc.due || item.Comment.Deleted != tc.due {
				t.Fatal("到期边界或清理状态错误")
			}
			if item.Note.ID != 301 || item.Comment.ID != 302 || !item.Note.Attempted || !item.Comment.Attempted {
				t.Fatal("删除后必须保留发布ID和意图，避免重复发布")
			}
			if tc.due && fake.comments[0] != [2]int64{200, 302} {
				t.Fatal("乐评删除未使用回执对应的歌曲")
			}
		})
	}
}

func TestPartnerRecommendCleanupSkipsUnknownLegacyAndDeletedReceipts(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name    string
		receipt partnerPublishReceipt
	}{
		{"unknown ID", partnerPublishReceipt{Attempted: true, CreatedAt: now.Add(-8 * 24 * time.Hour)}},
		{"missing timestamp", partnerPublishReceipt{Attempted: true, ID: 301}},
		{"no recorded publish", partnerPublishReceipt{ID: 301, CreatedAt: now.Add(-8 * 24 * time.Hour)}},
		{"already deleted", partnerPublishReceipt{Attempted: true, ID: 301, CreatedAt: now.Add(-8 * 24 * time.Hour), Deleted: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command, _ := partnerTestCommand(t)
			directory := t.TempDir()
			path := partnerCleanupRecord(t, directory, "12-2026-10-15.json", tc.receipt, tc.receipt)
			before, _ := os.ReadFile(path)
			fake := &partnerCleanupFake{}
			if err := command.cleanupRecommendationFiles(context.Background(), directory, 12, fake, now); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(path)
			if len(fake.notes)+len(fake.comments) != 0 || string(before) != string(after) {
				t.Fatal("未知或无需清理的发布记录被处理")
			}
		})
	}
}

func TestPartnerRecommendCleanupIsolatesAccountAndExpectedFileNames(t *testing.T) {
	command, _ := partnerTestCommand(t)
	directory := t.TempDir()
	now := time.Now()
	receipt := partnerPublishReceipt{Attempted: true, ID: 301, CreatedAt: now.Add(-8 * 24 * time.Hour)}
	for _, name := range []string{"123-2026-10-15.json", "12-invalid.json", "12-2026-10-15-copy.json", "12-2026-10-15.json.bak", "notes.json"} {
		partnerCleanupRecord(t, directory, name, receipt, receipt)
	}
	path := partnerCleanupRecord(t, directory, "12-2026-10-15.json", receipt, partnerPublishReceipt{})
	fake := &partnerCleanupFake{}
	if err := command.cleanupRecommendationFiles(context.Background(), directory, 12, fake, now); err != nil {
		t.Fatal(err)
	}
	if len(fake.notes) != 1 || len(fake.comments) != 0 {
		t.Fatal("扫描超出了当前账号的有效日期记录")
	}
	journal, err := loadPartnerRecommendJournal(path)
	if err != nil || !journal.Works["100"].Note.Deleted {
		t.Fatal("当前账号记录未保存")
	}
}

func TestPartnerRecommendCleanupFailureRetriesOnlyRemainingContent(t *testing.T) {
	command, _ := partnerTestCommand(t)
	directory := t.TempDir()
	now := time.Now()
	created := now.Add(-8 * 24 * time.Hour)
	path := partnerCleanupRecord(t, directory, "12-2026-10-15.json", partnerPublishReceipt{Attempted: true, ID: 301, CreatedAt: created}, partnerPublishReceipt{Attempted: true, ID: 302, CreatedAt: created})
	fake := &partnerCleanupFake{note: func(context.Context, int64) error { return errors.New("temporary failure") }}
	if err := command.cleanupRecommendationFiles(context.Background(), directory, 12, fake, now); err == nil {
		t.Fatal("删除失败必须报告并保留重试记录")
	}
	journal, err := loadPartnerRecommendJournal(path)
	if err != nil || journal.Works["100"].Note.Deleted || !journal.Works["100"].Comment.Deleted {
		t.Fatal("部分失败状态未正确保存，或后续乐评未清理")
	}
	fake.note = nil
	if err := command.cleanupRecommendationFiles(context.Background(), directory, 12, fake, now); err != nil {
		t.Fatal(err)
	}
	if len(fake.notes) != 2 || len(fake.comments) != 1 {
		t.Fatal("恢复清理应只重试失败内容")
	}
}

func TestPartnerRecommendCleanupHonorsFileLock(t *testing.T) {
	command, _ := partnerTestCommand(t)
	directory := t.TempDir()
	now := time.Now()
	path := partnerCleanupRecord(t, directory, "12-2026-10-15.json", partnerPublishReceipt{Attempted: true, ID: 301, CreatedAt: now.Add(-8 * 24 * time.Hour)}, partnerPublishReceipt{})
	lock, err := filelock.TryAcquire(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	fake := &partnerCleanupFake{}
	if err := command.cleanupRecommendationFiles(context.Background(), directory, 12, fake, now); err != nil {
		t.Fatal(err)
	}
	if len(fake.notes) != 0 {
		t.Fatal("持有发布记录锁时不能并发清理")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.cleanupRecommendationFiles(context.Background(), directory, 12, fake, now); err != nil || len(fake.notes) != 1 {
		t.Fatalf("锁释放后应恢复清理：%v", err)
	}
}

func TestPartnerRecommendCleanupCancellationPersistsCompletedDeletion(t *testing.T) {
	command, _ := partnerTestCommand(t)
	directory := t.TempDir()
	now := time.Now()
	created := now.Add(-8 * 24 * time.Hour)
	path := partnerCleanupRecord(t, directory, "12-2026-10-15.json", partnerPublishReceipt{Attempted: true, ID: 301, CreatedAt: created}, partnerPublishReceipt{Attempted: true, ID: 302, CreatedAt: created})
	ctx, cancel := context.WithCancel(context.Background())
	fake := &partnerCleanupFake{note: func(context.Context, int64) error { cancel(); return nil }}
	if err := command.cleanupRecommendationFiles(ctx, directory, 12, fake, now); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消未传播：%v", err)
	}
	journal, err := loadPartnerRecommendJournal(path)
	if err != nil || !journal.Works["100"].Note.Deleted || journal.Works["100"].Comment.Deleted || len(fake.comments) != 0 {
		t.Fatal("取消前成功删除应保存，取消后不应继续请求")
	}
	if err := command.cleanupRecommendationFiles(context.Background(), directory, 12, fake, now); err != nil {
		t.Fatal(err)
	}
	if len(fake.notes) != 1 || len(fake.comments) != 1 {
		t.Fatal("取消后恢复不应重删已完成的内容")
	}
}

func TestPartnerRecommendCleanupCancelledBeforeStart(t *testing.T) {
	command, _ := partnerTestCommand(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := &partnerCleanupFake{}
	if err := command.cleanupRecommendationFiles(ctx, t.TempDir(), 12, fake, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("预先取消的任务应直接退出：%v", err)
	}
	if len(fake.notes)+len(fake.comments) != 0 {
		t.Fatal("已取消任务不能删除内容")
	}
}

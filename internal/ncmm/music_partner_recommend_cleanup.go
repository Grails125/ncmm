package ncmm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/3899/ncmm/internal/filelock"
)

const partnerRecommendationRetention = 7 * 24 * time.Hour

type partnerRecommendCleanupAPI interface {
	DeleteNote(context.Context, int64) error
	DeleteComment(context.Context, int64, int64) error
}

// cleanupRecommendations 在后续任务中清理满七天的本功能发布记录，不阻塞等待到期。
func (c *MusicPartner) cleanupRecommendations(ctx context.Context, uid int64, client partnerRecommendCleanupAPI) error {
	directory := filepath.Join(c.root.getHomePath(), "music-partner-recommend")
	return c.cleanupRecommendationFiles(ctx, directory, uid, client, time.Now())
}

func (c *MusicPartner) cleanupRecommendationFiles(ctx context.Context, directory string, uid int64, client partnerRecommendCleanupAPI, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if uid <= 0 || client == nil {
		return fmt.Errorf("无法确认推荐内容清理账号")
	}
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		c.printf("读取推荐内容清理目录失败：%v", err)
		return fmt.Errorf("无法读取推荐内容清理记录")
	}
	prefix := strconv.FormatInt(uid, 10) + "-"
	var failures []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		name := entry.Name()
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".json")
		parsed, err := time.Parse("2006-01-02", day)
		if err != nil || parsed.Format("2006-01-02") != day {
			continue
		}
		if err := c.cleanupRecommendationFile(ctx, filepath.Join(directory, name), client, now); err != nil {
			failures = append(failures, err)
			if musicPartnerMustStop(ctx, err) {
				return errors.Join(failures...)
			}
		}
	}
	return errors.Join(failures...)
}

func (c *MusicPartner) cleanupRecommendationFile(ctx context.Context, path string, client partnerRecommendCleanupAPI, now time.Time) error {
	lock, err := filelock.TryAcquire(path + ".lock")
	if errors.Is(err, filelock.ErrLocked) {
		c.printf("推荐发布记录正在使用，本次跳过对应内容清理")
		return nil
	}
	if err != nil {
		c.printf("锁定推荐内容清理记录失败：%v", err)
		return fmt.Errorf("推荐内容清理记录暂不可用")
	}
	defer lock.Close()
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		c.printf("推荐内容清理记录不是可读取的普通文件")
		return fmt.Errorf("推荐内容清理记录无效")
	}
	journal, err := loadPartnerRecommendJournal(path)
	if err != nil {
		c.printf("读取推荐内容清理记录失败：%v", err)
		return err
	}
	keys := make([]string, 0, len(journal.Works))
	for id := range journal.Works {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	var failures []error
	for _, workID := range keys {
		work := journal.Works[workID]
		for _, item := range []struct {
			kind    string
			receipt *partnerPublishReceipt
		}{
			{kind: "笔记", receipt: &work.Note},
			{kind: "乐评", receipt: &work.Comment},
		} {
			if err := ctx.Err(); err != nil {
				return errors.Join(append(failures, err)...)
			}
			receipt := item.receipt
			if receipt.Deleted || !receipt.Attempted || receipt.ID <= 0 || receipt.CreatedAt.IsZero() || now.Before(receipt.CreatedAt.Add(partnerRecommendationRetention)) {
				continue
			}
			label := "作品ID=" + workID
			if work.Name != "" {
				label = strconv.Quote(work.Name) + "（" + label + "）"
			}
			if item.kind == "笔记" {
				err = client.DeleteNote(ctx, receipt.ID)
			} else {
				err = client.DeleteComment(ctx, work.ResourceID, receipt.ID)
			}
			if err != nil {
				c.printf("%s 的推荐%s清理失败（内容ID=%d），保留记录下次重试：%v", label, item.kind, receipt.ID, err)
				failures = append(failures, err)
				if musicPartnerMustStop(ctx, err) {
					return errors.Join(failures...)
				}
				continue
			}
			receipt.Deleted = true
			if err := savePartnerRecommendJournal(path, journal); err != nil {
				receipt.Deleted = false
				c.printf("%s 的推荐%s已删除，但清理状态保存失败（内容ID=%d）：%v", label, item.kind, receipt.ID, err)
				return errors.Join(append(failures, err)...)
			}
			c.printf("%s 的推荐%s已保留满 7 天并清理（内容ID=%d）", label, item.kind, receipt.ID)
		}
	}
	return errors.Join(failures...)
}

package ncmm

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/internal/atomicfile"
	"github.com/3899/ncmm/internal/filelock"
)

type musicPartnerRecommendAPI interface {
	PartnerDailyTask(context.Context, *weapi.PartnerTaskReq) (*weapi.PartnerTaskResp, error)
	PartnerRecommendResources(context.Context) (*weapi.PartnerRecommendResourcesResp, error)
	PartnerExtraReport(context.Context, *weapi.PartnerExtraReportReq) (*weapi.PartnerExtraReportResp, error)
	PrepareAudio(context.Context, weapi.PartnerWork) error
	PrepareNote(context.Context, weapi.PartnerRecommendResource, string) (*eapi.PartnerNotePublishReq, error)
	PublishNote(context.Context, *eapi.PartnerNotePublishReq) (int64, error)
	PublishComment(context.Context, weapi.PartnerRecommendResource, string) (int64, error)
}

// 先记录发布意图，再保存真实回执。超时或进程退出后不能重新发表未知结果的内容。
type partnerPublishReceipt struct {
	Attempted bool      `json:"attempted"`
	ID        int64     `json:"id"`
	Reported  bool      `json:"reported"`
	CreatedAt time.Time `json:"createdAt"`
	Deleted   bool      `json:"deleted"`
}

type partnerRecommendReceipt struct {
	Name       string                `json:"name"`
	ResourceID int64                 `json:"resourceId"`
	Note       partnerPublishReceipt `json:"note"`
	Comment    partnerPublishReceipt `json:"comment"`
}

type partnerRecommendJournal struct {
	Version int                                 `json:"version"`
	Works   map[string]*partnerRecommendReceipt `json:"works"`
}

func loadPartnerRecommendJournal(path string) (*partnerRecommendJournal, error) {
	result := &partnerRecommendJournal{Version: 1, Works: make(map[string]*partnerRecommendReceipt)}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, fmt.Errorf("无法读取推荐发布记录")
	}
	if len(data) > 2*1024*1024 || json.Unmarshal(data, result) != nil || result.Version != 1 || result.Works == nil {
		return nil, fmt.Errorf("推荐发布记录无效，已停止自动发布，请检查本地记录")
	}
	for id, item := range result.Works {
		workID, err := strconv.ParseInt(id, 10, 64)
		if err != nil || workID <= 0 || item == nil || item.ResourceID <= 0 || item.Note.ID < 0 || item.Comment.ID < 0 {
			return nil, fmt.Errorf("推荐发布记录包含无效作品，已停止自动发布")
		}
	}
	return result, nil
}

func savePartnerRecommendJournal(path string, journal *partnerRecommendJournal) error {
	data, err := json.Marshal(journal)
	if err == nil {
		err = atomicfile.Write(path, data, 0600)
	}
	if err != nil {
		return fmt.Errorf("推荐发布记录保存失败，已停止后续发布")
	}
	return nil
}

func (c *MusicPartner) recommendAccount(ctx context.Context, client *api.Client, cfg *config.MusicPartnerConf, cookieFile string) error {
	ctx, cancel := musicPartnerDayContext(ctx, time.Now())
	defer cancel()
	if cfg.RecommendCountMax == 0 {
		files, _ := filepath.Glob(filepath.Join(c.root.getHomePath(), "music-partner-recommend", "*.json"))
		if len(files) == 0 {
			return nil
		}
	}
	partner := weapi.New(client)
	home, err := partner.PartnerHome(ctx, &weapi.PartnerHomeReq{})
	if err != nil {
		return c.transportFailure(ctx, "确认推荐任务账号", err)
	}
	if home == nil || home.Code != 200 || home.Data.User.UserId <= 0 {
		return fmt.Errorf("无法确认推荐任务账号，未发布内容")
	}
	uid := home.Data.User.UserId
	day := time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02")
	path := filepath.Join(c.root.getHomePath(), "music-partner-recommend", fmt.Sprintf("%d-%s.json", uid, day))
	adapter := &partnerRecommendClient{Api: partner, publish: eapi.New(client), uid: uid, media: partnerMediaClient(client), token: c.root.Cfg.Accounts.AntiCheatTokenFor(cookieFile)}
	cleanupErr := c.cleanupRecommendations(ctx, uid, adapter)
	if cleanupErr != nil {
		c.printf("推荐内容到期清理未全部完成：%v", cleanupErr)
	}
	if musicPartnerMustStop(ctx, cleanupErr) {
		return cleanupErr
	}
	if partnerRecommendDailyTarget(path, cfg) > 0 {
		adapter.mode, err = partnerRecommendPublishMode(client, c.root.Cfg.Network)
		if err != nil {
			c.printf("推荐发布配置检查失败：%v", err)
			return errors.Join(cleanupErr, err)
		}
		c.printf("推荐发布协议：%s（按当前账号会话与移动端 UA 选择）", adapter.mode)
	}
	return errors.Join(cleanupErr, c.recommend(ctx, adapter, cfg, path))
}

func (c *MusicPartner) recommend(ctx context.Context, client musicPartnerRecommendAPI, cfg *config.MusicPartnerConf, path string) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	target := partnerRecommendDailyTarget(path, cfg)
	if target == 0 {
		c.printf("推荐歌曲：今日随机目标为 0 首，跳过新增推荐")
		return nil
	}
	ctx, cancel := musicPartnerDayContext(ctx, time.Now())
	defer cancel()
	lock, err := filelock.TryAcquire(path + ".lock")
	if err != nil {
		return fmt.Errorf("该账号的推荐任务正在执行或记录目录不可用")
	}
	defer lock.Close()
	journal, err := loadPartnerRecommendJournal(path)
	if err != nil {
		return err
	}
	daily, err := c.recommendDaily(ctx, client)
	if err != nil {
		return err
	}
	if !daily.Data.CanInteract {
		return fmt.Errorf("当前账号暂不能执行推荐互动，未发布内容")
	}
	items := daily.Data.RecResources
	list, listErr := client.PartnerRecommendResources(ctx)
	if listErr == nil && list != nil && list.Code == 200 && len(list.Data.Resources) > 0 {
		items = list.Data.Resources
	} else {
		if listErr != nil {
			failure := c.transportFailure(ctx, "读取推荐歌曲", listErr)
			if musicPartnerMustStop(ctx, failure) {
				return failure
			}
		}
		if list != nil && list.Code != 200 && musicPartnerMustStop(ctx, musicPartnerResponseError("读取推荐歌曲", list.Code, list.Message+list.Msg)) {
			return musicPartnerResponseError("读取推荐歌曲", list.Code, list.Message+list.Msg)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.printf("推荐列表暂不可用，使用每日任务返回的推荐歌曲")
	}
	// 合并服务端状态；手动完成和本地已发起的歌曲都占用每日目标。
	items = mergePartnerRecommendations(items, daily.Data.RecResources)
	used := make(map[string]bool)
	completed := 0
	for _, item := range items {
		if item.PublishEvent || item.PublishComment {
			used[strconv.FormatInt(item.Work.Id, 10)] = true
		}
		if item.PublishEvent && item.PublishComment {
			completed++
		}
	}
	for key := range journal.Works {
		used[key] = true
	}
	c.printf("推荐歌曲：每日目标 %d 首（范围 %d～%d），服务器已完成笔记和乐评 %d 首", target, cfg.RecommendCountMin, cfg.RecommendCountMax, completed)
	if completed >= target {
		return nil
	}
	var failures []error
	seen := make(map[int64]bool)
	selected := 0
	// 先恢复部分完成的歌曲，防止另选新歌导致超出每日目标。
	ordered := append([]weapi.PartnerRecommendResource{}, items...)
	if cfg.RecommendOrder == "random" {
		// 先按作品 ID 固定输入，服务端重排列表不会改变同一批候选的日内随机顺序。
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].Work.Id < ordered[j].Work.Id })
		partnerRecommendRandom(path, "order").Shuffle(len(ordered), func(i, j int) { ordered[i], ordered[j] = ordered[j], ordered[i] })
		c.printf("推荐歌曲抽取方式：随机抽取（同账号同日相同候选顺序保持一致）")
	} else {
		c.printf("推荐歌曲抽取方式：按列表顺序")
	}
	for pass := 0; pass < 2; pass++ {
		for _, item := range ordered {
			key := strconv.FormatInt(item.Work.Id, 10)
			if item.Work.Id <= 0 || item.Work.ResourceId <= 0 || seen[item.Work.Id] || used[key] != (pass == 0) {
				continue
			}
			seen[item.Work.Id] = true
			if item.PublishEvent && item.PublishComment {
				continue
			}
			if !item.CanInteract {
				c.printf("%s 当前不允许推荐互动，跳过发布", musicPartnerWorkLabel(item.Work))
				continue
			}
			if selected >= target-completed {
				continue
			}
			if !used[key] && len(used) >= target {
				continue
			}
			if !used[key] {
				used[key] = true
			}
			selected++
			if err := c.recommendSong(ctx, client, cfg, item, path, journal); err != nil {
				c.printf("%s 推荐任务未完成：%v", musicPartnerWorkLabel(item.Work), err)
				failures = append(failures, err)
				if musicPartnerMustStop(ctx, err) {
					return errors.Join(failures...)
				}
			}
		}
	}
	after, err := c.recommendDaily(ctx, client)
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	finished := 0
	finalItems := after.Data.RecResources
	if refreshed, refreshErr := client.PartnerRecommendResources(ctx); refreshErr == nil && refreshed != nil && refreshed.Code == 200 {
		finalItems = mergePartnerRecommendations(refreshed.Data.Resources, finalItems)
	}
	for _, item := range finalItems {
		if item.PublishEvent && item.PublishComment {
			finished++
		}
	}
	c.printf("推荐歌曲：服务器确认已完成 %d 首，今日推荐积分 %d → %d（%+d）", finished, daily.Data.CurRcmdScore, after.Data.CurRcmdScore, after.Data.CurRcmdScore-daily.Data.CurRcmdScore)
	if finished < target {
		failures = append(failures, fmt.Errorf("推荐歌曲尚未达到每日目标，已保留发布记录，下次仅恢复未完成步骤"))
	}
	return errors.Join(failures...)
}

func partnerRecommendRandom(path, purpose string) *rand.Rand {
	// 文件名由账号 ID 和北京时间日期构成；不使用会随机器或 home 路径变化的目录。
	digest := sha256.Sum256([]byte(purpose + "|" + filepath.Base(path)))
	return rand.New(rand.NewPCG(binary.BigEndian.Uint64(digest[:8]), binary.BigEndian.Uint64(digest[8:16])))
}

func partnerRecommendDailyTarget(path string, cfg *config.MusicPartnerConf) int {
	if cfg.RecommendCountMax <= cfg.RecommendCountMin {
		return cfg.RecommendCountMin
	}
	// uint64 覆盖 Go int 的整个非负范围，避免 max-min+1 的有符号溢出。
	width := uint64(cfg.RecommendCountMax) - uint64(cfg.RecommendCountMin) + 1
	rng := partnerRecommendRandom(path, fmt.Sprintf("target:%d:%d", cfg.RecommendCountMin, cfg.RecommendCountMax))
	return cfg.RecommendCountMin + int(rng.Uint64N(width))
}

func mergePartnerRecommendations(items, daily []weapi.PartnerRecommendResource) []weapi.PartnerRecommendResource {
	result := make([]weapi.PartnerRecommendResource, 0, len(items)+len(daily))
	index := make(map[int64]int)
	for _, item := range append(append([]weapi.PartnerRecommendResource{}, items...), daily...) {
		if item.Work.Id <= 0 || item.Work.ResourceId <= 0 {
			continue
		}
		if i, ok := index[item.Work.Id]; ok {
			result[i].PublishEvent = result[i].PublishEvent || item.PublishEvent
			result[i].PublishComment = result[i].PublishComment || item.PublishComment
			result[i].CanInteract = result[i].CanInteract && item.CanInteract
		} else {
			index[item.Work.Id] = len(result)
			result = append(result, item)
		}
	}
	return result
}

func (c *MusicPartner) recommendDaily(ctx context.Context, client musicPartnerRecommendAPI) (*weapi.PartnerTaskResp, error) {
	result, err := client.PartnerDailyTask(ctx, &weapi.PartnerTaskReq{})
	if err != nil {
		return nil, c.transportFailure(ctx, "读取推荐任务状态", err)
	}
	if result == nil {
		return nil, fmt.Errorf("推荐任务状态为空")
	}
	if result.Code != 200 {
		return nil, musicPartnerResponseError("读取推荐任务状态", result.Code, result.Message+result.Msg)
	}
	return result, nil
}

func partnerRecommendText(templates []string, work weapi.PartnerWork) (string, error) {
	if len(templates) == 0 {
		return "", fmt.Errorf("推荐内容模板为空")
	}
	text := strings.TrimSpace(strings.NewReplacer("{name}", work.Name, "{artist}", work.AuthorName).Replace(templates[rand.IntN(len(templates))]))
	if text == "" || utf8.RuneCountInString(text) > 500 {
		return "", fmt.Errorf("推荐模板替换后的内容须为 1～500 个字符")
	}
	return text, nil
}

func (c *MusicPartner) recommendSong(ctx context.Context, client musicPartnerRecommendAPI, cfg *config.MusicPartnerConf, item weapi.PartnerRecommendResource, path string, journal *partnerRecommendJournal) error {
	key := strconv.FormatInt(item.Work.Id, 10)
	receipt := journal.Works[key]
	if receipt == nil {
		receipt = &partnerRecommendReceipt{ResourceID: item.Work.ResourceId, Name: item.Work.Name}
		journal.Works[key] = receipt
	}
	if receipt.ResourceID != item.Work.ResourceId {
		return fmt.Errorf("推荐作品与发布记录的歌曲不一致，未继续发布")
	}
	if item.PublishEvent {
		receipt.Note.Reported = true
	}
	if item.PublishComment {
		receipt.Comment.Reported = true
	}
	if err := savePartnerRecommendJournal(path, journal); err != nil {
		return err
	}
	// 凭回执补上报无需再创建内容；未知回执只能通过服务器完成标记恢复。
	needsNew := (!receipt.Note.Reported && !receipt.Note.Attempted) || (!receipt.Comment.Reported && !receipt.Comment.Attempted)
	if needsNew {
		if err := client.PrepareAudio(ctx, item.Work); err != nil {
			return fmt.Errorf("获取推荐音频失败，未发布：%w", err)
		}
		delay := cfg.RecommendWaitMin
		if cfg.RecommendWaitMax > delay {
			delay += rand.IntN(cfg.RecommendWaitMax - delay + 1)
		}
		c.printf("%s 已获取音频，等待 %d 秒后确认试听上报", musicPartnerWorkLabel(item.Work), delay)
		if err := c.wait(ctx, time.Duration(delay)*time.Second); err != nil {
			return err
		}
		if err := c.reportRecommendation(ctx, client, item.Work, "PLAY_END", 0); err != nil {
			return err
		}
	}
	var failures []error
	for _, action := range []struct {
		kind      string
		name      string
		state     *partnerPublishReceipt
		templates []string
	}{{"PUBLISH_EVENT", "笔记", &receipt.Note, cfg.RecommendNotes}, {"PUBLISH_COMMENT", "乐评", &receipt.Comment, cfg.RecommendComments}} {
		if action.state.Reported {
			c.printf("%s %s已完成，跳过重复发布", musicPartnerWorkLabel(item.Work), action.name)
			continue
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if action.state.Attempted && action.state.ID == 0 {
			failures = append(failures, fmt.Errorf("%s上次发布结果未知，未重复发布，请在 App 核对", action.name))
			continue
		}
		if !action.state.Attempted {
			content, err := partnerRecommendText(action.templates, item.Work)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			var note *eapi.PartnerNotePublishReq
			if action.kind == "PUBLISH_EVENT" {
				note, err = client.PrepareNote(ctx, item, content)
				if err != nil {
					failures = append(failures, fmt.Errorf("准备推荐笔记失败：%w", err))
					if musicPartnerMustStop(ctx, err) {
						return errors.Join(failures...)
					}
					continue
				}
			}
			action.state.Attempted = true
			if err := savePartnerRecommendJournal(path, journal); err != nil {
				return errors.Join(append(failures, err)...)
			}
			var id int64
			if action.kind == "PUBLISH_EVENT" {
				id, err = client.PublishNote(ctx, note)
			} else {
				id, err = client.PublishComment(ctx, item, content)
			}
			if err != nil || id <= 0 {
				var publication *eapi.PartnerPublishError
				if errors.As(err, &publication) {
					c.printf("%s %s发布诊断：%s", musicPartnerWorkLabel(item.Work), action.name, publication.Error())
					if publication.NotSent || publication.Rejected {
						action.state.Attempted = false
						if saveErr := savePartnerRecommendJournal(path, journal); saveErr != nil {
							return errors.Join(append(failures, saveErr)...)
						}
						c.printf("%s %s发布未完成：%v", musicPartnerWorkLabel(item.Work), action.name, err)
						failures = append(failures, fmt.Errorf("%s发布未完成：%w", action.name, err))
						if musicPartnerMustStop(ctx, err) {
							return errors.Join(failures...)
						}
						continue
					}
				}
				c.printf("%s %s未获得确定回执（错误类型 %T），不会自动重发", musicPartnerWorkLabel(item.Work), action.name, err)
				var failure *musicPartnerFailure
				if errors.As(err, &failure) {
					c.printf("%s %s发布诊断：%s", musicPartnerWorkLabel(item.Work), action.name, failure.Error())
				}
				failures = append(failures, fmt.Errorf("%s发布结果未确认，请在 App 核对，已保留记录防止重复发表", action.name))
				if musicPartnerMustStop(ctx, err) {
					return errors.Join(append(failures, err)...)
				}
				continue
			}
			action.state.ID = id
			action.state.CreatedAt = time.Now().UTC()
			if err := savePartnerRecommendJournal(path, journal); err != nil {
				c.printf("%s %s已创建 ID=%d，但回执写入失败，请人工核对", musicPartnerWorkLabel(item.Work), action.name, id)
				return errors.Join(append(failures, err)...)
			}
			c.printf("%s %s已发布，ID=%d；保留内容并提交合伙人任务上报", musicPartnerWorkLabel(item.Work), action.name, id)
		}
		if err := c.reportRecommendation(ctx, client, item.Work, action.kind, action.state.ID); err != nil {
			failures = append(failures, err)
			if musicPartnerMustStop(ctx, err) {
				return errors.Join(failures...)
			}
			continue
		}
		action.state.Reported = true
		if err := savePartnerRecommendJournal(path, journal); err != nil {
			return errors.Join(append(failures, err)...)
		}
	}
	return errors.Join(failures...)
}

func (c *MusicPartner) reportRecommendation(ctx context.Context, client musicPartnerRecommendAPI, work weapi.PartnerWork, kind string, id int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	bizID := ""
	if id > 0 {
		bizID = strconv.FormatInt(id, 10)
	}
	for attempt := 0; attempt < 2; attempt++ {
		reply, err := client.PartnerExtraReport(ctx, &weapi.PartnerExtraReportReq{WorkId: strconv.FormatInt(work.Id, 10), ResourceId: strconv.FormatInt(work.ResourceId, 10), BizResourceId: bizID, InteractType: kind})
		if err != nil {
			return c.transportFailure(ctx, "推荐任务上报", err)
		}
		if reply == nil {
			return fmt.Errorf("推荐上报响应为空，已保留发布回执")
		}
		c.printf("%s 推荐上报：类型=%s，服务码=%d，确认=%t", musicPartnerWorkLabel(work), kind, reply.Code, reply.Data.InteractResult)
		if reply.Code != 200 {
			return musicPartnerResponseError("推荐任务上报", reply.Code, reply.Message+reply.Msg)
		}
		// 成功抓包的 failedReason 包含“发布成功，积分+2/+1”，非空不等于失败。
		if !reply.DataNull && reply.Data.InteractResult {
			return nil
		}
		if kind != "PUBLISH_COMMENT" || attempt > 0 {
			return fmt.Errorf("服务器未确认推荐任务上报，已保留回执，下次仅补上报")
		}
		if err := c.wait(ctx, 3*time.Second); err != nil {
			return err
		}
	}
	return nil
}

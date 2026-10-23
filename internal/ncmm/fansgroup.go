// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

package ncmm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/types"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/pkg/log"

	"github.com/spf13/cobra"
)

// 乐迷团任务默认使用的粉丝团 ID。
const defaultFansGroupId = "1872529203038486609"

var fansGroupSongIDPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:songId|songIds|trackId|resourceId)[^0-9]{0,16}([0-9]{5,})`),
	regexp.MustCompile(`(?i)/song/([0-9]{5,})`),
}

type FansGroupOpts struct {
	CookieFile string
}

type FansGroup struct {
	root *Root
	cmd  *cobra.Command
	l    *log.Logger
	rng  *rand.Rand
	opts FansGroupOpts
}

func NewFansGroup(root *Root, l *log.Logger) *FansGroup {
	c := &FansGroup{
		root: root,
		l:    l,
		rng:  rand.New(rand.NewSource(time.Now().UnixNano())),
		cmd: &cobra.Command{
			Use:     "fansgroup",
			Short:   "[need login] 执行乐迷团任务",
			Example: "  ncmm fansgroup --cookie-file run/cookie.json",
		},
	}
	c.addFlags()
	c.cmd.RunE = func(cmd *cobra.Command, args []string) error { return c.execute(cmd.Context()) }
	return c
}

func (c *FansGroup) Command() *cobra.Command { return c.cmd }

func (c *FansGroup) addFlags() {
	c.cmd.Flags().StringVar(&c.opts.CookieFile, "cookie-file", "", "cookie file path")
}

func (c *FansGroup) sleepBetweenAccounts(ctx context.Context, currentAccount string) {
	sleepSec := 5 + c.rng.Intn(16) // 5 ~ 20 秒
	c.cmd.Printf("[fansgroup] ⏳ 账号 (%s) 任务处理完毕，为规避风控，随机等待 %d 秒后继续下一个账号...\n", currentAccount, sleepSec)
	select {
	case <-ctx.Done():
	case <-time.After(time.Duration(sleepSec) * time.Second):
	}
}

func (c *FansGroup) execute(ctx context.Context) error {
	var queue []string
	if c.opts.CookieFile != "" {
		queue = append(queue, c.opts.CookieFile)
	} else {
		cfg := c.root.Cfg
		if cfg.Accounts == nil {
			err := fmt.Errorf("missing accounts section")
			c.root.ReportFailure("-", "fansgroup", err)
			return err
		}
		if cfg.FansGroup != nil && cfg.FansGroup.EnableMain && cfg.Accounts.Main != "" {
			queue = append(queue, cfg.Accounts.Main)
		}
		if cfg.FansGroup != nil && cfg.FansGroup.EnableSecondaries {
			queue = append(queue, cfg.Accounts.Secondary...)
		}
	}

	if len(queue) == 0 {
		c.cmd.Println("[fansgroup] 未配置可用账号，请检查 config.yaml")
		c.root.ReportSkip("-", "fansgroup", "未配置可用账号")
		return nil
	}

	for i, cookie := range queue {
		c.cmd.Printf("[fansgroup] 开始处理账号 (%s)\n", cookie)
		if err := c.ExecuteForCookie(ctx, cookie); err != nil {
			c.cmd.Printf("[fansgroup] 账号处理失败 (%s): %v\n", cookie, err)
			c.root.ReportFailure(cookie, "fansgroup", err)
		}
		c.cmd.Println("[fansgroup] --------------------------------------------------")

		if i < len(queue)-1 {
			c.sleepBetweenAccounts(ctx, cookie)
		}
	}
	return nil
}

func (c *FansGroup) ExecuteForCookie(ctx context.Context, cookieFile string) error {
	networkCfg := c.root.Cfg.Network
	absPath, err := filepath.Abs(cookieFile)
	if err != nil {
		return fmt.Errorf("resolve cookie path failed: %w", err)
	}

	cfgCopy := *networkCfg
	cfgCopy.Cookie.Filepath = absPath
	cli, err := api.NewClient(&cfgCopy, c.l)
	if err != nil {
		return fmt.Errorf("NewClient: %w", err)
	}
	defer cli.Close(ctx)

	eapiCli := eapi.New(cli)
	weapiCli := weapi.New(cli)

	user, err := weapiCli.GetUserInfo(ctx, &weapi.GetUserInfoReq{})
	if err != nil {
		return fmt.Errorf("verify login failed: %w", err)
	}
	if user.Code != 200 || user.Profile == nil || user.Account == nil {
		return fmt.Errorf("user not logged in or session expired")
	}
	c.cmd.Printf("[fansgroup] 当前账号: uid=%v nickname=%q\n", user.Account.Id, user.Profile.Nickname)
	commentCheckToken := ""
	if c.root.Cfg.FansGroup != nil {
		commentCheckToken = c.root.Cfg.FansGroup.CommentCheckTokenFor(cookieFile)
	}
	if err := recoverFansGroupComment(ctx, c.commentReceiptPath(user.Account.Id), user.Account.Id, fansGroupCommentClient{api: weapiCli, token: commentCheckToken}); err != nil {
		c.cmd.Printf("[fansgroup] 评论清理恢复：%v\n", err)
		c.root.ReportFailure(cookieFile, "fansgroup", err)
	}

	groupIds := c.fansGroupIDs()
	c.cmd.Printf("[fansgroup] 乐迷团任务数: %d\n", len(groupIds))
	failed := 0
	var firstErr error
	for i, fansGroupId := range groupIds {
		c.cmd.Printf("[fansgroup] 开始处理乐迷团 [%d/%d]: groupId=%s\n", i+1, len(groupIds), fansGroupId)
		if err := c.executeOneFansGroup(ctx, cli, eapiCli, weapiCli, user.Account.Id, fansGroupId, commentCheckToken); err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			c.cmd.Printf("[fansgroup] 乐迷团处理失败 groupId=%s: %v\n", fansGroupId, err)
		}
		if i < len(groupIds)-1 {
			sleepSec := 2 + c.rng.Intn(4)
			c.cmd.Printf("[fansgroup] 等待 %d 秒后继续下一个乐迷团...\n", sleepSec)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(sleepSec) * time.Second):
			}
		}
	}
	if failed == len(groupIds) && firstErr != nil {
		return fmt.Errorf("all configured fansgroup tasks failed: %w", firstErr)
	}
	return nil
}

func (c *FansGroup) fansGroupIDs() []string {
	if c.root == nil || c.root.Cfg == nil {
		return []string{defaultFansGroupId}
	}
	return configuredFansGroupIDs(c.root.Cfg.FansGroup)
}

func configuredFansGroupIDs(cfg *config.FansGroupConf) []string {
	if cfg == nil {
		return []string{defaultFansGroupId}
	}
	ids := uniqueStrings([]string(cfg.GroupIDs))
	if len(ids) == 0 {
		return []string{defaultFansGroupId}
	}
	return ids
}

func (c *FansGroup) executeOneFansGroup(ctx context.Context, cli *api.Client, eapiCli *eapi.Api, weapiCli *weapi.Api, currentUserId int64, fansGroupId, commentCheckToken string) error {
	c.cmd.Println("[fansgroup] 查询粉丝团详情...")
	detail, err := eapiCli.FansGroupDetailGet(ctx, &eapi.FansGroupDetailGetReq{GroupId: fansGroupId})
	if err != nil {
		return fmt.Errorf("fetch fans group detail failed: %w", err)
	}
	if detail.Code != 200 {
		return fmt.Errorf("fetch fans group detail failed: code=%d", detail.Code)
	}

	boardId := detail.Data.FansGroupInfo.BoardId
	groupName := detail.Data.FansGroupInfo.FansGroupName
	c.cmd.Printf("[fansgroup] 粉丝团: %s (groupId=%s, boardId=%s)\n", groupName, fansGroupId, boardId)

	joined := false
	userGrpDetail, err := eapiCli.FansGroupUserGroupDetailGet(ctx, &eapi.FansGroupUserGroupDetailGetReq{GroupId: fansGroupId})
	if err == nil && userGrpDetail.Code == 200 {
		joined = userGrpDetail.Data.FansGroupMemberDetail.Joined
		c.cmd.Printf("[fansgroup] 已加入：Joined=%v, Level=%s (%s)\n", joined, userGrpDetail.Data.FansGroupMemberDetail.Level.Level, userGrpDetail.Data.FansGroupMemberDetail.Level.FanTitle)
	} else if err != nil {
		c.cmd.Printf("[fansgroup] 查询加入状态失败: %v\n", err)
	} else {
		c.cmd.Printf("[fansgroup] 查询加入状态异常: code=%d\n", userGrpDetail.Code)
	}

	if !joined {
		c.cmd.Printf("[fansgroup] 未加入粉丝团，跳过 %q\n", groupName)
		return nil
	}

	missions, err := eapiCli.FansGroupMissionAll(ctx, &eapi.FansGroupMissionAllReq{FansGroupId: fansGroupId})
	if err != nil {
		return fmt.Errorf("获取任务列表失败: %w", err)
	}
	if missions.Code != 200 {
		return fmt.Errorf("获取任务列表失败: code=%d", missions.Code)
	}

	for _, m := range missions.Data.Normal.Data {
		c.cmd.Printf("  [%s] %s (%d/%d)\n", fmtMissionStatus(m.Status, m.CurrentProgress, m.AllProgress), m.Title, m.CurrentProgress, m.AllProgress)
	}
	orig := missions.Data.Originality.Data
	if orig.Title != "" {
		c.cmd.Printf("  [%s] %s (%d/%d) %s\n", fmtMissionStatus(orig.Status, orig.CurrentProgress, orig.AllProgress), orig.Title, orig.CurrentProgress, orig.AllProgress, orig.Subtitle)
	}

	for _, m := range missions.Data.Normal.Data {
		if missionCompleted(m.Status, m.CurrentProgress, m.AllProgress) {
			c.cmd.Printf("[fansgroup] [%s] 已完成，跳过\n", m.Title)
			continue
		}

		switch {
		case strings.Contains(m.Title, "点赞"):
			c.doLikeNotes(ctx, eapiCli, fansGroupId, currentUserId, m)
		case strings.Contains(m.Title, "播放"):
			c.doPlaySong(ctx, cli, weapiCli, m)
		case strings.Contains(m.Title, "分享"):
			c.doShareSong(ctx, eapiCli, m)
		case strings.Contains(m.Title, "笔记") || strings.Contains(m.Title, "发布"):
			c.doPublishNote(ctx, eapiCli, boardId, groupName, m)
		default:
			c.cmd.Printf("[fansgroup] [%s] 未知任务类型，跳过\n", m.Title)
		}
	}

	if orig.Title != "" && !missionCompleted(orig.Status, orig.CurrentProgress, orig.AllProgress) {
		c.doAccelerateTask(ctx, eapiCli, weapiCli, currentUserId, commentCheckToken, missions, orig)
	}

	c.cmd.Println("[fansgroup] 本轮请求处理结束，重新读取实际任务进度")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	updated, err := eapiCli.FansGroupMissionAll(ctx, &eapi.FansGroupMissionAllReq{FansGroupId: fansGroupId})
	if err != nil {
		return fmt.Errorf("刷新乐迷团实际进度失败：%w", err)
	}
	if updated.Code != 200 {
		return fmt.Errorf("刷新乐迷团实际进度失败：code=%d", updated.Code)
	}
	before := make(map[int64]int)
	for _, m := range missions.Data.Normal.Data {
		before[m.MissionId] = m.CurrentProgress
	}
	for _, m := range updated.Data.Normal.Data {
		c.cmd.Printf("  [%s] %s：%d → %d/%d\n", fmtMissionStatus(m.Status, m.CurrentProgress, m.AllProgress), m.Title, before[m.MissionId], m.CurrentProgress, m.AllProgress)
	}
	if after := updated.Data.Originality.Data; after.MissionId == orig.MissionId && after.Title != "" {
		c.cmd.Printf("  [%s] %s：%d → %d/%d\n", fmtMissionStatus(after.Status, after.CurrentProgress, after.AllProgress), after.Subtitle, orig.CurrentProgress, after.CurrentProgress, after.AllProgress)
	}
	return nil
}

func fmtMissionStatus(status string, current, total int) string {
	if missionCompleted(status, current, total) {
		return "已完成"
	}
	return "未完成"
}

func missionCompleted(status string, current, total int) bool {
	return status == "COMPLETED" || (total > 0 && current >= total)
}

func missionRemaining(current, total int) int {
	if total <= 0 {
		return 1
	}
	remaining := total - current
	if remaining <= 0 {
		return 1
	}
	return remaining
}

func (c *FansGroup) doPlaySong(ctx context.Context, cli *api.Client, weapiCli *weapi.Api, m eapi.FansGroupMissionItem) {
	if c.root == nil || c.root.Cfg == nil || c.root.Cfg.FansGroup == nil || !c.root.Cfg.FansGroup.WebSession {
		c.cmd.Println("[fansgroup] 未开启 fansgroup.webSession，仅跳过播放歌曲；其他任务继续执行")
		return
	}
	remaining := missionRemaining(m.CurrentProgress, m.AllProgress)
	songIDs := parseSongIdsFromMission(m)
	if len(songIDs) == 0 {
		c.cmd.Println("[fansgroup] 无法从播放任务解析歌曲，跳过")
		return
	}
	for i := 0; i < remaining; i++ {
		if ctx.Err() != nil {
			return
		}
		songID := songIDs[c.rng.Intn(len(songIDs))]
		songName, source, sourceID, _ := c.fetchFansGroupSongPlaybackMeta(ctx, weapiCli, strconv.FormatInt(songID, 10))
		c.cmd.Printf("[fansgroup] [%d/%d] 播放作品「%s」(songId=%d)\n", i+1, remaining, songName, songID)
		player, err := weapiCli.FansGroupPlayerURL(ctx, &weapi.SongPlayerV1Req{Ids: types.IntsString([]int64{songID}), Level: types.LevelExhigh, EncodeType: "aac"})
		if err != nil {
			c.cmd.Printf("  获取网页播放地址失败：%v\n", err)
			continue
		}
		if player.Code != 200 || len(player.Data) == 0 || player.Data[0].Code != 200 {
			c.cmd.Printf("  播放地址接口拒绝 code=%d\n", player.Code)
			continue
		}
		mediaURL, err := fansGroupMediaURL(player.Data[0].Url)
		if err != nil {
			c.cmd.Printf("  %v，跳过本首上报\n", err)
			continue
		}
		entries := weapi.FansGroupPlaybackLogs(songID, source, sourceID, 0)
		started := time.Now()
		response, err := weapiCli.FansGroupWebLog(ctx, entries[0])
		if err != nil {
			c.cmd.Printf("  startplay 请求失败：%v\n", err)
			continue
		}
		if response.Code != 200 {
			c.cmd.Printf("  startplay 拒绝 code=%d\n", response.Code)
			continue
		}
		if err := fetchFansGroupAudio(ctx, cli, mediaURL); err != nil {
			c.cmd.Printf("  完整音频拉取失败：%v\n", err)
			continue
		}
		// 按实际拉取耗时上报，不伪造整首播放时长，也不影响普通刷歌的整首等待。
		entries = weapi.FansGroupPlaybackLogs(songID, source, sourceID, int(time.Since(started).Round(time.Second)/time.Second))
		response, err = weapiCli.FansGroupWebLog(ctx, entries[1])
		if err != nil {
			c.cmd.Printf("  play 请求失败：%v\n", err)
			continue
		}
		if response.Code != 200 {
			c.cmd.Printf("  play 拒绝 code=%d\n", response.Code)
			continue
		}
		c.cmd.Println("  音频完整拉取，上报接口已接受；是否计入任务以随后刷新进度为准")
		if i < remaining-1 && !waitFansGroup(ctx, time.Duration(2+c.rng.Intn(4))*time.Second) {
			return
		}
	}
}

func waitFansGroup(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func fansGroupMediaURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" {
		return "", fmt.Errorf("媒体地址格式无效")
	}
	if u.Scheme == "http" {
		u.Scheme = "https"
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" || u.Port() != "" || (!strings.HasSuffix(host, ".music.126.net") && !strings.HasSuffix(host, ".music.163.com")) {
		return "", fmt.Errorf("媒体地址不属于支持的网易云 CDN")
	}
	if u.Query().Get("authSecret") == "" {
		return "", fmt.Errorf("网页媒体地址缺少 authSecret，可能未取得有效网页会话")
	}
	return u.String(), nil
}

func fetchFansGroupAudio(ctx context.Context, cli *api.Client, songURL string) error {
	mediaURL, err := fansGroupMediaURL(songURL)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return fmt.Errorf("无法构造音频请求")
	}
	req.Header.Set("Referer", "https://music.163.com/")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", cli.UserAgent(api.CryptoModeWEAPI))
	// 媒体域只使用签名 URL；不携带账号 Cookie，并限制重定向目标。
	httpClient := *cli.GetClient()
	httpClient.Jar = nil
	httpClient.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("音频重定向次数过多")
		}
		if r.URL.Scheme != "https" {
			return fmt.Errorf("拒绝音频重定向到非 HTTPS 地址")
		}
		_, err := fansGroupMediaURL(r.URL.String())
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("音频请求失败（已隐藏签名地址）")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("音频 HTTP 状态码：%d", resp.StatusCode)
	}
	const limit = int64(100 * 1024 * 1024)
	if resp.ContentLength > limit {
		return fmt.Errorf("音频超过 100 MB 安全上限")
	}
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("音频读取中断")
	}
	if n > limit {
		return fmt.Errorf("音频超过 100 MB 安全上限")
	}
	if n == 0 || (resp.ContentLength >= 0 && n != resp.ContentLength) {
		return fmt.Errorf("音频下载不完整")
	}
	return nil
}

func (c *FansGroup) fetchFansGroupSongPlaybackMeta(ctx context.Context, weapiCli *weapi.Api, songId string) (name, source, sourceId string, songTime int) {
	source = "toplist"
	sourceId = ""
	songTime = 3

	detail, err := weapiCli.SongDetail(ctx, &weapi.SongDetailReq{
		C: []weapi.SongDetailReqList{{Id: songId, V: 0}},
	})
	if err != nil {
		c.cmd.Printf("  获取歌曲详情失败，继续播放链路: %v\n", err)
		return name, source, sourceId, songTime
	}
	if detail.Code != 200 || len(detail.Songs) == 0 {
		c.cmd.Printf("  歌曲详情返回异常 code=%d songs=%d，继续播放链路\n", detail.Code, len(detail.Songs))
		return name, source, sourceId, songTime
	}

	song := detail.Songs[0]
	name = song.Name
	if song.Dt > 0 {
		songTime = int(song.Dt / 1000)
	}
	if song.Al.Id > 0 {
		source = "album"
		sourceId = strconv.FormatInt(song.Al.Id, 10)
	}
	return name, source, sourceId, songTime
}

func (c *FansGroup) doPublishNote(ctx context.Context, eapiCli *eapi.Api, boardId, groupName string, m eapi.FansGroupMissionItem) {
	remaining := missionRemaining(m.CurrentProgress, m.AllProgress)
	c.cmd.Printf("[fansgroup] 处理发布笔记任务 [%s]（剩余 %d/%d）\n", m.Title, remaining, m.AllProgress)

	activityInfo := []map[string]interface{}{
		{
			"id":        boardId,
			"type":      3,
			"subType":   11,
			"name":      groupName,
			"desc":      nil,
			"selected":  true,
			"canChange": true,
		},
	}
	activityJSON, _ := json.Marshal(activityInfo)

	n := NewNote(c.root, c.l)
	cfg := c.root.Cfg.Note

	var pics string
	if cfg != nil {
		uniqueURLs := uniqueStrings(n.resolveImageURLs(ctx, cfg.ImageURLs))
		if len(uniqueURLs) > 0 {
			shuffled := shuffleSlice(uniqueURLs)
			for _, u := range shuffled {
				tmpFile, err := downloadImageToTemp(ctx, u)
				if err != nil {
					continue
				}
				pics, err = eapiCli.EventUploadImage(ctx, tmpFile)
				if err != nil {
					c.cmd.Printf("  上传图片失败: %v\n", err)
					c.cmd.Println("  图文笔记图片准备失败，跳过本项任务")
					return
				}
				break
			}
		}
	}
	if pics == "" || pics == "[]" || pics == "null" {
		c.cmd.Println("  没有可用的已上传图片，跳过图文笔记任务；请检查 note.imageUrls")
		return
	}

	autoDelete := true
	if c.root.Cfg.FansGroup != nil && c.root.Cfg.FansGroup.AutoDeleteNote != nil {
		autoDelete = *c.root.Cfg.FansGroup.AutoDeleteNote
	} else if cfg != nil && cfg.AutoDelete != nil {
		autoDelete = *cfg.AutoDelete
	}

	for i := 0; i < remaining; i++ {
		title, msg := c.pickFansGroupNoteText(i)
		c.cmd.Printf("  [%d/%d] 准备发布动态：%q\n", i+1, remaining, title)
		resp, err := eapiCli.EventPublish(ctx, &eapi.EventPublishReq{
			Title:            title,
			Msg:              msg,
			Type:             "noresource",
			Pics:             pics,
			ActivityInfoList: string(activityJSON),
		})
		if err != nil {
			c.cmd.Printf("  [%d/%d] 发布动态失败: %v\n", i+1, remaining, err)
			continue
		}
		if resp.Code != 200 {
			c.cmd.Printf("  [%d/%d] 发布动态返回异常 code=%d\n", i+1, remaining, resp.Code)
			continue
		}
		c.cmd.Printf("  [%d/%d] 动态发布成功，id=%d\n", i+1, remaining, resp.Id)

		if autoDelete {
			delay := 5 + c.rng.Intn(26)
			c.cmd.Printf("  %d 秒后删除刚发布的动态...\n", delay)
			time.Sleep(time.Duration(delay) * time.Second)
			delResp, err := eapiCli.EventDelete(ctx, &eapi.EventDeleteReq{Id: resp.Id})
			if err != nil || delResp.Code != 200 {
				c.cmd.Printf("  删除动态失败: %v\n", err)
			} else {
				c.cmd.Println("  删除动态成功")
			}
		}

		if i < remaining-1 {
			time.Sleep(time.Duration(2+c.rng.Intn(4)) * time.Second)
		}
	}
}

func (c *FansGroup) pickFansGroupNoteText(index int) (string, string) {
	cfg := c.root.Cfg.Note

	var title, msg string
	if cfg != nil {
		if len(cfg.Titles) > 0 {
			title = cfg.Titles[c.rng.Intn(len(cfg.Titles))]
		}
		if len(cfg.Messages) > 0 {
			msg = cfg.Messages[c.rng.Intn(len(cfg.Messages))]
		}
	}
	if title == "" {
		title = "Music Share"
	}
	if msg == "" {
		msg = "Share a nice song"
	}
	return title, withFansGroupPublishNonce(msg, index+1, time.Now().UnixNano())
}

func withFansGroupPublishNonce(msg string, index int, nonce int64) string {
	if msg == "" {
		msg = "Share a nice song"
	}
	for len([]rune(msg)) < 10 {
		msg += " more text"
	}
	return fmt.Sprintf("%s\n分享编号 %d-%06d", msg, index, nonce%1000000)
}

func (c *FansGroup) doShareSong(ctx context.Context, eapiCli *eapi.Api, m eapi.FansGroupMissionItem) {
	remaining := missionRemaining(m.CurrentProgress, m.AllProgress)
	c.cmd.Printf("[fansgroup] 处理分享任务 [%s]（剩余 %d/%d）\n", m.Title, remaining, m.AllProgress)

	resourceId, resourceType := parseShareParamsFromButton(m.Button.Url)
	if resourceId == "" {
		c.cmd.Println("[fansgroup] 未找到资源ID，跳过分享任务")
		return
	}

	for i := 0; i < remaining; i++ {
		c.cmd.Printf("  [%d/%d] 资源参数：resourceId=%s resourceType=%s\n", i+1, remaining, resourceId, resourceType)
		resp, err := eapiCli.FansGroupMissionForwardProgress(ctx, &eapi.FansGroupMissionForwardProgressReq{
			ResourceId:   resourceId,
			Action:       "share",
			FansGroupId:  "null",
			ResourceType: resourceType,
		})
		if err != nil {
			c.cmd.Printf("  [%d/%d] 提交分享进度失败: %v\n", i+1, remaining, err)
			continue
		}
		if resp.Code != 200 {
			c.cmd.Printf("  [%d/%d] 分享进度返回异常 code=%d\n", i+1, remaining, resp.Code)
			continue
		}
		c.cmd.Printf("  [%d/%d] 分享进度提交成功\n", i+1, remaining)
		if i < remaining-1 {
			time.Sleep(time.Duration(2+c.rng.Intn(4)) * time.Second)
		}
	}
	c.cmd.Println("  分享任务完成")
}

func (c *FansGroup) doLikeNotes(ctx context.Context, eapiCli *eapi.Api, fansGroupId string, currentUserId int64, m eapi.FansGroupMissionItem) {
	remaining := missionRemaining(m.CurrentProgress, m.AllProgress)
	c.cmd.Printf("[fansgroup] 处理点赞任务 [%s]（剩余 %d）\n", m.Title, remaining)

	feedResp, err := eapiCli.FansGroupFeedRecommend(ctx, &eapi.FansGroupFeedRecommendReq{
		FansGroupId: fansGroupId,
		Size:        fmt.Sprintf("%d", remaining+15),
	})
	if err != nil || feedResp.Code != 200 {
		c.cmd.Printf("  获取推荐列表失败: %v\n", err)
		return
	}

	var feedData struct {
		Records []struct {
			ThreadId string `json:"threadId"`
			User     struct {
				UserId int64 `json:"userId"`
			} `json:"user"`
			Info struct {
				Liked bool `json:"liked"`
			} `json:"info"`
		} `json:"records"`
	}

	rawBytes, _ := json.Marshal(feedResp.Data)
	if err := json.Unmarshal(rawBytes, &feedData); err != nil {
		c.cmd.Printf("  解析推荐列表失败: %v\n", err)
		return
	}

	var threadIds []string
	for _, r := range feedData.Records {
		if r.ThreadId != "" && !r.Info.Liked && r.User.UserId != currentUserId {
			threadIds = append(threadIds, r.ThreadId)
		}
	}

	if len(threadIds) == 0 {
		c.cmd.Println("[fansgroup] 没有可点赞的帖子，跳过")
		return
	}

	success := 0
	appLogExt := fmt.Sprintf(`{"multiRefer":"{\\\"%s:fans_group\\\":\\\"\\\"}","addRefer":"%s:fans_group"}`, fansGroupId, fansGroupId)
	for i := 0; i < remaining && i < len(threadIds); i++ {
		resp, err := eapiCli.ResourceLike(ctx, &eapi.ResourceLikeReq{
			ThreadId:  threadIds[i],
			AppLogExt: appLogExt,
		})
		if err != nil || resp.Code != 200 {
			c.cmd.Printf("  [%d/%d] like failed\n", i+1, remaining)
			continue
		}
		success++
		c.cmd.Printf("  [%d/%d] like success\n", i+1, remaining)
		if i < remaining-1 && i < len(threadIds)-1 {
			time.Sleep(time.Duration(1+c.rng.Intn(3)) * time.Second)
		}
	}
	c.cmd.Printf("[fansgroup] 点赞完成：%d/%d\n", success, remaining)
}

func (c *FansGroup) doCollectSong(ctx context.Context, eapiCli *eapi.Api, title string, remaining int, songIds []int64) {
	if remaining <= 0 {
		remaining = 1
	}
	songIds = uniqueInt64s(songIds)
	if len(songIds) == 0 {
		songIds = []int64{186016}
		c.cmd.Printf("[fansgroup] [%s] 未解析到任务歌曲，使用默认歌曲 %d\n", title, songIds[0])
	}

	c.cmd.Printf("[fansgroup] 处理收藏任务 [%s]（剩余 %d）\n", title, remaining)
	for i := 0; i < remaining; i++ {
		songId := songIds[c.rng.Intn(len(songIds))]
		if !c.collectOneSong(ctx, eapiCli, songId, i+1, remaining) {
			continue
		}
		if i < remaining-1 {
			time.Sleep(time.Duration(2+c.rng.Intn(4)) * time.Second)
		}
	}
}

func (c *FansGroup) collectOneSong(ctx context.Context, eapiCli *eapi.Api, songId int64, index, total int) bool {
	songIdStr := fmt.Sprintf("%d", songId)
	c.cmd.Printf("  [%d/%d] 准备收藏歌曲 %s\n", index, total, songIdStr)

	preResp, err := eapiCli.SongLike(ctx, &eapi.SongLikeReq{
		TrackId:    songIdStr,
		Like:       "false",
		Time:       "3",
		CheckToken: "",
	})
	if err != nil {
		c.cmd.Printf("  [%d/%d] 预取消收藏请求失败，继续尝试收藏: %v\n", index, total, err)
	} else if preResp.Code == 200 {
		c.cmd.Printf("  [%d/%d] 已先归一化为未收藏状态\n", index, total)
	} else {
		c.cmd.Printf("  [%d/%d] 预取消收藏返回 code=%d，继续尝试收藏\n", index, total, preResp.Code)
	}

	time.Sleep(time.Duration(1+c.rng.Intn(3)) * time.Second)
	likeResp, err := eapiCli.SongLike(ctx, &eapi.SongLikeReq{
		TrackId:    songIdStr,
		Like:       "true",
		Time:       "3",
		CheckToken: "",
	})
	if err != nil {
		c.cmd.Printf("  [%d/%d] 收藏歌曲失败: %v\n", index, total, err)
		return false
	}
	if likeResp.Code != 200 {
		c.cmd.Printf("  [%d/%d] 收藏歌曲返回异常 code=%d\n", index, total, likeResp.Code)
		return false
	}
	c.cmd.Printf("  [%d/%d] 收藏歌曲成功\n", index, total)

	delay := 3 + c.rng.Intn(8)
	c.cmd.Printf("  %d 秒后取消收藏歌曲...\n", delay)
	time.Sleep(time.Duration(delay) * time.Second)

	unlikeResp, err := eapiCli.SongLike(ctx, &eapi.SongLikeReq{
		TrackId:    songIdStr,
		Like:       "false",
		Time:       "3",
		CheckToken: "",
	})
	if err != nil || unlikeResp.Code != 200 {
		c.cmd.Printf("  取消收藏歌曲失败: %v\n", err)
		return true
	}
	c.cmd.Println("  取消收藏歌曲成功")
	return true
}

func (c *FansGroup) doAccelerateTask(ctx context.Context, eapiCli *eapi.Api, weapiCli *weapi.Api, uid int64, commentCheckToken string, missions *eapi.FansGroupMissionAllResp, orig eapi.FansGroupMissionOriginality) {
	c.cmd.Printf("[fansgroup] 处理加速任务 [%s] %s...\n", orig.Title, orig.Subtitle)
	if strings.Contains(orig.Subtitle, "评论") {
		if strings.TrimSpace(commentCheckToken) == "" {
			c.cmd.Println("[fansgroup] 当前账号未配置 fansgroup.commentCheckTokens 网页评论短效令牌，跳过新建评论；其他任务继续")
			return
		}
		target, err := fansGroupCommentTarget(orig)
		if err != nil {
			c.cmd.Printf("[fansgroup] %v\n", err)
			return
		}
		if target.MlogID != "" {
			videoID, code, err := weapiCli.FansGroupMlogVideo(ctx, target.MlogID)
			if err != nil || code != 200 || !fansGroupResourceID.MatchString(videoID) {
				c.cmd.Printf("[fansgroup] 无法确认笔记对应的视频目标，跳过评论（code=%d）\n", code)
				return
			}
			target.ThreadID = "R_VI_62_" + videoID
		}
		delay := time.Duration(3+c.rng.Intn(5)) * time.Second
		c.cmd.Printf("[fansgroup] 评论任务指定视频笔记 %s，%d 秒后删除\n", target.ThreadID, int(delay/time.Second))
		if err := runFansGroupComment(ctx, c.commentReceiptPath(uid), uid, target.ThreadID, fansGroupCommentClient{api: weapiCli, token: commentCheckToken}, delay); err != nil {
			c.cmd.Printf("[fansgroup] 评论任务：%v\n", err)
			c.root.ReportFailure(strconv.FormatInt(uid, 10), "fansgroup", err)
		} else {
			c.cmd.Println("[fansgroup] 评论已发表并删除，实际任务进度随后刷新确认")
		}
		return
	}

	if strings.Contains(orig.Subtitle, "收藏") || strings.Contains(orig.Subtitle, "红心") {
		songIds := parseSongIdsFromOriginality(orig)
		if len(songIds) == 0 {
			songIds = parseFallbackSongIdsFromMissions(missions)
		}
		c.doCollectSong(ctx, eapiCli, orig.Subtitle, missionRemaining(orig.CurrentProgress, orig.AllProgress), songIds)
		return
	}

	songIds := parseFallbackSongIdsFromMissions(missions)
	if len(songIds) == 0 {
		c.cmd.Println("[fansgroup] 没有可用歌曲，跳过加速任务")
		return
	}

	songId := songIds[c.rng.Intn(len(songIds))]
	songIdStr := fmt.Sprintf("%d", songId)
	c.cmd.Printf("  处理歌曲 %s...\n", songIdStr)

	likeResp, err := eapiCli.SongLike(ctx, &eapi.SongLikeReq{
		TrackId: songIdStr,
		Like:    "true",
		Time:    "3",
	})
	if err != nil {
		c.cmd.Printf("  点赞失败: %v\n", err)
		return
	}
	if likeResp.Code != 200 {
		c.cmd.Printf("  点赞返回异常 code=%d\n", likeResp.Code)
		return
	}
	c.cmd.Println("  点赞成功")

	delay := 3 + c.rng.Intn(5)
	c.cmd.Printf("  %d 秒后执行取消点赞...\n", delay)
	time.Sleep(time.Duration(delay) * time.Second)

	unlikeResp, err := eapiCli.SongLike(ctx, &eapi.SongLikeReq{
		TrackId: songIdStr,
		Like:    "false",
		Time:    "3",
	})
	if err != nil || unlikeResp.Code != 200 {
		c.cmd.Println("  unlike failed")
	} else {
		c.cmd.Println("  unlike success")
	}
}

// parseSongIdsFromButton 从 button.url 里提取 songIds、songId、trackId 或歌曲 resourceId。
func parseSongIdsFromButton(buttonUrl string) []int64 {
	return parseSongIdsFromJSONText(buttonUrl)
}

func parseSongIdsFromMission(m eapi.FansGroupMissionItem) []int64 {
	var ids []int64
	ids = append(ids, parseSongIdsFromJSONText(m.Button.Url)...)
	ids = append(ids, parseSongIdsFromJSONText(m.IconUi.TargetUrl)...)
	ids = append(ids, parseSongIdsFromJSONText(m.LogInfo)...)
	return uniqueInt64s(ids)
}

func parseSongIdsFromOriginality(orig eapi.FansGroupMissionOriginality) []int64 {
	var ids []int64
	ids = append(ids, parseSongIdsFromJSONText(orig.Button.Url)...)
	ids = append(ids, parseSongIdsFromJSONText(orig.LogInfo)...)
	if orig.MissionDetail != nil {
		raw, _ := json.Marshal(orig.MissionDetail)
		ids = append(ids, parseSongIdsFromJSONText(string(raw))...)
	}
	return uniqueInt64s(ids)
}

func parseFallbackSongIdsFromMissions(missions *eapi.FansGroupMissionAllResp) []int64 {
	if missions == nil {
		return nil
	}
	var ids []int64
	for _, m := range missions.Data.Normal.Data {
		ids = append(ids, parseSongIdsFromMission(m)...)
	}
	return uniqueInt64s(ids)
}

func parseSongIdsFromJSONText(raw string) []int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var v interface{}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return extractSongIDsFromText(raw)
	}

	var ids []int64
	walkSongIDs(v, &ids)
	return uniqueInt64s(ids)
}

func walkSongIDs(v interface{}, ids *[]int64) {
	switch x := v.(type) {
	case map[string]interface{}:
		for _, key := range []string{"songIds", "songId", "trackIds", "trackId"} {
			if value, ok := x[key]; ok {
				appendSongIDsFromValue(value, ids)
			}
		}
		if value, ok := x["resourceId"]; ok && isSongResourceType(x["resourceType"]) {
			appendSongIDsFromValue(value, ids)
		}
		for _, value := range x {
			walkSongIDs(value, ids)
		}
	case []interface{}:
		for _, item := range x {
			walkSongIDs(item, ids)
		}
	case string:
		if nested := parseSongIdsFromJSONText(x); len(nested) > 0 {
			*ids = append(*ids, nested...)
		}
	}
}

func appendSongIDsFromValue(v interface{}, ids *[]int64) {
	switch x := v.(type) {
	case json.Number:
		if id, err := x.Int64(); err == nil && id > 0 {
			*ids = append(*ids, id)
		}
	case float64:
		if x > 0 {
			*ids = append(*ids, int64(x))
		}
	case int64:
		if x > 0 {
			*ids = append(*ids, x)
		}
	case int:
		if x > 0 {
			*ids = append(*ids, int64(x))
		}
	case string:
		s := strings.TrimSpace(x)
		if id, err := strconv.ParseInt(s, 10, 64); err == nil && id > 0 {
			*ids = append(*ids, id)
			return
		}
		*ids = append(*ids, parseSongIdsFromJSONText(s)...)
	case []interface{}:
		for _, item := range x {
			appendSongIDsFromValue(item, ids)
		}
	default:
		raw, err := json.Marshal(x)
		if err == nil {
			*ids = append(*ids, parseSongIdsFromJSONText(string(raw))...)
		}
	}
}

func isSongResourceType(v interface{}) bool {
	switch x := v.(type) {
	case nil:
		return true
	case json.Number:
		return x.String() == "4"
	case float64:
		return int64(x) == 4
	case string:
		x = strings.TrimSpace(strings.ToLower(x))
		return x == "" || x == "4" || x == "song"
	default:
		return fmt.Sprintf("%v", x) == "4"
	}
}

func extractSongIDsFromText(raw string) []int64 {
	var ids []int64
	for _, pattern := range fansGroupSongIDPatterns {
		matches := pattern.FindAllStringSubmatch(raw, -1)
		for _, match := range matches {
			if len(match) < 2 {
				continue
			}
			if id, err := strconv.ParseInt(match[1], 10, 64); err == nil && id > 0 {
				ids = append(ids, id)
			}
		}
	}
	return uniqueInt64s(ids)
}

func uniqueInt64s(values []int64) []int64 {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(values))
	unique := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

// parseShareParamsFromButton 从 button.url 里的 actionCustomParams 提取 resourceId 和 resourceType。
func parseShareParamsFromButton(buttonUrl string) (string, string) {
	var btn struct {
		ActionCustomParams struct {
			ProgressParams struct {
				ResourceId   string      `json:"resourceId"`
				ResourceType interface{} `json:"resourceType"`
			} `json:"progressParams"`
		} `json:"actionCustomParams"`
	}
	if err := json.Unmarshal([]byte(buttonUrl), &btn); err != nil {
		return "", "4"
	}
	rt := "4"
	if btn.ActionCustomParams.ProgressParams.ResourceType != nil {
		rt = fmt.Sprintf("%v", btn.ActionCustomParams.ProgressParams.ResourceType)
	}
	return btn.ActionCustomParams.ProgressParams.ResourceId, rt
}

func extractThreadIds(data interface{}) []string {
	var ids []string
	raw, _ := json.Marshal(data)
	walkJSON(raw, &ids)
	return ids
}

func walkJSON(raw []byte, result *[]string) {
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err == nil {
		if tid, ok := m["threadId"].(string); ok && tid != "" && strings.HasPrefix(tid, "A_EV_") {
			*result = append(*result, tid)
			return
		}
		for _, v := range m {
			b, _ := json.Marshal(v)
			walkJSON(b, result)
		}
		return
	}
	var arr []interface{}
	if err := json.Unmarshal(raw, &arr); err == nil {
		for _, item := range arr {
			b, _ := json.Marshal(item)
			walkJSON(b, result)
		}
	}
}

package ncmm

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/3899/ncmm/api/weapi"
)

// playSourceTokens 同时供歌曲 ID 和歌单来源使用，URL 内的协议和路由片段不作为注释。
func playSourceTokens(text string) []string {
	var tokens []string
	for _, line := range strings.Split(strings.TrimPrefix(text, "\ufeff"), "\n") {
		for _, token := range strings.FieldsFunc(line, func(r rune) bool {
			return unicode.IsSpace(r) || strings.ContainsRune(",，;；", r)
		}) {
			if strings.HasPrefix(token, "#") || strings.HasPrefix(token, "//") {
				break
			}
			comment := -1
			if !strings.HasPrefix(token, "https://") && !strings.HasPrefix(token, "http://") {
				comment = strings.Index(token, "#")
				if index := strings.Index(token, "//"); index >= 0 && (comment < 0 || index < comment) {
					comment = index
				}
			}
			if comment >= 0 {
				token = token[:comment]
			}
			if token != "" {
				tokens = append(tokens, token)
			}
			if comment >= 0 {
				break
			}
		}
	}
	return tokens
}

func positivePlayID(value string) (string, bool) {
	if value == "" {
		return "", false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return "", false
		}
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return "", false
	}
	return strconv.FormatInt(id, 10), true
}

func parsePlaySongIDs(text string) []string {
	var ids []string
	for _, token := range playSourceTokens(text) {
		if id, ok := positivePlayID(token); ok {
			ids = append(ids, id)
		}
	}
	return uniqueStrings(ids)
}

func (c *PlayIds) collectPlaySources() ([]string, []string) {
	ids := parsePlaySongIDs(c.opts.Ids)
	files := []string{c.opts.IdsFile}
	playlists := append([]string(nil), c.opts.PlaylistIDs...)
	if cfg := c.root.Cfg.PlayIds; cfg != nil {
		ids = append(ids, parsePlaySongIDs(cfg.IDs)...)
		files = append(files, cfg.IDsFile...)
		playlists = append(playlists, cfg.PlaylistIDs...)
	}
	for _, file := range uniqueStrings(files) {
		if file == "" {
			continue
		}
		fileIDs, err := parseIdsFromFile(file)
		if err != nil {
			c.log("[WARN] 读取歌曲 ID 文件 (%s) 失败: %s，该来源将被跳过", file, err)
			continue
		}
		ids = append(ids, fileIDs...)
	}
	var playlistSources []string
	for _, source := range playlists {
		playlistSources = append(playlistSources, playSourceTokens(source)...)
	}
	return uniqueStrings(ids), uniqueStrings(playlistSources)
}

func parsePlaylistSourceURL(source string) (*url.URL, error) {
	u, err := url.Parse(source)
	if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("请使用有效歌单 ID 或网易云歌单链接")
	}
	if u.Port() != "" && !(u.Scheme == "https" && u.Port() == "443") && !(u.Scheme == "http" && u.Port() == "80") {
		return nil, fmt.Errorf("歌单链接不能使用自定义端口")
	}
	switch strings.ToLower(u.Hostname()) {
	case "music.163.com", "y.music.163.com", "163cn.tv":
		return u, nil
	default:
		return nil, fmt.Errorf("仅支持网易云音乐官方歌单链接")
	}
}

func playlistIDFromURL(u *url.URL) (string, error) {
	if strings.HasPrefix(u.Fragment, "/") {
		fragment, err := url.Parse(u.Fragment)
		if err != nil {
			return "", fmt.Errorf("歌单链接路由无效")
		}
		u = fragment
	}
	path := strings.TrimSuffix(u.Path, "/")
	var value string
	switch path {
	case "/playlist", "/m/playlist":
		values := u.Query()["id"]
		if len(values) == 1 {
			value = values[0]
		}
	default:
		if strings.HasPrefix(path, "/playlist/") {
			value = strings.TrimPrefix(path, "/playlist/")
		}
	}
	if id, ok := positivePlayID(value); ok {
		return id, nil
	}
	return "", fmt.Errorf("链接未包含有效歌单 ID，请确认分享的是歌单而非歌曲")
}

// 短链只访问官方域名且不发送账号 Cookie，每次跳转都重新验证目的地址。
func resolvePlaylistSource(ctx context.Context, source string, client *http.Client) (string, error) {
	if id, ok := positivePlayID(source); ok {
		return id, nil
	}
	u, err := parsePlaylistSourceURL(source)
	if err != nil {
		return "", err
	}
	if strings.ToLower(u.Hostname()) != "163cn.tv" {
		return playlistIDFromURL(u)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	shortClient := *client
	shortClient.Jar = nil
	shortClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for range 5 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return "", fmt.Errorf("创建歌单短链请求失败")
		}
		resp, err := shortClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("展开歌单短链失败")
		}
		_ = resp.Body.Close()
		if resp.StatusCode < 300 || resp.StatusCode >= 400 {
			return "", fmt.Errorf("歌单短链未返回有效跳转，状态码 %d", resp.StatusCode)
		}
		next, err := resp.Location()
		if err != nil {
			return "", fmt.Errorf("歌单短链缺少有效跳转地址")
		}
		u, err = parsePlaylistSourceURL(next.String())
		if err != nil {
			return "", err
		}
		if strings.ToLower(u.Hostname()) != "163cn.tv" {
			return playlistIDFromURL(u)
		}
	}
	return "", fmt.Errorf("歌单短链跳转次数过多")
}

type playPlaylistReader interface {
	PlaylistDetail(context.Context, *weapi.PlaylistDetailReq) (*weapi.PlaylistDetailResp, error)
}

func (c *PlayIds) mergePlaylistSongs(ctx context.Context, request playPlaylistReader, songIDs, sources []string) ([]string, error) {
	ids := append([]string(nil), songIDs...)
	seen := make(map[string]bool)
	for index, source := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		playlistID, err := resolvePlaylistSource(ctx, source, httpClient)
		if err != nil {
			c.log("[WARN] 歌单来源第 %d 项无效: %s，该来源将被跳过", index+1, err)
			continue
		}
		if seen[playlistID] {
			continue
		}
		seen[playlistID] = true
		detail, err := request.PlaylistDetail(ctx, &weapi.PlaylistDetailReq{Id: playlistID})
		if err != nil {
			c.log("[WARN] 读取歌单 %s 失败: %s，该来源将被跳过", playlistID, err)
			continue
		}
		if detail == nil || detail.Code != 200 {
			c.log("[WARN] 歌单 %s 不可访问或已失效，该来源将被跳过", playlistID)
			continue
		}
		var playlistIDs []string
		// tracks 可能只有前几首详情，trackIds 才是完整歌曲列表。
		for _, track := range detail.Playlist.TrackIds {
			if track.Id > 0 {
				playlistIDs = append(playlistIDs, strconv.FormatInt(track.Id, 10))
			}
		}
		if len(detail.Playlist.TrackIds) == 0 {
			for _, track := range detail.Playlist.Tracks {
				if track.Id > 0 {
					playlistIDs = append(playlistIDs, strconv.FormatInt(track.Id, 10))
				}
			}
		}
		playlistIDs = uniqueStrings(playlistIDs)
		if len(playlistIDs) == 0 {
			c.log("[WARN] 歌单「%s」(%s) 没有可用歌曲，该来源将被跳过", detail.Playlist.Name, playlistID)
			continue
		}
		if detail.Playlist.TrackCount > int64(len(playlistIDs)) {
			c.log("[WARN] 歌单「%s」(%s) 当前账号仅获得 %d/%d 首，保留可访问歌曲", detail.Playlist.Name, playlistID, len(playlistIDs), detail.Playlist.TrackCount)
		} else {
			c.log("读取歌单「%s」(%s): %d 首歌曲", detail.Playlist.Name, playlistID, len(playlistIDs))
		}
		ids = append(ids, playlistIDs...)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return uniqueStrings(ids), nil
}

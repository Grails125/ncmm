package ncmm

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/3899/ncmm/api/eapi"
)

var fansGroupResourceID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func validFansGroupThread(thread string) bool {
	return strings.HasPrefix(thread, "R_VI_62_") && fansGroupResourceID.MatchString(strings.TrimPrefix(thread, "R_VI_62_"))
}

type fansGroupCommentResource struct{ ThreadID, MlogID string }

// 只接受任务明确指定的视频/笔记标识；不从歌单、通用 id 或推荐列表猜测评论对象。
func fansGroupCommentTarget(orig eapi.FansGroupMissionOriginality) (fansGroupCommentResource, error) {
	found := map[fansGroupCommentResource]bool{}
	add := func(key, value string) {
		if key == "threadId" && validFansGroupThread(value) {
			found[fansGroupCommentResource{ThreadID: value}] = true
		}
		if !fansGroupResourceID.MatchString(value) {
			return
		}
		if key == "videoId" {
			found[fansGroupCommentResource{ThreadID: "R_VI_62_" + value}] = true
		}
		if key == "mlogId" {
			found[fansGroupCommentResource{MlogID: value}] = true
		}
	}
	var walk func(any, int)
	walk = func(value any, depth int) {
		if depth > 8 {
			return
		}
		switch v := value.(type) {
		case map[string]any:
			for key, item := range v {
				if str, ok := item.(string); ok {
					add(key, str)
				}
				walk(item, depth+1)
			}
		case []any:
			for _, item := range v {
				walk(item, depth+1)
			}
		case string:
			if len(v) > 32768 {
				return
			}
			var nested any
			if json.Unmarshal([]byte(v), &nested) == nil {
				if _, ok := nested.(string); !ok {
					walk(nested, depth+1)
				}
				return
			}
			u, err := url.Parse(v)
			if err != nil {
				return
			}
			if u.Scheme != "orpheus" && !((u.Scheme == "https" || u.Scheme == "http") && (u.Hostname() == "music.163.com" || strings.HasSuffix(u.Hostname(), ".music.163.com"))) {
				return
			}
			if strings.HasPrefix(u.Fragment, "/") {
				fragment, _ := url.Parse(u.Fragment)
				u.Path, u.RawQuery = fragment.Path, fragment.RawQuery
			}
			q := u.Query()
			for _, key := range []string{"threadId", "videoId", "mlogId"} {
				add(key, q.Get(key))
			}
			path := strings.Trim(u.Path, "/")
			if u.Scheme == "orpheus" {
				path = u.Host + "/" + path
			}
			parts := strings.Split(strings.Trim(path, "/"), "/")
			if len(parts) > 0 && (parts[0] == "video" || parts[0] == "mlog") {
				key := parts[0] + "Id"
				id := q.Get("id")
				if len(parts) == 2 {
					id = parts[1]
				}
				add(key, id)
			}
		}
	}
	walk(orig.Button.Url, 0)
	walk(orig.MissionDetail, 0)
	if len(found) != 1 {
		return fansGroupCommentResource{}, fmt.Errorf("任务未提供唯一、可识别的视频笔记目标，跳过评论")
	}
	for target := range found {
		return target, nil
	}
	return fansGroupCommentResource{}, fmt.Errorf("未识别评论目标")
}

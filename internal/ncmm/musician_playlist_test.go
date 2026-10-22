package ncmm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/config"
	cookiepkg "github.com/3899/ncmm/pkg/cookie"
	"github.com/3899/ncmm/pkg/log"
)

func TestMusicianPlaySourcesInheritPlaylistsAndKeepExclusiveOverride(t *testing.T) {
	root := &config.PlayIdsConfig{IDs: "10，11", IDsFile: config.StringOrSlice{filepath.Join(t.TempDir(), "missing-ids.txt")}, PlaylistIDs: config.StringOrSlice{"100;101", "100", "https://music.163.com/#/playlist?id=102"}}
	ids, playlists := musicianPlaySources(config.MusicianPlayConf{}, root)
	if !reflect.DeepEqual(ids, []string{"10", "11"}) || !reflect.DeepEqual(playlists, []string{"100", "101", "https://music.163.com/#/playlist?id=102"}) {
		t.Fatalf("inherited sources = %v, %v", ids, playlists)
	}
	ids, playlists = musicianPlaySources(config.MusicianPlayConf{IDs: "20；20"}, root)
	if !reflect.DeepEqual(ids, []string{"20"}) || len(playlists) != 0 {
		t.Fatalf("musician-specific sources must remain exclusive, got %v, %v", ids, playlists)
	}
}

func TestMusicianInheritedPlaylistsUseEachListeningSessionAndCountOnlyCandidates(t *testing.T) {
	root := &Root{Cfg: &config.Config{
		PlayIds: &config.PlayIdsConfig{IDs: "10", PlaylistIDs: config.StringOrSlice{"100", "101"}},
		MixPlay: &config.MixPlayConf{Enabled: true, DailyRecommendRatio: 0.3, CountTarget: true},
	}}
	songIDs, playlists := musicianPlaySources(config.MusicianPlayConf{}, root.Cfg.PlayIds)
	for _, accountID := range []int64{101, 202} {
		t.Run(strconv.FormatInt(accountID, 10), func(t *testing.T) {
			accountCookie := fmt.Sprintf("fixture-listening-account-%d", accountID)
			cookiePath := filepath.Join(t.TempDir(), "cookie.json")
			if err := os.WriteFile(cookiePath, []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			client, err := api.NewClient(&api.Config{Cookie: cookiepkg.Config{Filepath: cookiePath, ReadOnly: true}}, log.Default)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })
			origin, _ := url.Parse("https://music.163.com")
			client.GetClient().Jar.SetCookies(origin, []*http.Cookie{{Name: "MUSIC_U", Value: accountCookie}})
			playlistRequests, reports := 0, 0
			client.GetClient().Transport = playIdsRoleTransport(func(request *http.Request) (*http.Response, error) {
				var body string
				switch request.URL.Path {
				case "/weapi/w/nuser/account/get":
					body = fmt.Sprintf(`{"code":200,"account":{"id":%d},"profile":{"nickname":"fixture"}}`, accountID)
				case "/api/v6/playlist/detail":
					cookie, err := request.Cookie("MUSIC_U")
					if err != nil || cookie.Value != accountCookie {
						t.Fatalf("playlist request did not use current listening account")
					}
					playlistRequests++
					if playlistRequests == 1 {
						body = `{"code":403}`
					} else {
						body = fmt.Sprintf(`{"code":200,"playlist":{"name":"fixture","trackIds":[{"id":10},{"id":%d},{"id":99}]}}`, accountID)
					}
				case "/weapi/v3/song/detail":
					body = fmt.Sprintf(`{"code":200,"songs":[{"id":10,"name":"static","dt":1000,"ar":[{"id":500}]},{"id":%d,"name":"playlist","dt":1000,"ar":[{"id":500}]},{"id":99,"name":"own","dt":1000,"ar":[{"id":500},{"id":900}]}]}`, accountID)
				case "/eapi/nmusician/workbench/musician/role/get":
					body = fmt.Sprintf(`{"code":200,"data":{"userId":%d,"isMusician":true,"artistId":900}}`, accountID)
				case "/weapi/v3/discovery/recommend/songs":
					body = `{"code":200,"data":{"dailySongs":[{"id":20,"name":"mixed recommendation","dt":1000,"ar":[{"id":500}]}]}}`
				case "/weapi/song/enhance/player/url/v1":
					body = `{"code":200,"data":[{"url":"https://m7.music.126.net/fixture.mp3"}]}`
				case "/fixture.mp3":
					body = "fixture audio"
				case "/weapi/feedback/weblog":
					reports++
					body = `{"code":200}`
				case "/weapi/artist/sub":
					body = `{"code":200}`
				default:
					t.Fatalf("unexpected outbound request %s", request.URL.Path)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})
			player := NewPlayIds(root, log.Default)
			player.sharedDB = newPlayStatsMemoryDB()
			if err := player.sharedDB.Set(t.Context(), playIdsDailyTargetKey(strconv.FormatInt(accountID, 10)), "100"); err != nil {
				t.Fatal(err)
			}
			player.opts = PlayIdsOpts{DailyMin: 100, DailyMax: 100, RunMin: 3, RunMax: 3, GapMin: 1, GapMax: 1}
			player.cmd.SetOut(&bytes.Buffer{})
			played, err := player.executeForClient(t.Context(), client, "fixture-account.json", songIDs, playlists...)
			if err != nil || played != 2 || reports != 3 || playlistRequests != 2 {
				t.Fatalf("candidate plays=%d, reports=%d, playlist requests=%d, err=%v; want 2/3/2", played, reports, playlistRequests, err)
			}
		})
	}
}

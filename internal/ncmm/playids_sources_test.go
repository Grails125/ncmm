package ncmm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/pkg/log"
)

func TestPlayIdsFileAcceptsSeparatorsCommentsAndOnlyPositiveIDs(t *testing.T) {
	file := filepath.Join(t.TempDir(), "songs.txt")
	data := "\ufeff# 停用整行\n001，2；3;4 // 临时停用 99\n5# 行内备注\n//6\n7 8\n0,-9,+10,1.5,abc,9223372036854775808\nhttps://music.163.com/song?id=11\n"
	if err := os.WriteFile(file, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := parseIdsFromFile(file)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"1", "2", "3", "4", "5", "7", "8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("song IDs = %v; want %v", got, want)
	}
}

func TestPlaylistSourcesAcceptOfficialRoutesAndRejectOtherURLs(t *testing.T) {
	for _, tc := range []struct {
		source string
		want   string
	}{
		{source: "00123", want: "123"},
		{source: "https://music.163.com/playlist?id=123&userid=88", want: "123"},
		{source: "https://music.163.com/#/playlist?id=123", want: "123"},
		{source: "https://y.music.163.com/m/playlist?id=123&uct2=share", want: "123"},
		{source: "https://music.163.com/playlist/123/", want: "123"},
		{source: "https://music.163.com/song?id=123"},
		{source: "https://attacker.example/playlist?id=123"},
		{source: "https://music.163.com.attacker.example/playlist?id=123"},
		{source: "https://music.163.com@attacker.example/playlist?id=123"},
		{source: "https://user:password@music.163.com/playlist?id=123"},
		{source: "http://127.0.0.1/playlist?id=123"},
		{source: "https://music.163.com:8443/playlist?id=123"},
		{source: "https://music.163.com/playlist?id=123&id=456"},
		{source: "https://music.163.com/playlist?id=0"},
		{source: "-123"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			client := &http.Client{Transport: playIdsRoleTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("full playlist links and invalid inputs must not make HTTP requests")
				return nil, nil
			})}
			got, err := resolvePlaylistSource(t.Context(), tc.source, client)
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("resolvePlaylistSource() = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	got := playSourceTokens("123，https://music.163.com/#/playlist?id=456 // 停用 789\nhttps://y.music.163.com/m/playlist?id=10;11#备注")
	want := []string{"123", "https://music.163.com/#/playlist?id=456", "https://y.music.163.com/m/playlist?id=10", "11"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens = %v; want %v", got, want)
	}
}

func TestPlaylistShortLinksCheckEveryRedirectWithoutCredentials(t *testing.T) {
	for _, tc := range []struct {
		name, location, want string
		requests             int
	}{
		{name: "official playlist", location: "https://music.163.com/#/playlist?id=123", want: "123", requests: 1},
		{name: "foreign host", location: "https://attacker.example/playlist?id=123", requests: 1},
		{name: "loopback", location: "http://127.0.0.1/playlist?id=123", requests: 1},
		{name: "redirect loop", location: "/another", requests: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: playIdsRoleTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Hostname() != "163cn.tv" || req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
					t.Fatalf("unexpected short-link request host or credentials")
				}
				return &http.Response{StatusCode: 302, Header: http.Header{"Location": {tc.location}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
			})}
			got, err := resolvePlaylistSource(t.Context(), "https://163cn.tv/test", client)
			if got != tc.want || (err != nil) != (tc.want == "") || calls != tc.requests {
				t.Fatalf("short link = %q, %v, calls=%d; want %q, calls=%d", got, err, calls, tc.want, tc.requests)
			}
		})
	}
}

type playlistReaderFunc func(context.Context, *weapi.PlaylistDetailReq) (*weapi.PlaylistDetailResp, error)

func (f playlistReaderFunc) PlaylistDetail(ctx context.Context, req *weapi.PlaylistDetailReq) (*weapi.PlaylistDetailResp, error) {
	return f(ctx, req)
}

func playlistResponse(t *testing.T, text string) *weapi.PlaylistDetailResp {
	t.Helper()
	var response weapi.PlaylistDetailResp
	if err := json.Unmarshal([]byte(text), &response); err != nil {
		t.Fatal(err)
	}
	return &response
}

func TestPlaylistSongsMergeAllTrackIDsAndKeepValidSources(t *testing.T) {
	player := &PlayIds{}
	var requested []string
	reader := playlistReaderFunc(func(ctx context.Context, req *weapi.PlaylistDetailReq) (*weapi.PlaylistDetailResp, error) {
		requested = append(requested, req.Id)
		switch req.Id {
		case "1":
			return playlistResponse(t, `{"code":200,"playlist":{"name":"完整歌单","trackCount":3,"trackIds":[{"id":10},{"id":11},{"id":12}],"tracks":[{"id":10}]}}`), nil
		case "2":
			return nil, errors.New("simulated timeout")
		case "3":
			return playlistResponse(t, `{"code":401}`), nil
		case "4":
			return playlistResponse(t, `{"code":200,"playlist":{"name":"兼容详情","tracks":[{"id":12},{"id":13},{"id":0}]}}`), nil
		default:
			return playlistResponse(t, `{"code":200,"playlist":{"name":"空歌单"}}`), nil
		}
	})
	got, err := player.mergePlaylistSongs(t.Context(), reader, []string{"9", "10"}, []string{"1", "https://music.163.com/#/playlist?id=1", "2", "3", "4", "5", "https://attacker.example/playlist?id=6"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"9", "10", "11", "12", "13"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("merged songs = %v; want %v", got, want)
	}
	if want := []string{"1", "2", "3", "4", "5"}; !reflect.DeepEqual(requested, want) {
		t.Fatalf("playlist requests = %v; want %v", requested, want)
	}
}

func TestPlaylistSongsAreResolvedForEachAccountAndRespectCancellation(t *testing.T) {
	player := &PlayIds{}
	for _, accountSong := range []string{"10", "20"} {
		reader := playlistReaderFunc(func(ctx context.Context, req *weapi.PlaylistDetailReq) (*weapi.PlaylistDetailResp, error) {
			return playlistResponse(t, `{"code":200,"playlist":{"trackIds":[{"id":`+accountSong+`}]}}`), nil
		})
		got, err := player.mergePlaylistSongs(t.Context(), reader, nil, []string{"1"})
		if err != nil || !reflect.DeepEqual(got, []string{accountSong}) {
			t.Fatalf("account's accessible songs = %v, %v", got, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := player.mergePlaylistSongs(ctx, playlistReaderFunc(func(context.Context, *weapi.PlaylistDetailReq) (*weapi.PlaylistDetailResp, error) {
		t.Fatal("cancelled request must not fetch playlists")
		return nil, nil
	}), []string{"9"}, []string{"1"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestPlaySourcesMergeCLIConfigAndFiles(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ids.txt")
	if err := os.WriteFile(file, []byte("3；4 # 备注"), 0600); err != nil {
		t.Fatal(err)
	}
	player := NewPlayIds(&Root{Cfg: &config.Config{PlayIds: &config.PlayIdsConfig{
		IDs: "2，3", IDsFile: config.StringOrSlice{file, file + ".missing"}, PlaylistIDs: config.StringOrSlice{"101", "https://music.163.com/#/playlist?id=102"},
	}}}, log.Default)
	if err := player.cmd.ParseFlags([]string{"--ids", "1，2 // 99", "--ids-file", file, "--playlist-ids", "100;101", "--playlist-ids", "103"}); err != nil {
		t.Fatal(err)
	}
	ids, playlists := player.collectPlaySources()
	if !reflect.DeepEqual(ids, []string{"1", "2", "3", "4"}) || !reflect.DeepEqual(playlists, []string{"100", "101", "103", "https://music.163.com/#/playlist?id=102"}) {
		t.Fatalf("collected sources = %v, %v", ids, playlists)
	}
}

func TestMusicianCandidatesReuseSongInputParsingAndKeepIndependentPool(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ids.txt")
	if err := os.WriteFile(file, []byte("3；4 // 98"), 0600); err != nil {
		t.Fatal(err)
	}
	root := &config.PlayIdsConfig{IDs: "1，2 # 99\n3；4", IDsFile: config.StringOrSlice{file}, PlaylistIDs: config.StringOrSlice{"999"}}
	if got, playlists := musicianPlaySources(config.MusicianPlayConf{}, root); !reflect.DeepEqual(got, []string{"1", "2", "3", "4"}) || !reflect.DeepEqual(playlists, []string{"999"}) {
		t.Fatalf("fallback candidates = %v, playlists = %v", got, playlists)
	}
	if got, playlists := musicianPlaySources(config.MusicianPlayConf{IDs: "10，11 // 97\n12；10", IDsFile: config.StringOrSlice{file}}, root); !reflect.DeepEqual(got, []string{"10", "11", "12", "3", "4"}) || len(playlists) != 0 {
		t.Fatalf("independent candidates = %v, playlists = %v", got, playlists)
	}
}

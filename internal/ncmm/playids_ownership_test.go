package ncmm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/types"
	"github.com/3899/ncmm/api/weapi"
	cookiepkg "github.com/3899/ncmm/pkg/cookie"
	"github.com/3899/ncmm/pkg/log"
)

func TestPlayIdsOwnSongUsesListeningAccountArtistID(t *testing.T) {
	tests := []struct {
		name     string
		account  int64
		artist   int64
		song     weapi.SongDetailRespSongs
		wantSelf bool
	}{
		{name: "艺名与昵称不同仍匹配真实歌手", account: 10, artist: 100, song: weapi.SongDetailRespSongs{Ar: []types.Artist{{Id: 100, Name: "不同艺名"}}}, wantSelf: true},
		{name: "合作歌手不在首位", account: 10, artist: 100, song: weapi.SongDetailRespSongs{Ar: []types.Artist{{Id: 200}, {Id: 100}}}, wantSelf: true},
		{name: "同名不同歌手不误判", account: 10, artist: 100, song: weapi.SongDetailRespSongs{Ar: []types.Artist{{Id: 200, Name: "相同昵称"}}}},
		{name: "辅助账号可播放主账号作品", account: 20, artist: 200, song: weapi.SongDetailRespSongs{Ar: []types.Artist{{Id: 100}}}},
		{name: "歌手身份未知不匹配零值", account: 10, song: weapi.SongDetailRespSongs{Ar: []types.Artist{{Id: 0}}}},
		{name: "保留本人上传者匹配", account: 10, song: weapi.SongDetailRespSongs{DjId: 10}, wantSelf: true},
		{name: "其他上传者不误判", account: 10, song: weapi.SongDetailRespSongs{DjId: 20}},
		{name: "空身份不匹配零值上传者", song: weapi.SongDetailRespSongs{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPlayIdsOwnSong(&tt.song, tt.account, tt.artist); got != tt.wantSelf {
				t.Fatalf("isPlayIdsOwnSong() = %v, want %v", got, tt.wantSelf)
			}
		})
	}
}

type playIdsRoleTransport func(*http.Request) (*http.Response, error)

func (f playIdsRoleTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestPlayIdsArtistIDUsesCurrentAccountRole(t *testing.T) {
	tests := []struct {
		name    string
		code    int64
		data    eapi.MusicianRoleGetData
		err     error
		wantID  int64
		wantErr bool
	}{
		{name: "已认证音乐人", code: 200, data: eapi.MusicianRoleGetData{UserId: 10, IsMusician: true, ArtistId: 100}, wantID: 100},
		{name: "缺省用户 ID 时使用当前登录会话", code: 200, data: eapi.MusicianRoleGetData{IsMusician: true, ArtistId: 100}, wantID: 100},
		{name: "非音乐人", code: 200, data: eapi.MusicianRoleGetData{UserId: 10}},
		{name: "非音乐人不采用残留歌手 ID", code: 200, data: eapi.MusicianRoleGetData{UserId: 10, ArtistId: 100}},
		{name: "音乐人缺少歌手 ID", code: 200, data: eapi.MusicianRoleGetData{UserId: 10, IsMusician: true}},
		{name: "拒绝其他账号身份", code: 200, data: eapi.MusicianRoleGetData{UserId: 20, IsMusician: true, ArtistId: 200}, wantErr: true},
		{name: "接口拒绝", code: 500, wantErr: true},
		{name: "网络失败", err: errors.New("identity unavailable"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := api.NewClient(&api.Config{Cookie: cookiepkg.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, log.Default)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })
			calls := 0
			client.GetClient().Transport = playIdsRoleTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				if request.URL.Path != "/eapi/nmusician/workbench/musician/role/get" {
					t.Fatalf("unexpected request path: %s", request.URL.Path)
				}
				if tt.err != nil {
					return nil, tt.err
				}
				body, err := json.Marshal(eapi.MusicianRoleGetResp{RespCommon: types.RespCommon[eapi.MusicianRoleGetData]{Code: tt.code, Data: tt.data}})
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
			})
			artistID, err := playIdsArtistID(t.Context(), eapi.New(client), 10)
			if artistID != tt.wantID || (err != nil) != tt.wantErr {
				t.Fatalf("playIdsArtistID() = (%d, %v), want (%d, error=%v)", artistID, err, tt.wantID, tt.wantErr)
			}
			if calls != 1 {
				t.Fatalf("role requests = %d, want 1", calls)
			}
		})
	}
}

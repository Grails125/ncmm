package ncmm

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/config"
	cookiepkg "github.com/3899/ncmm/pkg/cookie"
	"github.com/spf13/cobra"
)

type fansTransport func(*http.Request) (*http.Response, error)

func (f fansTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFansGroupPlaybackDisabledDoesNotRequest(t *testing.T) {
	var output bytes.Buffer
	c := &FansGroup{root: &Root{Cfg: &config.Config{FansGroup: &config.FansGroupConf{}}}, cmd: &cobra.Command{}}
	c.cmd.SetOut(&output)
	c.doPlaySong(context.Background(), nil, nil, eapi.FansGroupMissionItem{Title: "播放歌曲", AllProgress: 1})
	if !strings.Contains(output.String(), "fansgroup.webSession") {
		t.Fatalf("missing skip diagnostic: %s", output.String())
	}
}

func TestFansGroupWebSessionOffStillRunsOtherTasks(t *testing.T) {
	client, err := api.NewClient(&api.Config{Cookie: cookiepkg.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	shares, reads := 0, 0
	client.GetClient().Transport = fansTransport(func(r *http.Request) (*http.Response, error) {
		payload := `{"code":200}`
		switch r.URL.Path {
		case "/eapi/social/fansgroup/bff/detail/get":
			payload = `{"code":200,"data":{"fansGroupInfo":{"boardId":"1","fansGroupName":"测试"}}}`
		case "/eapi/social/fansgroup/bff/user/group/detail/get":
			payload = `{"code":200,"data":{"fansGroupMemberDetail":{"joined":true}}}`
		case "/eapi/fans/group/mission/all":
			reads++
			payload = `{"code":200,"data":{"normal":{"success":true,"data":[{"missionId":1,"title":"播放歌曲","allProgress":1},{"missionId":2,"title":"分享歌曲","allProgress":1,"button":{"url":"{\"actionCustomParams\":{\"progressParams\":{\"resourceId\":\"123456\",\"resourceType\":\"4\"}}}"}}]}}}`
		case "/eapi/fans/group/mission/forward/progress":
			shares++
		default:
			t.Errorf("unexpected network request: %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(payload)), Request: r}, nil
	})
	c := &FansGroup{root: &Root{Cfg: &config.Config{FansGroup: &config.FansGroupConf{}}}, cmd: &cobra.Command{}, rng: rand.New(rand.NewSource(1))}
	c.cmd.SetOut(io.Discard)
	if err := c.executeOneFansGroup(context.Background(), client, eapi.New(client), weapi.New(client), 123, "1", ""); err != nil {
		t.Fatal(err)
	}
	if shares != 1 || reads != 2 {
		t.Fatalf("shares=%d progress reads=%d", shares, reads)
	}
}

func TestFansGroupMediaValidationAndCancellation(t *testing.T) {
	for _, raw := range []string{"https://evil.invalid/test?authSecret=s", "https://m1.music.126.net/test", "https://user:pass@m1.music.126.net/test?authSecret=s", "https://m1.music.126.net:8080/test?authSecret=s"} {
		if _, err := fansGroupMediaURL(raw); err == nil {
			t.Errorf("unsafe media accepted: %s", raw)
		}
	}
	client, err := api.NewClient(&api.Config{Cookie: cookiepkg.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	client.GetClient().Transport = fansTransport(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := fetchFansGroupAudio(ctx, client, "https://m1.music.126.net/test?authSecret=private-signature"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestFansGroupAudioDownloadsBeyondProbeAndRejectsTruncation(t *testing.T) {
	client, err := api.NewClient(&api.Config{Cookie: cookiepkg.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	for _, truncated := range []bool{false, true} {
		body := bytes.NewReader(bytes.Repeat([]byte{7}, 1024*1024))
		client.GetClient().Transport = fansTransport(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("Cookie") != "" || r.Header.Get("Range") != "" {
				t.Error("media must not receive account cookies or partial range")
			}
			length := int64(body.Len())
			if truncated {
				length++
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: length, Body: io.NopCloser(body), Request: r}, nil
		})
		err := fetchFansGroupAudio(context.Background(), client, "https://m701.music.126.net/test.mp3?authSecret=test-only")
		if truncated && err == nil {
			t.Error("truncated media accepted")
		}
		if !truncated && (err != nil || body.Len() != 0) {
			t.Fatalf("not fully consumed: remaining=%d err=%v", body.Len(), err)
		}
	}
}

func TestFansGroupWebLogFields(t *testing.T) {
	logs := weapi.FansGroupPlaybackLogs(123, "song", "123", 4)
	if len(logs) != 2 || logs[0].Action != "startplay" || logs[1].Action != "play" {
		t.Fatalf("unexpected logs: %#v", logs)
	}
	if logs[1].JSON.MainsiteWeb != "1" || logs[1].JSON.Time == nil || *logs[1].JSON.Time != 4 {
		t.Fatalf("missing webpage fields: %#v", logs[1])
	}
}

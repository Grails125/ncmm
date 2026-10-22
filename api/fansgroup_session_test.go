package api

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/pkg/cookie"
)

func TestFansGroupWebSessionDoesNotChangeAccountCookies(t *testing.T) {
	c, err := NewClient(&Config{Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	u, _ := url.Parse("https://music.163.com/")
	c.SetCookies(u, []*http.Cookie{{Name: "os", Value: "iphone"}, {Name: "MUSIC_U", Value: "test-session"}})
	web := true
	c.GetClient().Transport = sensitiveTransport(func(r *http.Request) (*http.Response, error) {
		_, osErr := r.Cookie("os")
		if (osErr == nil) == web {
			t.Errorf("web=%v: os cookie isolation failed", web)
		}
		if ck, err := r.Cookie("MUSIC_U"); err != nil || ck.Value != "test-session" {
			t.Error("login cookie lost")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}, "Set-Cookie": {"os=changed; Domain=music.163.com; Path=/"}}, Body: io.NopCloser(strings.NewReader(`{"code":200}`)), Request: r}, nil
	})
	var reply struct{ Code int }
	opts := NewOptions()
	opts.FansGroupWebSession = true
	opts.Sensitive = true
	if _, err := c.Request(context.Background(), "https://clientlogusf.music.163.com/weapi/feedback/weblog", map[string]string{}, &reply, opts); err != nil {
		t.Fatal(err)
	}
	if got := c.cookieValue("https://music.163.com/", "os"); got != "iphone" {
		t.Fatalf("shared cookie changed: %s", got)
	}
	web = false
	if _, err := c.Request(context.Background(), "https://music.163.com/weapi/song/enhance/player/url/v1", map[string]string{}, &reply, NewOptions()); err != nil {
		t.Fatal(err)
	}
}

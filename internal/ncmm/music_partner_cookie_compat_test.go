package ncmm

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/internal/rewardevent"
	"github.com/3899/ncmm/pkg/log"
)

func TestMusicPartnerImportedCookieWithoutCSRF(t *testing.T) {
	t.Setenv("NCMM_WEB_CHILD", "1")
	for _, hasSession := range []bool{true, false} {
		name := "missing_login_cookie"
		if hasSession {
			name = "music_u_only"
		}
		t.Run(name, func(t *testing.T) {
			command, _ := partnerTestCommand(t)
			var output bytes.Buffer
			command.Command().SetOut(&output)
			file := filepath.Join(t.TempDir(), "imported.json")
			network := &api.Config{}
			network.Cookie.Filepath = file
			seed, err := api.NewClient(network, log.Default)
			if err != nil {
				t.Fatal(err)
			}
			if hasSession {
				endpoint, _ := url.Parse("https://music.163.com/")
				seed.SetCookies(endpoint, []*http.Cookie{{Name: "MUSIC_U", Value: "synthetic-imported-session", Path: "/"}})
			}
			_ = seed.Close(context.Background())
			command.root.Cfg.Network = network
			seen := 0
			command.newClient = func(cfg *api.Config, logger *log.Logger) (*api.Client, error) {
				client, err := api.NewClient(cfg, logger)
				if err != nil {
					return nil, err
				}
				client.GetClient().Transport = musicPartnerTestTransport(func(req *http.Request) (*http.Response, error) {
					seen++
					if cookie, err := req.Cookie("MUSIC_U"); err != nil || cookie.Value != "synthetic-imported-session" {
						t.Error("imported login cookie was not preserved")
					}
					if req.URL.Query().Get("csrf_token") != "" {
						t.Error("a CSRF token was synthesized")
					}
					payload := `{"code":200,"data":{"period":73,"week":2,"integral":{"integral":100,"currentWeekIntegral":40}}}`
					if strings.Contains(req.URL.Path, "/daily/task/") {
						payload = `{"code":200,"data":{"id":10,"count":0,"completedCount":0,"works":[]}}`
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(payload)), Request: req}, nil
				})
				return client, nil
			}
			err = command.executeAccount(context.Background(), "imported.json", file)
			if hasSession && (err != nil || seen != 3) {
				t.Fatalf("MUSIC_U-only imported session: requests=%d, error=%v", seen, err)
			}
			if hasSession {
				events := 0
				for _, line := range strings.Split(output.String(), "\n") {
					event, matched, eventErr := rewardevent.ParseLine(line)
					if !matched {
						continue
					}
					events++
					if eventErr != nil || event.Account != "imported.json" {
						t.Fatalf("absolute session path replaced account identity: %+v", event)
					}
				}
				if events != 1 || !strings.Contains(output.String(), "账号 (imported.json) 开始评分") || strings.Contains(output.String(), file) {
					t.Fatal("account log identity differs from event identity")
				}
			}
			if !hasSession && (err == nil || seen != 0) {
				t.Fatalf("missing login session must fail before HTTP: requests=%d, error=%v", seen, err)
			}
			current, err := os.ReadFile(file)
			if err != nil || strings.Contains(string(current), "__csrf") || (hasSession && !strings.Contains(string(current), "synthetic-imported-session")) {
				t.Fatal("partner session changed login credentials or invented a CSRF token")
			}
		})
	}
}

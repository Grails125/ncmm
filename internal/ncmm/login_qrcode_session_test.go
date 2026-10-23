package ncmm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/internal/loginresult"
	"github.com/3899/ncmm/pkg/crypto"
	"github.com/3899/ncmm/pkg/log"
	"github.com/spf13/cobra"
)

func TestQRCodeSessionUsesPCPairAndProtectsSavedCookie(t *testing.T) {
	for _, scenario := range []string{"success", "8821", "invalid_user", "missing_login_cookie", "cookie_not_persisted"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			target := filepath.Join(home, "existing.json")
			original := []byte(`{"preserved-session":"synthetic-existing-session"}`)
			if err := os.WriteFile(target, original, 0600); err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{Network: &api.Config{Debug: true, Retry: 5}, Accounts: &config.AccountsConf{Main: target}}
			cfg.Network.Cookie.Filepath = target
			root := &Root{Cfg: cfg, Opts: RootOpts{Home: home}}
			var output bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&output)
			login := &Login{root: root, cmd: cmd, isMain: true, noConfigWrite: true, jsonResult: true}
			command := &loginQrcodeCmd{root: login, cmd: cmd, l: log.Default, timeout: time.Second, dir: home, level: 1, waitPoll: func(context.Context, time.Duration) error { return nil }}
			seen := 0
			command.newClient = func(network *api.Config, logger *log.Logger) (*api.Client, error) {
				if network.Cookie.Filepath == target || network.Debug || network.Cookie.ReadOnly || network.Retry != 0 {
					t.Error("QR login must use a fresh private session")
				}
				sessionConfig := *network
				if scenario == "cookie_not_persisted" {
					// 模拟导出未落盘但 Close 仍返回 nil，不依赖操作系统权限或测试运行账号。
					sessionConfig.Cookie.ReadOnly = true
				}
				client, err := api.NewClient(&sessionConfig, logger)
				if err != nil {
					return nil, err
				}
				client.GetClient().Transport = musicPartnerTestTransport(func(req *http.Request) (*http.Response, error) {
					seen++
					body := `{"code":200,"unikey":"synthetic-qr-key"}`
					header := http.Header{"Content-Type": {"application/json"}}
					if seen <= 2 {
						if req.URL.Host != "interfacepc.music.163.com" || !strings.HasPrefix(req.URL.Path, "/eapi/login/qrcode/") {
							t.Errorf("unexpected QR protocol: %s%s", req.URL.Host, req.URL.Path)
							return nil, io.ErrUnexpectedEOF
						}
						if err := req.ParseForm(); err != nil {
							t.Fatal(err)
						}
						plain, err := crypto.EApiDecrypt(req.PostForm.Get("params"), "hex")
						if err != nil {
							t.Fatal(err)
						}
						parts := strings.Split(string(plain), "-36cd479b6b5-")
						if len(parts) != 3 {
							t.Fatal("malformed EAPI QR request")
						}
						var payload struct {
							Type int    `json:"type"`
							Key  string `json:"key"`
						}
						if err := json.Unmarshal([]byte(parts[1]), &payload); err != nil {
							t.Fatal(err)
						}
						if payload.Type != 3 || (seen == 2 && payload.Key != "synthetic-qr-key") {
							t.Error("QR key/check pair must both use type 3 and the same key")
						}
						if seen == 2 {
							body = `{"code":803}`
							if scenario == "8821" {
								body = `{"code":8821,"message":"请切换其他登录方式或升级新版本再试 synthetic-private-server-message"}`
							} else if scenario != "missing_login_cookie" {
								header.Add("Set-Cookie", "MUSIC_U=synthetic-new-session; Domain=.music.163.com; Path=/; Secure; HttpOnly")
							}
						}
					} else {
						if seen > 3 || req.URL.Path != "/weapi/w/nuser/account/get" {
							t.Fatalf("unexpected post-login request: %s", req.URL.Path)
						}
						body = `{"code":200,"account":{"id":123},"profile":{"userId":123,"nickname":"tester","avatarUrl":"https://p1.music.126.net/avatar.jpg"}}`
						if scenario == "invalid_user" {
							body = `{"code":200,"account":null,"profile":null}`
						}
					}
					return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
				})
				return client, nil
			}
			err := command.execute(context.Background(), nil)
			current, readErr := os.ReadFile(target)
			if scenario == "success" {
				if err != nil || seen != 3 || readErr != nil || !strings.Contains(string(current), "synthetic-new-session") {
					t.Fatalf("verified QR session not saved: requests=%d error=%v read=%v", seen, err, readErr)
				}
				result, parseErr := loginresult.Parse(output.String())
				if parseErr != nil || result.UID != 123 || result.CookiePath != target {
					t.Fatalf("login result missing or incorrect: %v", parseErr)
				}
			} else {
				if err == nil || readErr != nil || !bytes.Equal(current, original) {
					t.Fatalf("failed QR login overwrote an account or succeeded: error=%v read=%v", err, readErr)
				}
				if strings.Contains(output.String(), "login success") || strings.Contains(output.String(), "Cookie 已保存") {
					t.Error("failed QR login printed a successful result")
				}
				if scenario == "8821" && (!strings.Contains(err.Error(), "8821") || !strings.Contains(err.Error(), "Cookie") || strings.Contains(err.Error(), "synthetic-private-server-message")) {
					t.Error("8821 must remain a failure with actionable, private guidance")
				}
				if scenario == "cookie_not_persisted" && !strings.Contains(err.Error(), "扫码登录会话未正确保存") {
					t.Error("unsaved QR credentials must be rejected before replacing the original account")
				}
			}
			leftovers, err := filepath.Glob(filepath.Join(home, ".qrcode-cookie-*"))
			if err != nil || len(leftovers) != 0 {
				t.Fatal("QR temporary credentials were not cleaned up")
			}
			if _, err := os.Stat(filepath.Join(home, "qrcode.png")); !os.IsNotExist(err) {
				t.Fatal("QR file was not cleaned up")
			}
			if cfg.Network.Cookie.Filepath != target || cfg.Network.Retry != 5 || !cfg.Network.Debug {
				t.Error("QR login changed shared network configuration")
			}
			if strings.Contains(output.String(), "chainId=") {
				t.Error("PC QR login unexpectedly included a Web chainId")
			}
		})
	}
}

func TestQRCodePollingStopsOnTimeoutWithoutWaitingThreeSeconds(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "existing.json")
	if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Network: &api.Config{}}
	cfg.Network.Cookie.Filepath = target
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	root := &Root{Cfg: cfg, Opts: RootOpts{Home: home}}
	login := &Login{root: root, cmd: cmd, isMain: true, noConfigWrite: true}
	command := &loginQrcodeCmd{root: login, cmd: cmd, l: log.Default, timeout: 30 * time.Millisecond, dir: home, level: 1}
	seen := 0
	command.newClient = func(network *api.Config, logger *log.Logger) (*api.Client, error) {
		client, err := api.NewClient(network, logger)
		if err != nil {
			return nil, err
		}
		client.GetClient().Transport = musicPartnerTestTransport(func(req *http.Request) (*http.Response, error) {
			seen++
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":200,"unikey":"synthetic-polling-key"}`)), Request: req}, nil
		})
		return client, nil
	}
	started := time.Now()
	err := command.execute(context.Background(), nil)
	if !errors.Is(err, context.DeadlineExceeded) || seen != 1 || time.Since(started) >= time.Second {
		t.Fatalf("QR timeout did not interrupt poll interval: requests=%d elapsed=%v error=%v", seen, time.Since(started), err)
	}
	if _, err := os.Stat(filepath.Join(home, "qrcode.png")); !os.IsNotExist(err) {
		t.Fatal("canceled QR session left a scannable code")
	}
	files, err := filepath.Glob(filepath.Join(home, ".qrcode-cookie-*"))
	if err != nil || len(files) != 0 {
		t.Fatal("canceled QR session left temporary cookies")
	}
}

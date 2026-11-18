package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/internal/filelock"
	"github.com/3899/ncmm/internal/loginresult"
	webauth "github.com/3899/ncmm/internal/webui/auth"
	"github.com/3899/ncmm/pkg/log"
)

type fakeSMSClient struct {
	sends, logins, closes int
	sendErr, loginErr     error
	onLogin               func()
}

func (f *fakeSMSClient) send(context.Context, string, int64) error { f.sends++; return f.sendErr }
func (f *fakeSMSClient) login(context.Context, string, int64, string) (loginresult.Result, []byte, error) {
	f.logins++
	if f.onLogin != nil {
		f.onLogin()
	}
	return loginresult.Result{UID: 123, Nickname: "短信测试"}, []byte(`{"fixture":"cookie"}`), f.loginErr
}

func TestSMSLoginCanceledOrExpiredBeforeCommit(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprintf("expired-%t", expired), func(t *testing.T) {
			now := time.Now()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fake := &fakeSMSClient{}
			manager := newSMSLoginManager(func(context.Context) (smsLoginClient, error) { return fake, nil })
			defer manager.close()
			manager.now = func() time.Time { return now }
			view, err := manager.send(ctx, "13800000000", 86, "fan.json", false, "rev")
			if err != nil {
				t.Fatal(err)
			}
			fake.onLogin = func() {
				if expired {
					now = now.Add(6 * time.Minute)
				} else {
					cancel()
				}
			}
			commits := 0
			err = manager.login(ctx, view.ID, "123456", func(string, loginresult.Result, []byte) error { commits++; return nil })
			if err == nil || commits != 0 || fake.closes != 1 || len(manager.sessions) != 0 {
				t.Fatalf("late login committed: err=%v commits=%d closes=%d sessions=%d", err, commits, fake.closes, len(manager.sessions))
			}
			if !expired && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}
func (f *fakeSMSClient) close() { f.closes++ }

func TestSMSLoginCooldownAndTargetBinding(t *testing.T) {
	fake := &fakeSMSClient{}
	manager := newSMSLoginManager(func(context.Context) (smsLoginClient, error) { return fake, nil })
	defer manager.close()
	now := time.Now()
	manager.now = func() time.Time { return now }
	view, err := manager.send(context.Background(), "13800000000", 86, "fan1.json", false, "revision-1")
	if err != nil || view.ID == "" || view.RetryAfter != 60 {
		t.Fatalf("view=%+v error=%v", view, err)
	}
	if _, err := manager.send(context.Background(), "13800000000", 86, "other.json", true, "revision-2"); err == nil || fake.sends != 1 {
		t.Fatal("same phone bypassed cooldown")
	}
	var expected string
	var result loginresult.Result
	commit := func(revision string, value loginresult.Result, data []byte) error {
		expected = revision
		result = value
		return nil
	}
	if err := manager.login(context.Background(), view.ID, "123456", commit); err != nil {
		t.Fatal(err)
	}
	if expected != "revision-1" || result.AccountPath != "./fan1.json" || result.Main || fake.logins != 1 {
		t.Fatalf("wrong target binding: %s %+v", expected, result)
	}
	if err := manager.login(context.Background(), view.ID, "123456", commit); err == nil || fake.logins != 1 {
		t.Fatal("session replayed")
	}
}

func TestSMSLoginNormalizesTargetBeforeBinding(t *testing.T) {
	fake := &fakeSMSClient{}
	manager := newSMSLoginManager(func(context.Context) (smsLoginClient, error) { return fake, nil })
	defer manager.close()
	view, err := manager.send(t.Context(), "13800000000", 86, " fan ", false, "revision")
	if err != nil {
		t.Fatal(err)
	}
	var target string
	if err := manager.login(t.Context(), view.ID, "123456", func(_ string, result loginresult.Result, _ []byte) error { target = result.AccountPath; return nil }); err != nil {
		t.Fatal(err)
	}
	if target != "./fan.json" {
		t.Fatalf("target not normalized: %q", target)
	}
}

func TestSMSRemoteSendRequiresExplicitAcceptance(t *testing.T) {
	for _, body := range []string{`{"code":200}`, `{"code":200,"data":{}}`, `{"code":200,"data":null}`, `{"code":200,"data":false}`, `{"code":200,"data":true}`} {
		t.Run(body, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "cookie.json")
			os.WriteFile(path, []byte("{}"), 0600)
			cfg := &api.Config{}
			cfg.Cookie.Filepath = path
			cfg.Cookie.Interval = 0
			client, err := api.NewClient(cfg, log.Default)
			if err != nil {
				t.Fatal(err)
			}
			remote := &remoteSMSClient{client: client, path: path, dir: dir, identity: eapi.NewSMSLoginIdentity()}
			defer remote.close()
			client.GetClient().Transport = smsFixtureTransport(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			err = remote.send(t.Context(), "13800000000", 86)
			if accepted := strings.Contains(body, `"data":true`); (err == nil) != accepted {
				t.Fatalf("send receipt accepted=%t err=%v", accepted, err)
			}
		})
	}
}

func TestSMSLoginFailureExpiryAndAttemptBound(t *testing.T) {
	fake := &fakeSMSClient{sendErr: errors.New("secret phone captcha token")}
	manager := newSMSLoginManager(func(context.Context) (smsLoginClient, error) { return fake, nil })
	defer manager.close()
	now := time.Now()
	manager.now = func() time.Time { return now }
	if _, err := manager.send(context.Background(), "13800000000", 86, "fan.json", false, "rev"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("send failure leaked or was accepted")
	}
	now = now.Add(time.Minute)
	fake.sendErr = nil
	fake.loginErr = errors.New("secret")
	view, err := manager.send(context.Background(), "13800000000", 86, "fan.json", false, "rev")
	if err != nil {
		t.Fatal(err)
	}
	commit := func(string, loginresult.Result, []byte) error { t.Fatal("failed login committed"); return nil }
	for i := 0; i < 6; i++ {
		if err := manager.login(context.Background(), view.ID, "123456", commit); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("bad verification accepted or leaked")
		}
	}
	if fake.logins != 5 {
		t.Fatalf("verification count=%d", fake.logins)
	}
	now = now.Add(time.Minute)
	view, err = manager.send(context.Background(), "13800000000", 86, "fan.json", false, "rev")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	if err := manager.login(context.Background(), view.ID, "123456", commit); err == nil || fake.logins != 5 {
		t.Fatal("expired session submitted")
	}
}

func TestSMSAccountCommitPreservesExistingOnRevisionConflict(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "config.yaml")
	cookiePath := filepath.Join(home, "cookie.json")
	if err := os.WriteFile(configPath, configForTest(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cookiePath, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	store := newConfigStore(configPath)
	doc, _ := store.get()
	result := loginresult.Result{UID: 123, Nickname: "短信登录", CookiePath: cookiePath, AccountPath: "./cookie.json", Main: true}
	if _, err := store.updateAccountWithCookie("stale", result, []byte("new")); !errors.Is(err, errConfigRevisionConflict) {
		t.Fatalf("error=%v", err)
	}
	data, _ := os.ReadFile(cookiePath)
	if string(data) != "original" {
		t.Fatal("stale login changed Cookie")
	}
	updated, err := store.updateAccountWithCookie(doc.Revision, result, []byte("new"))
	if err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(cookiePath)
	if string(data) != "new" || updated.AccountProfiles["./cookie.json"].CookieVerifiedAt == "" {
		t.Fatal("verified account not saved")
	}
}

func TestSMSAccountCommitRollsBackCookieOnValidationFailure(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.yaml")
	cookiePath := filepath.Join(home, "cookie.json")
	os.WriteFile(path, configForTest(), 0600)
	os.WriteFile(cookiePath, []byte("original"), 0600)
	store := newYAMLStore(path, func(string) error { return errors.New("fixture rejected") })
	doc, _ := store.get()
	_, err := store.updateAccountWithCookie(doc.Revision, loginresult.Result{UID: 123, CookiePath: cookiePath, AccountPath: "./cookie.json", Main: true}, []byte("new"))
	data, _ := os.ReadFile(cookiePath)
	current, _ := store.get()
	if err == nil || string(data) != "original" || current.Revision != doc.Revision {
		t.Fatal("failed save changed prior account")
	}
}

func TestSMSAccountCommitCancellationWhileWaitingForConfigLock(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.yaml")
	cookiePath := filepath.Join(home, "cookie.json")
	os.WriteFile(path, configForTest(), 0600)
	os.WriteFile(cookiePath, []byte("original"), 0600)
	store := newConfigStore(path)
	doc, err := store.get()
	if err != nil {
		t.Fatal(err)
	}
	held, err := filelock.Acquire(t.Context(), path+".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := store.updateAccountWithCookieContext(ctx, doc.Revision, loginresult.Result{UID: 123, CookiePath: cookiePath, AccountPath: "./cookie.json", Main: true}, []byte("new"))
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for store.mu.TryLock() {
		store.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("commit did not start waiting")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("lost cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("config lock wait ignored cancellation")
	}
	data, _ := os.ReadFile(cookiePath)
	configData, _ := os.ReadFile(path)
	if string(data) != "original" || revision(configData) != doc.Revision {
		t.Fatal("cancelled lock wait changed prior account")
	}
}

func TestSMSHTTPRequiresAuthenticationAndCSRF(t *testing.T) {
	home := t.TempDir()
	server := newAuthTestServer(t, home)
	os.WriteFile(filepath.Join(home, "config.yaml"), configForTest(), 0600)
	server.config = newConfigStore(filepath.Join(home, "config.yaml"))
	fake := &fakeSMSClient{}
	server.sms = newSMSLoginManager(func(context.Context) (smsLoginClient, error) { return fake, nil })
	defer server.sms.close()
	credentials, err := server.authManager.Setup(context.Background(), "Admin#123", requestClientInfo(httptest.NewRequest("GET", "/", nil)))
	if err != nil {
		t.Fatal(err)
	}
	handler := server.routes()
	request := func(csrf, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/accounts/sms/send", strings.NewReader(`{"phone":"13800000000","countryCode":86,"filename":"fan1.json"}`))
		req.Host = "localhost:3899"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://localhost:3899")
		req.Header.Set(csrfHeader, csrf)
		if token != "" {
			req.AddCookie(&http.Cookie{Name: webauth.SessionCookieName, Value: token})
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	if response := request("", ""); response.Code != 401 {
		t.Fatalf("unauth status=%d", response.Code)
	}
	if response := request("", credentials.Token); response.Code != 403 {
		t.Fatalf("csrf status=%d", response.Code)
	}
	response := request(credentials.Session.CSRFToken, credentials.Token)
	if response.Code != 200 || fake.sends != 1 {
		t.Fatalf("send status=%d body=%s", response.Code, response.Body.String())
	}
	var view smsLoginView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil || view.ID == "" {
		t.Fatal("missing SMS session")
	}
	if strings.Contains(response.Body.String(), "13800000000") || strings.Contains(response.Body.String(), "captcha") {
		t.Fatal("sensitive response")
	}
}

type smsFixtureTransport func(*http.Request) (*http.Response, error)

func (f smsFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestSMSRemoteLoginRequiresCookieAndMatchingAccount(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		cookie   bool
		uid      int64
		accepted bool
	}{{"verified", true, 123, true}, {"no-cookie", false, 123, false}, {"wrong-account", true, 456, false}} {
		t.Run(scenario.name, func(t *testing.T) {
			dir := t.TempDir()
			cookiePath := filepath.Join(dir, "cookie.json")
			os.WriteFile(cookiePath, []byte("{}"), 0600)
			cfg := &api.Config{}
			cfg.Cookie.Filepath = cookiePath
			cfg.Cookie.Interval = 0
			client, err := api.NewClient(cfg, log.Default)
			if err != nil {
				t.Fatal(err)
			}
			remote := &remoteSMSClient{client: client, path: cookiePath, dir: dir, identity: eapi.NewSMSLoginIdentity()}
			defer remote.close()
			client.GetClient().Transport = smsFixtureTransport(func(req *http.Request) (*http.Response, error) {
				header := http.Header{"Content-Type": []string{"application/json"}}
				body := `{"code":200,"account":{"id":123}}`
				if strings.Contains(req.URL.Path, "login/cellphone") {
					if scenario.cookie {
						header.Add("Set-Cookie", "MUSIC_U=fixture-auth; Domain=.music.163.com; Path=/; Secure; HttpOnly")
					}
				} else if strings.Contains(req.URL.Path, "nuser/account/get") {
					body = fmt.Sprintf(`{"code":200,"account":{"id":%d},"profile":{"userId":%d,"nickname":"fixture"}}`, scenario.uid, scenario.uid)
				} else {
					t.Fatal("unexpected request")
				}
				return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			result, data, err := remote.login(context.Background(), "13800000000", 86, "123456")
			if scenario.accepted {
				if err != nil || result.UID != 123 || !strings.Contains(string(data), "fixture-auth") {
					t.Fatalf("valid login not persisted: %v", err)
				}
			} else if err == nil {
				t.Fatal("unverified login accepted")
			}
		})
	}
}

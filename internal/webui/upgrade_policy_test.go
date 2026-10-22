package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/internal/upgradepolicy"
	webauth "github.com/3899/ncmm/internal/webui/auth"
)

func writeRequiredUpgrade(t *testing.T, home string) {
	t.Helper()
	cache := map[string]any{
		"last_check_time": time.Now(), "required_version": "1.2.9-grails.2",
		"required_published_at": time.Now().AddDate(0, -2, 0),
		"github_token":          "must-not-be-exposed", "private_path": "/private/home",
	}
	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "ncmm-update.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestTaskAPIsReturnUpgradeRequiredWithoutStartingWork(t *testing.T) {
	home := t.TempDir()
	server, err := New(context.Background(), Options{Home: home, Listen: defaultListen, Version: "1.2.9-grails.1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.scheduler.close)
	t.Cleanup(server.qrcode.close)
	t.Cleanup(func() { server.processes.Close(time.Second) })
	credentials, err := server.authManager.Setup(context.Background(), "UpgradeTest#2026", webauth.ClientInfo{})
	if err != nil {
		t.Fatal(err)
	}
	job, err := server.scheduler.upsert(Schedule{Name: "隔离评分测试", Command: "music-partner", Cron: "0 3 * * *", OverlapPolicy: "skip"})
	if err != nil {
		t.Fatal(err)
	}
	writeRequiredUpgrade(t, home)
	for _, endpoint := range []string{"/api/v1/schedules/" + job.ID + "/run", "/api/v1/plugins/fixture/run"} {
		t.Run(endpoint, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{}`))
			request.Host = defaultListen
			request.Header.Set(csrfHeader, credentials.Session.CSRFToken)
			request.AddCookie(&http.Cookie{Name: webauth.SessionCookieName, Value: credentials.Token})
			response := httptest.NewRecorder()
			server.routes().ServeHTTP(response, request)
			var body struct {
				Error         string               `json:"error"`
				UpgradePolicy upgradepolicy.Status `json:"upgradePolicy"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusUpgradeRequired || !body.UpgradePolicy.Required || body.UpgradePolicy.RequiredVersion != "1.2.9-grails.2" {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "must-not-be-exposed") || strings.Contains(response.Body.String(), "/private/home") {
				t.Fatal("private cache fields leaked")
			}
		})
	}
	if len(server.runner.list()) != 0 {
		t.Fatal("blocked task created a run")
	}
	// 到期策略只阻止任务准入；认证、读取更新与重启自救继续经过原有鉴权。
	for _, endpoint := range []string{"/api/v1/auth/status", "/api/v1/update"} {
		request := httptest.NewRequest(http.MethodGet, endpoint, nil)
		request.Host = defaultListen
		request.AddCookie(&http.Cookie{Name: webauth.SessionCookieName, Value: credentials.Token})
		response := httptest.NewRecorder()
		server.routes().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("recovery %s = %d %s", endpoint, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/system/restart", strings.NewReader(`{}`))
	request.Host = defaultListen
	request.Header.Set(csrfHeader, credentials.Session.CSRFToken)
	request.AddCookie(&http.Cookie{Name: webauth.SessionCookieName, Value: credentials.Token})
	response := httptest.NewRecorder()
	server.routes().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("restart recovery = %d %s", response.Code, response.Body.String())
	}
}

func TestAPIErrorRecognizesWrappedUpgradeRequirement(t *testing.T) {
	response := httptest.NewRecorder()
	err := fmt.Errorf("task admission: %w", &upgradepolicy.RequiredError{Status: upgradepolicy.Status{Required: true, CurrentVersion: "1.2.9-grails.1", RequiredVersion: "1.2.9-grails.2"}})
	writeAPIError(response, http.StatusBadRequest, err)
	if response.Code != http.StatusUpgradeRequired || !strings.Contains(response.Body.String(), `"upgradePolicy"`) {
		t.Fatalf("wrapped policy = %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	writeAPIError(response, http.StatusNotFound, os.ErrNotExist)
	if response.Code != http.StatusNotFound {
		t.Fatal("ordinary error status changed")
	}
}

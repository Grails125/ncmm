package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/internal/partnerpoints"
)

func pointsTestServer(t *testing.T) *Server {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, "config.yaml")
	raw := "accounts:\n  main: ./cookie.json\n  secondary:\n    - fan.json\nnetwork:\n  timeout: 20s\n  debug: true\n  retry: 3\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	return &Server{opts: Options{Home: home}, config: newConfigStore(path)}
}

func TestPartnerPointsOnlyReadsConfiguredAccount(t *testing.T) {
	s := pointsTestServer(t)
	called := 0
	s.readPartnerPoints = func(ctx context.Context, network *api.Config, path string) (*partnerpoints.Snapshot, error) {
		called++
		if path != filepath.Join(s.opts.Home, "cookie.json") || network.Timeout != 20*time.Second {
			t.Fatal("wrong account or network configuration")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 15*time.Second {
			t.Fatal("missing bounded request timeout")
		}
		zero, period := int64(0), int64(73)
		return &partnerpoints.Snapshot{Period: &period, Integral: &zero, UpdatedAt: time.Now()}, nil
	}
	for _, query := range []string{"?account=../../secret.json", "?account=C:/secret.json", "?account=cookie.json"} {
		r := httptest.NewRecorder()
		s.handlePartnerPointsGet(r, httptest.NewRequest(http.MethodGet, "/api/v1/music-partner/points"+query, nil))
		if r.Code != http.StatusBadRequest || called != 0 {
			t.Fatalf("unconfigured path accepted: %s", query)
		}
	}
	r := httptest.NewRecorder()
	s.handlePartnerPointsGet(r, httptest.NewRequest(http.MethodGet, "/api/v1/music-partner/points", nil))
	if r.Code != http.StatusOK || called != 1 || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected response: %d %s", r.Code, r.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["account"] != "./cookie.json" || payload["integral"] != float64(0) || payload["currentWeekIntegral"] != nil || payload["period"] != float64(73) {
		t.Fatalf("zero or unknown values lost: %v", payload)
	}
}

func TestPartnerPointsFailureDoesNotExposeCredentials(t *testing.T) {
	s := pointsTestServer(t)
	var logs strings.Builder
	s.opts.Output = func(format string, args ...any) { fmt.Fprintf(&logs, format, args...) }
	s.readPartnerPoints = func(context.Context, *api.Config, string) (*partnerpoints.Snapshot, error) {
		return nil, errors.New("https://example.invalid?csrf_token=unit-secret MUSIC_U=unit-secret")
	}
	r := httptest.NewRecorder()
	s.handlePartnerPointsGet(r, httptest.NewRequest(http.MethodGet, "/api/v1/music-partner/points?account=fan.json", nil))
	if r.Code != http.StatusServiceUnavailable || strings.Contains(r.Body.String()+logs.String(), "unit-secret") {
		t.Fatalf("unsafe error response: %d", r.Code)
	}
	if strings.Contains(r.Body.String(), `"integral":0`) {
		t.Fatal("failure must not look like zero points")
	}
}

func TestPartnerPointsRequiresWebUIAuthentication(t *testing.T) {
	s := newAuthTestServer(t, t.TempDir())
	called := false
	s.readPartnerPoints = func(context.Context, *api.Config, string) (*partnerpoints.Snapshot, error) {
		called = true
		return nil, nil
	}
	r := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music-partner/points?account=./cookie.json", nil)
	req.Host = "127.0.0.1:3899"
	s.routes().ServeHTTP(r, req)
	if r.Code != http.StatusUnauthorized || called {
		t.Fatalf("unauthenticated request: status=%d called=%v", r.Code, called)
	}
}

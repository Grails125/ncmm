package webui

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/internal/plugininstall"
)

type pluginPlatformTestManager struct {
	PluginManager
	name, token, proxy string
	force              bool
}

func (p *pluginPlatformTestManager) UpgradePlugin(_ context.Context, name, token, proxy string) (string, error) {
	p.name, p.token, p.proxy = name, token, proxy
	return name, nil
}
func (p *pluginPlatformTestManager) CheckPluginUpdates(_ context.Context, force bool) ([]plugininstall.Update, error) {
	p.force = force
	return []plugininstall.Update{{Name: "demo", LatestTag: "v2"}}, nil
}

func (p *pluginPlatformTestManager) CheckPublicPluginRepositories(_ context.Context, repositories []string, force bool) ([]plugininstall.Update, error) {
	if len(repositories) != 1 || repositories[0] != "crossgg/ncmm-qqbot" {
		return nil, context.Canceled
	}
	return []plugininstall.Update{{Repository: repositories[0], LatestTag: "v2.3.4", CheckedAt: time.Now().UTC()}}, nil
}

func TestPluginMarketExposesMetadataAndRealCachedVersion(t *testing.T) {
	server := &Server{plugins: &pluginPlatformTestManager{}}
	response := httptest.NewRecorder()
	server.handlePluginMarket(response, httptest.NewRequest("GET", "/api/v1/plugins/market", nil))
	var entries []struct {
		Repository string               `json:"repository"`
		Author     string               `json:"author"`
		Features   []string             `json:"features"`
		Update     plugininstall.Update `json:"update"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &entries); err != nil || len(entries) != 1 {
		t.Fatalf("invalid market response: %s %v", response.Body.String(), err)
	}
	entry := entries[0]
	if entry.Author != "crossgg" || len(entry.Features) == 0 || entry.Update.LatestTag != "v2.3.4" || entry.Update.CheckedAt.IsZero() || entry.Update.Repository != entry.Repository {
		t.Fatalf("market metadata/version missing: %+v", entry)
	}
}
func TestPluginPlatformAPIContracts(t *testing.T) {
	manager := &pluginPlatformTestManager{}
	server := &Server{plugins: manager}
	response := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/v1/plugins/demo/upgrade", strings.NewReader(`{"token":"private-token","proxy":"https://ghproxy.net"}`))
	request.SetPathValue("name", "demo")
	server.handlePluginUpgrade(response, request)
	if response.Code != 200 || manager.name != "demo" || manager.token != "private-token" || manager.proxy != "https://ghproxy.net" {
		t.Fatalf("bad upgrade response %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "private-token") {
		t.Fatal("response leaked token")
	}
	response = httptest.NewRecorder()
	server.handlePluginUpdates(response, httptest.NewRequest("POST", "/api/v1/plugins/updates", strings.NewReader(`{"force":true}`)))
	if response.Code != 200 || !manager.force {
		t.Fatalf("bad check response %d", response.Code)
	}
	response = httptest.NewRecorder()
	server.handlePluginMarket(response, httptest.NewRequest("GET", "/api/v1/plugins/market", nil))
	var market []pluginMarketEntry
	if json.Unmarshal(response.Body.Bytes(), &market) != nil || len(market) != 1 || market[0].Repository != "crossgg/ncmm-qqbot" {
		t.Fatalf("bad public market %s", response.Body.String())
	}
}

func TestPluginMutationAdmissionBlocksActiveAndNewRuns(t *testing.T) {
	manager := newControlledRunManager(t, 1)
	manager.runs["custom"] = RunRecord{Command: "plugin", Args: []string{"run", "demo", "--", "--amount", "1"}, Status: "queued"}
	if _, err := manager.reservePluginMutation("demo"); err == nil {
		t.Fatal("queued plugin admitted upgrade")
	}
	manager.runs["custom"] = RunRecord{Command: "plugin", Args: []string{"run", "demo"}, Status: "running"}
	if _, err := manager.reservePluginMutation("DEMO"); err == nil {
		t.Fatal("running plugin admitted upgrade")
	}
	delete(manager.runs, "custom")
	release, err := manager.reservePluginMutation("demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.reservePluginMutation("demo"); err == nil {
		t.Fatal("duplicate mutation admitted")
	}
	if _, err = manager.start(context.Background(), Schedule{Command: "plugin", Args: []string{"run", "demo"}}); err == nil || !strings.Contains(err.Error(), "升级") {
		t.Fatalf("new run not blocked: %v", err)
	}
	otherRelease, err := manager.reservePluginMutation("other")
	if err != nil {
		t.Fatal("unrelated plugin blocked")
	}
	otherRelease()
	release()
	done, err := manager.reservePluginMutation("demo")
	if err != nil {
		t.Fatal("reservation not released")
	}
	done()
}

func TestPluginMutationConservativelyBlocksGlobalFlagLayouts(t *testing.T) {
	for _, args := range [][]string{{"run", "--config", "plugin-host.yaml", "demo"}, {"--home", "another", "run", "demo"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			manager := newControlledRunManager(t, 1)
			manager.runs["unknown-layout"] = RunRecord{Command: "plugin", Args: args, Status: "running"}
			if _, err := manager.reservePluginMutation("demo"); err == nil {
				t.Fatal("global-flag plugin run bypassed mutation gate")
			}
			delete(manager.runs, "unknown-layout")
			release, err := manager.reservePluginMutation("demo")
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if _, err = manager.start(context.Background(), Schedule{Command: "plugin", Args: args}); err == nil || !strings.Contains(err.Error(), "升级") {
				t.Fatalf("unknown plugin run entered active mutation: %v", err)
			}
		})
	}
}

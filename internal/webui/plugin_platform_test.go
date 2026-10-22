package webui

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

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

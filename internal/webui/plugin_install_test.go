package webui

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/3899/ncmm/internal/plugininstall"
)

type installTestManager struct {
	PluginManager
	target, token, tag string
}

func (p *installTestManager) InstallPlugin(_ context.Context, options plugininstall.Options, _ func(string, ...any)) (string, error) {
	if _, err := options.ParseTarget(); err != nil {
		return "", err
	}
	p.target, p.token, p.tag = options.Target, options.Token, options.Tag
	return "demo", nil
}

func TestPluginInstallAcceptsWebUIRequest(t *testing.T) {
	for _, field := range []string{"repoOrURL", "target"} {
		manager := &installTestManager{}
		server := &Server{plugins: manager}
		response := httptest.NewRecorder()
		server.handlePluginInstall(response, httptest.NewRequest("POST", "/api/v1/plugins/install", strings.NewReader(`{"`+field+`":"owner/demo","tag":"v2","token":"test-private-token"}`)))
		if response.Code != 200 || manager.target != "owner/demo" || manager.token != "test-private-token" || manager.tag != "v2" {
			t.Fatalf("status=%d target=%q body=%s", response.Code, manager.target, response.Body.String())
		}
	}
}

func TestPluginInstallRejectsConflictingOrInvalidTargets(t *testing.T) {
	for _, body := range []string{`{"target":"owner/a","repoOrURL":"owner/b"}`, `{"target":"file:///tmp/a.zip"}`} {
		server := &Server{plugins: &installTestManager{}}
		response := httptest.NewRecorder()
		server.handlePluginInstall(response, httptest.NewRequest("POST", "/api/v1/plugins/install", strings.NewReader(body)))
		if response.Code != 400 {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

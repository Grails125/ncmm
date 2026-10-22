package ncmm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/3899/ncmm/internal/plugininstall"
)

func TestPluginInstallRecordsActualTagWithoutCredentials(t *testing.T) {
	p := NewPlugin(&Root{Opts: RootOpts{Home: t.TempDir()}}, nil)
	p.client.Transport = pluginInstallTransport(func(r *http.Request) (*http.Response, error) {
		body := pluginTestArchive(t)
		if strings.Contains(r.URL.Path, "/releases/latest") {
			body, _ = json.Marshal(map[string]any{"tag_name": "v2.4.0", "assets": []map[string]string{{"name": "ncmm-demo_" + runtime.GOOS + "_" + runtime.GOARCH + ".zip", "url": "https://api.github.com/assets/1"}}})
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	if _, err := p.InstallPlugin(context.Background(), PluginInstallOptions{Target: "owner/demo", Token: "private-secret", Tag: "latest"}, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(p.DefaultPluginDir(), "ncmm-demo", "source.json"))
	if err != nil {
		t.Fatal(err)
	}
	var source plugininstall.Source
	if err = json.Unmarshal(data, &source); err != nil {
		t.Fatal(err)
	}
	if source.Tag != "v2.4.0" || source.Repository != "owner/demo" || !source.Private || strings.Contains(string(data), "private-secret") {
		t.Fatalf("invalid source: %s", data)
	}
}

func TestPluginProxyFallsBackWithoutSharingPrivateToken(t *testing.T) {
	for _, token := range []string{"", "private-secret"} {
		t.Run(token, func(t *testing.T) {
			p := NewPlugin(&Root{Opts: RootOpts{Home: t.TempDir()}}, nil)
			mirrorCalls, directCalls := 0, 0
			p.client.Transport = pluginInstallTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "ghproxy.net" {
					mirrorCalls++
					if r.Header.Get("Authorization") != "" {
						t.Fatal("token leaked to mirror")
					}
					return &http.Response{StatusCode: 502, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
				}
				directCalls++
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(pluginTestArchive(t)))}, nil
			})
			_, err := p.InstallPlugin(context.Background(), PluginInstallOptions{Target: "https://github.com/owner/demo/releases/download/v2/demo.zip", Proxy: "https://ghproxy.net", Token: token}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if directCalls != 1 || (token == "" && mirrorCalls != 1) || (token != "" && mirrorCalls != 0) {
				t.Fatalf("mirror=%d direct=%d", mirrorCalls, directCalls)
			}
		})
	}
}

func TestPluginUpdateCheckUsesHEADAndPersistentDebounce(t *testing.T) {
	home := t.TempDir()
	p := NewPlugin(&Root{Opts: RootOpts{Home: home}}, nil)
	dir := filepath.Join(p.DefaultPluginDir(), "ncmm-demo")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "ncmm-demo.exe"), []byte("not executed"), 0755)
	source, _ := json.Marshal(plugininstall.Source{Repository: "owner/demo", Tag: "v1"})
	os.WriteFile(filepath.Join(dir, "source.json"), source, 0600)
	calls := 0
	transport := pluginInstallTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "HEAD" || r.URL.String() != "https://github.com/owner/demo/releases/latest" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL)
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://github.com/owner/demo/releases/tag/v2"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	p.client.Transport = transport
	updates, err := p.CheckPluginUpdates(context.Background(), true)
	if err != nil || len(updates) != 1 || !updates[0].UpdateAvailable || updates[0].LatestTag != "v2" {
		t.Fatalf("updates=%+v err=%v", updates, err)
	}
	p2 := NewPlugin(&Root{Opts: RootOpts{Home: home}}, nil)
	p2.client.Transport = transport
	if _, err = p2.CheckPluginUpdates(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("manual debounce failed across restart: %d", calls)
	}
	if _, err = p2.CheckPluginUpdates(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("automatic cache ignored")
	}
}

func TestPluginLatestTagMirrorFallbackValidatesRedirect(t *testing.T) {
	p := NewPlugin(&Root{Opts: RootOpts{Home: t.TempDir()}}, nil)
	var hosts []string
	p.client.Transport = pluginInstallTransport(func(r *http.Request) (*http.Response, error) {
		hosts = append(hosts, r.URL.Host)
		location := "https://github.com/owner/demo/releases/tag/v3"
		if r.URL.Host == "ghproxy.net" {
			location = "https://malicious.invalid/owner/demo/releases/tag/v9"
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {location}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	tag, err := p.latestPluginTag(context.Background(), "owner/demo", "https://ghproxy.net")
	if err != nil || tag != "v3" || strings.Join(hosts, ",") != "ghproxy.net,github.com" {
		t.Fatalf("tag=%s hosts=%v err=%v", tag, hosts, err)
	}
}

func TestPluginDaemonCannotRunAsTask(t *testing.T) {
	p := NewPlugin(&Root{Opts: RootOpts{Home: t.TempDir()}}, nil)
	dir := t.TempDir()
	path := filepath.Join(dir, "ncmm-demo.exe")
	os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"demo","type":"daemon"}`), 0600)
	_, err := p.ExecutePlugin(context.Background(), path, nil)
	if err == nil || !strings.Contains(err.Error(), "常驻插件") {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestPluginPrivateUpdateNeverUsesNetwork(t *testing.T) {
	p := NewPlugin(&Root{Opts: RootOpts{Home: t.TempDir()}}, nil)
	dir := filepath.Join(p.DefaultPluginDir(), "ncmm-demo")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "ncmm-demo.exe"), []byte("not executed"), 0755)
	source, _ := json.Marshal(plugininstall.Source{Repository: "owner/demo", Tag: "v1", Private: true, Proxy: "https://ghproxy.net"})
	os.WriteFile(filepath.Join(dir, "source.json"), source, 0600)
	p.client.Transport = pluginInstallTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("private background check reached network")
		return nil, nil
	})
	updates, err := p.CheckPluginUpdates(context.Background(), true)
	if err != nil || len(updates) != 1 || updates[0].Error == "" {
		t.Fatalf("updates=%+v err=%v", updates, err)
	}
}

func TestPluginUpgradeRejectsWrongPackageWithoutChangingInstallation(t *testing.T) {
	p := NewPlugin(&Root{Opts: RootOpts{Home: t.TempDir()}}, nil)
	_, err := p.InstallFromArchive(pluginArchiveFiles(t, map[string]string{"ncmm-demo.exe": "old", "config.yaml": "keep: true"}), "old.zip", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.installArchive(context.Background(), pluginArchiveFiles(t, map[string]string{"ncmm-other.exe": "different"}), "other.zip", "", "demo", nil)
	if err == nil {
		t.Fatal("accepted mismatching upgrade name")
	}
	got, err := os.ReadFile(filepath.Join(p.DefaultPluginDir(), "ncmm-demo", "ncmm-demo.exe"))
	if err != nil || string(got) != "old" {
		t.Fatalf("old plugin changed: %s %v", got, err)
	}
}

func TestPluginDiscoveryIgnoresStagingAndNonExecutableData(t *testing.T) {
	p := NewPlugin(&Root{Opts: RootOpts{Home: t.TempDir()}}, nil)
	_, err := p.InstallFromArchive(pluginArchiveFiles(t, map[string]string{"ncmm-demo.exe": "fake", "local.db": "data", "LICENSE": "notice"}), "demo.zip", "")
	if err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(p.DefaultPluginDir(), ".ncmm-install-old")
	os.MkdirAll(staging, 0755)
	os.WriteFile(filepath.Join(staging, "ncmm-ghost.exe"), []byte("never run"), 0755)
	plugins := p.DiscoverPlugins()
	if len(plugins) != 1 || plugins[0].Name != "demo" {
		t.Fatalf("non-plugin content discovered: %+v", plugins)
	}
}

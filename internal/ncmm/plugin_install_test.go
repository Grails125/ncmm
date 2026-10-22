package ncmm

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type pluginInstallTransport func(*http.Request) (*http.Response, error)

func (f pluginInstallTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func pluginTestArchive(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	entry, err := writer.Create("ncmm-demo.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("test fixture, never executed")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestPluginInstallRepositoryFormsRespectTagAndPrivateToken(t *testing.T) {
	for _, target := range []string{"owner/demo", "https://github.com/owner/demo", "https://github.com/owner/demo.git/"} {
		t.Run(target, func(t *testing.T) {
			home := t.TempDir()
			plugin := NewPlugin(&Root{Opts: RootOpts{Home: home}}, nil)
			archive := pluginTestArchive(t)
			var paths []string
			plugin.client = &http.Client{Transport: pluginInstallTransport(func(r *http.Request) (*http.Response, error) {
				paths = append(paths, r.URL.EscapedPath())
				if r.URL.Host != "api.github.com" || r.Header.Get("Authorization") != "Bearer test-private-token" {
					t.Fatalf("unexpected request host/auth: %s", r.URL.Host)
				}
				var body []byte
				switch r.URL.EscapedPath() {
				case "/repos/owner/demo/releases/tags/release%2Fv2":
					body, _ = json.Marshal(map[string]any{"tag_name": "release/v2", "assets": []map[string]any{{"name": "ncmm-demo_" + runtime.GOOS + "_" + runtime.GOARCH + ".zip", "url": "https://api.github.com/repos/owner/demo/releases/assets/123"}}})
				case "/repos/owner/demo/releases/assets/123":
					body = archive
				default:
					t.Fatalf("unexpected request: %s", r.URL.EscapedPath())
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
			})}
			name, err := plugin.InstallPlugin(context.Background(), PluginInstallOptions{Target: target, Tag: "release/v2", Token: "test-private-token"}, nil)
			if err != nil || name != "demo" || len(paths) != 2 {
				t.Fatalf("name=%q requests=%v err=%v", name, paths, err)
			}
			if _, err := os.Stat(filepath.Join(home, "plugins", "ncmm-demo", "ncmm-demo.exe")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPluginInstallDirectReleaseURL(t *testing.T) {
	plugin := NewPlugin(&Root{Opts: RootOpts{Home: t.TempDir()}}, nil)
	archive := pluginTestArchive(t)
	target := "https://github.com/owner/demo/releases/download/v2/demo.zip?download=1"
	plugin.client = &http.Client{Transport: pluginInstallTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != target {
			t.Fatalf("direct URL changed: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(archive)), Header: make(http.Header)}, nil
	})}
	if _, err := plugin.InstallPlugin(context.Background(), PluginInstallOptions{Target: target}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestPluginInstallRejectsAmbiguousTargetsBeforeNetwork(t *testing.T) {
	for _, options := range []PluginInstallOptions{
		{Target: "owner/repo/extra"},
		{Target: "https://github.com/owner/repo/tree/main"},
		{Target: "https://user:password@github.com/owner/repo"},
		{Target: "file:///tmp/plugin.zip"},
		{Target: "https://example.com/demo.zip", Tag: "v2"},
	} {
		plugin := NewPlugin(&Root{Opts: RootOpts{Home: t.TempDir()}}, nil)
		plugin.client = &http.Client{Transport: pluginInstallTransport(func(_ *http.Request) (*http.Response, error) {
			t.Fatal("invalid target reached network")
			return nil, nil
		})}
		if _, err := plugin.InstallPlugin(context.Background(), options, nil); err == nil {
			t.Fatalf("accepted target %q", options.Target)
		}
	}
}

func TestPluginInstallDoesNotSendGitHubTokenToOtherHosts(t *testing.T) {
	for _, target := range []string{"https://example.com/demo.zip?signature=private", "http://github.com/owner/demo/releases/download/v2/demo.zip"} {
		plugin := NewPlugin(&Root{Opts: RootOpts{Home: t.TempDir()}}, nil)
		plugin.client = &http.Client{Transport: pluginInstallTransport(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("Authorization") != "" {
				t.Fatal("GitHub token leaked to arbitrary download host")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(pluginTestArchive(t))), Header: make(http.Header)}, nil
		})}
		if _, err := plugin.InstallPlugin(context.Background(), PluginInstallOptions{Target: target, Token: "test-private-token"}, func(format string, args ...any) {
			if strings.Contains(fmt.Sprintf(format, args...), "signature") {
				t.Fatal("signed URL logged")
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
}

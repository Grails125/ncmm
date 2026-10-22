package ncmm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/internal/upgradepolicy"
	"github.com/3899/ncmm/pkg/log"
	"github.com/spf13/cobra"
)

func TestSelfUpdateKeepsFiveConfigurationBackups(t *testing.T) {
	root, executable := updaterFixture(t)
	downloads := fakeUpdateRelease(t, "v1.2.2-grails.2", false)
	root.CfgPath = filepath.Join(root.getHomePath(), "config.yaml")
	base := config.DefaultYAML()
	for i := 0; i < 6; i++ {
		data := append(append([]byte(nil), base...), []byte(fmt.Sprintf("\n# 自更新前修订 %d\n", i))...)
		if err := os.WriteFile(root.CfgPath, data, 0600); err != nil {
			t.Fatal(err)
		}
		if i < 5 {
			if _, err := config.BackupConfigFile(root.CfgPath, 5); err != nil {
				t.Fatal(err)
			}
		}
	}
	before, err := os.ReadFile(root.CfgPath)
	if err != nil {
		t.Fatal(err)
	}
	state, err := root.applyAvailableUpdateAt(executable)
	if err != nil || state.UpdateStatus != "success" || downloads.Load() != 1 {
		t.Fatalf("temporary self-update failed: %+v, %v, downloads=%d", state, err, downloads.Load())
	}
	files, err := filepath.Glob(filepath.Join(root.getHomePath(), "backups", "config_*.yaml"))
	if err != nil || len(files) != 5 {
		t.Fatalf("self-update kept %d configuration backups, want 5: %v", len(files), err)
	}
	contents := make(map[string]bool)
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		contents[string(data)] = true
	}
	for i := 1; i < 6; i++ {
		want := string(base) + fmt.Sprintf("\n# 自更新前修订 %d\n", i)
		if !contents[want] {
			t.Fatalf("self-update lost recent configuration revision %d", i)
		}
	}
	if after, err := os.ReadFile(root.CfgPath); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("self-update changed existing configuration: %v", err)
	}
}

func TestCLIUpgradeGraceNoticeUsesValidatedCacheOnce(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, name := range []string{"grace", "ordinary", "expired cache", "failed check", "future check", "invalid cache", "already current", "upstream version", "expired grace", "unknown current"} {
		t.Run(name, func(t *testing.T) {
			root, _ := updaterFixture(t)
			root.Cfg = &config.Config{Log: &log.Config{Stdout: false}}
			root.cmd = &cobra.Command{Use: "ncmm"}
			var stderr bytes.Buffer
			root.cmd.SetErr(&stderr)
			t.Setenv("NCMM_UPDATER_CHECK", "false")
			t.Setenv("NCMM_UPDATER_AUTO_UPDATE", "false")
			var requests atomic.Int32
			old := http.DefaultClient
			t.Cleanup(func() { http.DefaultClient = old })
			http.DefaultClient = &http.Client{Transport: updaterTransport(func(*http.Request) (*http.Response, error) {
				requests.Add(1)
				return nil, fmt.Errorf("unexpected network request")
			})}
			cache := upgradepolicy.Cache{LastCheckTime: now, RequiredVersion: "v1.2.2-grails.2", RequiredPublishedAt: now.Add(-24 * time.Hour)}
			switch name {
			case "ordinary":
				cache.RequiredVersion = ""
			case "expired cache":
				cache.LastCheckTime = now.Add(-25 * time.Hour)
			case "failed check":
				cache.LastCheckFailed = true
			case "future check":
				cache.LastCheckTime = now.Add(2 * time.Minute)
			case "already current":
				cache.RequiredVersion = root.AppVersion
			case "upstream version":
				cache.RequiredVersion = "v1.2.9"
			case "expired grace":
				cache.RequiredPublishedAt = now.AddDate(0, -2, 0)
			case "unknown current":
				root.AppVersion = "1.2.9"
			}
			data, err := json.Marshal(cache)
			if err != nil {
				t.Fatal(err)
			}
			if name == "invalid cache" {
				data = []byte(`{"broken":`)
			}
			if err := os.WriteFile(root.getUpdateStatePath(), data, 0600); err != nil {
				t.Fatal(err)
			}
			root.CheckForUpdatesPreRun()
			root.CheckForUpdatesPreRun()
			if after, err := os.ReadFile(root.getUpdateStatePath()); err != nil || !bytes.Equal(after, data) || requests.Load() != 0 {
				t.Fatalf("offline grace notice changed state or requested network: %v, requests=%d", err, requests.Load())
			}
			message := stderr.String()
			if name != "grace" {
				if message != "" {
					t.Fatalf("unexpected grace notice: %q", message)
				}
				if name == "expired grace" {
					err := upgradepolicy.Check(root.getHomePath(), root.AppVersion, now)
					if required, ok := err.(*upgradepolicy.RequiredError); !ok || required.ExitCode() != 3 {
						t.Fatalf("expired grace stopped blocking CLI: %v", err)
					}
				}
				return
			}
			if strings.Count(message, "\n") != 1 || !strings.Contains(message, root.AppVersion) || !strings.Contains(message, cache.RequiredVersion) || !strings.Contains(message, cache.RequiredPublishedAt.AddDate(0, 1, 0).Format(time.RFC3339)) {
				t.Fatalf("expected one visible notice with current/required versions and deadline, got %q", message)
			}
			if err := upgradepolicy.Check(root.getHomePath(), root.AppVersion, now); err != nil {
				t.Fatalf("grace notice blocked CLI: %v", err)
			}
		})
	}
}

func TestCLIUpgradeGraceNoticeSeesFreshCheckAndRecoveredCache(t *testing.T) {
	root, _ := updaterFixture(t)
	root.cmd = &cobra.Command{Use: "ncmm"}
	var stderr bytes.Buffer
	root.cmd.SetErr(&stderr)
	t.Setenv("NCMM_UPDATER_CHECK", "false")
	t.Setenv("NCMM_UPDATER_AUTO_UPDATE", "false")
	if err := os.WriteFile(root.getUpdateStatePath(), []byte(`{"broken":`), 0600); err != nil {
		t.Fatal(err)
	}
	root.CheckForUpdatesPreRun()
	if stderr.Len() != 0 {
		t.Fatal("invalid cache produced a grace notice")
	}
	now := time.Now().UTC().Truncate(time.Second)
	payload, err := json.Marshal(githubRelease{TagName: "v1.2.2-grails.2", Body: upgradepolicy.Marker, PublishedAt: now.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	old := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = old })
	http.DefaultClient = &http.Client{Transport: updaterTransport(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.URL.String() != "https://api.github.com/repos/Grails125/ncmm/releases/latest" {
			return nil, fmt.Errorf("unexpected update download")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(payload)), Header: make(http.Header)}, nil
	})}
	t.Setenv("NCMM_UPDATER_CHECK", "true")
	root.CheckForUpdatesPreRun()
	root.CheckForUpdatesPreRun()
	if requests.Load() != 1 || strings.Count(stderr.String(), "\n") != 1 || !strings.Contains(stderr.String(), "v1.2.2-grails.2") {
		t.Fatalf("fresh valid policy was not noticed once after recovery: %q, requests=%d", stderr.String(), requests.Load())
	}
}

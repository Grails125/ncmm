package ncmm

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/pkg/log"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func writeDiscoveryConfig(t *testing.T, path string) []byte {
	t.Helper()
	var cfg config.Config
	if err := yaml.Unmarshal(config.DefaultYAML(), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Notify.Enabled = false
	check := false
	cfg.Updater.Check = &check
	cfg.Accounts.Main = "${HOME}/primary.json"
	cfg.MusicPartner.ExtraLimitMin, cfg.MusicPartner.ExtraLimitMax = 4, 9
	cfg.MusicPartner.Comments = map[string][]string{"3": {"保留已有乐评"}}
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestConfigDiscoveryPriorityAndInstanceIsolation(t *testing.T) {
	for _, tc := range []struct {
		name, explicitConfig, home string
		files                      []string
		discover                   bool
		wantPath, wantHome         string
	}{
		{name: "existing default home wins", home: "instance", files: []string{"instance/config.yml", "config.yaml", "data/config.yaml"}, discover: true, wantPath: "instance/config.yml", wantHome: "instance"},
		{name: "current directory before children", home: "instance", files: []string{"config.yml", "data/config.yaml"}, discover: true, wantPath: "config.yml", wantHome: "."},
		{name: "data before template", home: "instance", files: []string{"data/config.yaml", "run/config.yaml", "config/config.yaml"}, discover: true, wantPath: "data/config.yaml", wantHome: "data"},
		{name: "run before template", home: "instance", files: []string{"run/config.yaml", "config/config.yaml"}, discover: true, wantPath: "run/config.yaml", wantHome: "run"},
		{name: "explicit home never searches elsewhere", home: "instance", files: []string{"config.yaml", "data/config.yaml"}, wantHome: "instance"},
		{name: "explicit config keeps selected home", explicitConfig: "other/custom.yaml", home: "instance", files: []string{"other/custom.yaml", "instance/config.yaml"}, discover: true, wantPath: "other/custom.yaml", wantHome: "instance"},
		{name: "standard names only", home: "instance", files: []string{"config.yaml.bak", "config.backup.yaml", "config.tmp.yaml", "config.yaml~", "backups/config.yaml", "tmp/config.yaml", "data/backups/config.yaml", "instance-b/config.yaml"}, discover: true, wantHome: "instance"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range tc.files {
				writeDiscoveryConfig(t, filepath.Join(dir, name))
			}
			got, err := findConfigLocation(tc.explicitConfig, tc.home, dir, tc.discover)
			if err != nil {
				t.Fatal(err)
			}
			wantPath := ""
			if tc.wantPath != "" {
				wantPath = filepath.Join(dir, tc.wantPath)
			}
			if got.Path != wantPath || got.Home != filepath.Join(dir, tc.wantHome) {
				t.Fatalf("location = %+v; want path=%q home=%q", got, wantPath, filepath.Join(dir, tc.wantHome))
			}
		})
	}
}

func TestConfigDiscoveryExplicitMissingOrNonFileFails(t *testing.T) {
	dir := t.TempDir()
	writeDiscoveryConfig(t, filepath.Join(dir, "data", "config.yaml"))
	if _, err := findConfigLocation("missing.yaml", "instance", dir, true); err == nil {
		t.Fatal("missing explicit configuration silently fell back")
	}
	if err := os.Mkdir(filepath.Join(dir, "config.yaml"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := findConfigLocation("", "instance", dir, true); err == nil {
		t.Fatal("directory named config.yaml silently fell back")
	}
}

func TestConfigDiscoveryEnvironmentHomeAndCLIOverride(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	t.Setenv("NCMM_HOME", "environment")
	t.Setenv("NCMM_DOCKER_OFFICIAL", "")
	writeDiscoveryConfig(t, filepath.Join(workingDir, "config.yaml"))
	root := New()
	location, err := root.resolveConfigLocation()
	if err != nil || location.Path != "" || location.Home != filepath.Join(workingDir, "environment") {
		t.Fatalf("environment home should stay isolated: %+v, %v", location, err)
	}
	writeDiscoveryConfig(t, filepath.Join(workingDir, "explicit", "config.yml"))
	if err := root.cmd.PersistentFlags().Set("home", "explicit"); err != nil {
		t.Fatal(err)
	}
	location, err = root.resolveConfigLocation()
	if err != nil || location.Path != filepath.Join(workingDir, "explicit", "config.yml") {
		t.Fatalf("CLI home did not override environment: %+v, %v", location, err)
	}
	if err := root.cmd.PersistentFlags().Set("home", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := root.resolveConfigLocation(); err == nil {
		t.Fatal("empty explicit home should fail instead of selecting another instance")
	}
}

func TestConfigDiscoveryDockerHomeDoesNotUseWorkingDirectory(t *testing.T) {
	workingDir, defaultHome := t.TempDir(), t.TempDir()
	t.Chdir(workingDir)
	t.Setenv("NCMM_HOME", "")
	t.Setenv("NCMM_DOCKER_OFFICIAL", "1")
	writeDiscoveryConfig(t, filepath.Join(workingDir, "data", "config.yaml"))
	root := New()
	root.Opts.Home = defaultHome
	location, err := root.resolveConfigLocation()
	if err != nil || location.Path != "" || location.Home != defaultHome {
		t.Fatalf("Docker home should stay isolated: %+v, %v", location, err)
	}
}

func TestConfigDiscoveryRecoveryLocatesInstanceWithoutParsingBrokenConfig(t *testing.T) {
	workingDir, defaultHome := t.TempDir(), t.TempDir()
	t.Chdir(workingDir)
	t.Setenv("NCMM_HOME", "")
	t.Setenv("NCMM_DOCKER_OFFICIAL", "")
	path := filepath.Join(workingDir, "data", "config.yaml")
	writeDiscoveryConfig(t, path)
	if err := os.WriteFile(path, []byte("broken: [yaml"), 0600); err != nil {
		t.Fatal(err)
	}
	root := New()
	root.Opts.Home = defaultHome
	command := &cobra.Command{Use: "status", Annotations: map[string]string{skipConfigAnnotation: "true"}}
	if err := root.cmd.PersistentPreRunE(command, nil); err != nil {
		t.Fatalf("recovery must not parse business configuration: %v", err)
	}
	if root.Cfg != nil || webAuthHome(root) != filepath.Dir(path) {
		t.Fatalf("wrong recovery instance: home=%q config=%v", webAuthHome(root), root.Cfg)
	}
	// 普通启动应报错，不能跳过坏文件改用其他目录的新实例。
	t.Setenv("NCMM_WEB_CHILD", "1")
	if err := root.cmd.PersistentPreRunE(&cobra.Command{Use: "update"}, nil); err == nil || !strings.Contains(err.Error(), "配置无效") {
		t.Fatalf("invalid selected configuration should fail: %v", err)
	}
}

func TestWebConfigReusesExistingFilesAndOnlyCreatesFirstDefault(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.yml")
	original := writeDiscoveryConfig(t, path)
	for _, configured := range []string{path, "", "default"} {
		actual, err := prepareWebConfig(context.Background(), home, configured)
		if err != nil || actual != path {
			t.Fatalf("existing configuration for %q = %q, %v", configured, actual, err)
		}
	}
	if actual, err := os.ReadFile(path); err != nil || !bytes.Equal(actual, original) {
		t.Fatalf("existing configuration was overwritten: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("a redundant config.yaml was created: %v", err)
	}
	newHome := t.TempDir()
	first, err := prepareWebConfig(context.Background(), newHome, "default")
	if err != nil {
		t.Fatal(err)
	}
	modified := writeDiscoveryConfig(t, first)
	second, err := prepareWebConfig(context.Background(), newHome, "default")
	if err != nil || first != second {
		t.Fatalf("second initialization = %q, %v", second, err)
	}
	if actual, err := os.ReadFile(second); err != nil || !bytes.Equal(actual, modified) {
		t.Fatalf("second initialization overwrote the user's configuration: %v", err)
	}
}

func TestWebRestartPinsResolvedInstancePaths(t *testing.T) {
	home := t.TempDir()
	configPath, settingsPath := filepath.Join(home, "config.yml"), filepath.Join(home, "settings.yaml")
	for _, args := range [][]string{
		{"web", "--listen", "127.0.0.1:1234"},
		{"--home", "./old", "-c", "../old/config.yaml", "web", "--web-config", "../settings.yaml", "--listen", "127.0.0.1:1234"},
		{"--home=./old", "--config=./old/config.yaml", "web", "--web-config=./old/webui.yaml", "--listen", "127.0.0.1:1234"},
		{"-c./old/config.yaml", "web", "--listen", "127.0.0.1:1234"},
	} {
		want := []string{"--home", home, "--config", configPath, "web", "--listen", "127.0.0.1:1234", "--web-config", settingsPath}
		if got := webRestartArgs(args, home, configPath, settingsPath); !reflect.DeepEqual(got, want) {
			t.Fatalf("restart arguments = %q; want %q", got, want)
		}
	}
}

func TestConfigDiscoveryRejectsUnrelatedConfigurationBeforeMigration(t *testing.T) {
	workingDir, defaultHome := t.TempDir(), t.TempDir()
	t.Chdir(workingDir)
	t.Setenv("NCMM_HOME", "")
	t.Setenv("NCMM_DOCKER_OFFICIAL", "")
	t.Setenv("NCMM_WEB_CHILD", "")
	path := filepath.Join(workingDir, "config.yaml")
	original := []byte("anotherApplication:\n  database: production\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	root := New()
	root.Opts.Home = defaultHome
	originalLogger := log.Default
	t.Cleanup(func() {
		if root.l != nil {
			_ = root.l.Close()
		}
		log.Default = originalLogger
	})
	if err := root.cmd.PersistentPreRunE(&cobra.Command{Use: "update"}, nil); err == nil {
		t.Fatal("unrelated configuration was accepted")
	}
	if actual, err := os.ReadFile(path); err != nil || !bytes.Equal(actual, original) {
		t.Fatalf("unrelated configuration was changed before validation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workingDir, "backups")); !os.IsNotExist(err) {
		t.Fatalf("unrelated configuration triggered migration: %v", err)
	}
}

func TestConfigDiscoveryDoesNotReplaceEmptyOrIncompleteConfiguration(t *testing.T) {
	version := "version: " + config.GetDefault().Version + "\n"
	for _, data := range []string{"", version, version + "network: {}\nlog: {}\n"} {
		t.Run(data, func(t *testing.T) {
			workingDir, defaultHome := t.TempDir(), t.TempDir()
			t.Chdir(workingDir)
			t.Setenv("NCMM_HOME", "")
			t.Setenv("NCMM_DOCKER_OFFICIAL", "")
			t.Setenv("NCMM_WEB_CHILD", "")
			path := filepath.Join(workingDir, "config.yaml")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			root := New()
			root.Opts.Home = defaultHome
			if err := root.cmd.PersistentPreRunE(&cobra.Command{Use: "update"}, nil); err == nil {
				t.Fatal("empty or incomplete automatic candidate was accepted")
			}
			if actual, err := os.ReadFile(path); err != nil || string(actual) != data {
				t.Fatalf("invalid configuration was overwritten: %v", err)
			}
		})
	}
}

func TestConfigDiscoveryMigratesLegacyFieldsWithoutLosingPartnerSettings(t *testing.T) {
	workingDir, defaultHome := t.TempDir(), t.TempDir()
	t.Chdir(workingDir)
	t.Setenv("NCMM_HOME", "")
	t.Setenv("NCMM_DOCKER_OFFICIAL", "")
	t.Setenv("NCMM_WEB_CHILD", "")
	path := filepath.Join(defaultHome, "config.yaml")
	var legacy map[string]interface{}
	if err := yaml.Unmarshal(writeDiscoveryConfig(t, path), &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["version"] = "1.1.14"
	legacy["musicianVip"] = legacy["musician"]
	delete(legacy, "musician")
	tasks := legacy["task"].(map[string]interface{})
	tasks["musicianVip"] = true
	delete(tasks, "musician-sign")
	accounts := legacy["accounts"].(map[string]interface{})
	accounts["primary"] = accounts["main"]
	delete(accounts, "main")
	for _, name := range []string{"playids", "sign"} {
		section := legacy[name].(map[string]interface{})
		section["enablePrimary"] = section["enableMain"]
		delete(section, "enableMain")
	}
	data, err := yaml.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	root := New()
	root.Opts.Home = defaultHome
	originalLogger := log.Default
	t.Cleanup(func() {
		if root.l != nil {
			_ = root.l.Close()
		}
		log.Default = originalLogger
	})
	if err := root.cmd.PersistentPreRunE(&cobra.Command{Use: "update"}, nil); err != nil {
		t.Fatalf("legacy discovery migration failed: %v", err)
	}
	if root.Cfg.Version != config.GetDefault().Version || root.Cfg.Musician == nil || !root.Cfg.Task.MusicianSign {
		t.Fatalf("legacy configuration was not migrated: %+v", root.Cfg.Task)
	}
	if filepath.Clean(root.Cfg.Accounts.Main) != filepath.Join(defaultHome, "primary.json") || root.Cfg.MusicPartner.ExtraLimitMax != 9 || root.Cfg.MusicPartner.Comments["3"][0] != "保留已有乐评" {
		t.Fatal("legacy migration lost account or music partner configuration")
	}
}

func TestConfigDiscoveryReusesWorkingDirectoryDataAndPreservesPartner(t *testing.T) {
	workingDir, defaultHome := t.TempDir(), t.TempDir()
	t.Chdir(workingDir)
	t.Setenv("NCMM_HOME", "")
	t.Setenv("NCMM_DOCKER_OFFICIAL", "")
	t.Setenv("NCMM_WEB_CHILD", "1")
	originalHome, originalLogger := config.HomeDir, log.Default
	config.HomeDir = defaultHome
	t.Cleanup(func() { config.HomeDir, log.Default = originalHome, originalLogger })
	path := filepath.Join(workingDir, "data", "config.yaml")
	original := writeDiscoveryConfig(t, path)
	root := New()
	// 仅运行配置初始化，update 名称避开联网更新检查。
	if err := root.cmd.PersistentPreRunE(&cobra.Command{Use: "update"}, nil); err != nil {
		t.Fatal(err)
	}
	defer root.l.Close()
	if root.CfgPath != path || root.Opts.Home != filepath.Dir(path) {
		t.Fatalf("location = %q, %q; want %q, %q", root.CfgPath, root.Opts.Home, path, filepath.Dir(path))
	}
	if filepath.Clean(root.Cfg.Accounts.Main) != filepath.Join(filepath.Dir(path), "primary.json") {
		t.Fatalf("account home was not resolved: %q", root.Cfg.Accounts.Main)
	}
	if root.Cfg.MusicPartner.ExtraLimitMax != 9 || root.Cfg.MusicPartner.Comments["3"][0] != "保留已有乐评" {
		t.Fatal("existing music partner configuration changed")
	}
	if actual, err := os.ReadFile(path); err != nil || !bytes.Equal(actual, original) {
		t.Fatalf("discovery rewrote existing configuration: %v", err)
	}
	if _, err := os.Stat(filepath.Join(defaultHome, "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("unexpected default configuration: %v", err)
	}
}

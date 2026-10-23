package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func writeCompatibilityConfig(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOfficial129ConfigReadAndPreview(t *testing.T) {
	text := strings.Replace(string(DefaultYAML()), "version: "+GetDefault().Version, "version: 1.2.9", 1)
	if !strings.Contains(strings.ReplaceAll(text, "\r\n", "\n"), "\nplay:\n") {
		text += "\nplay:\n  webSession: true\n"
	}
	path := writeCompatibilityConfig(t, text)
	for _, load := range []func(string) (*Config, error){func(path string) (*Config, error) { return New(path) }, PreviewConfigMigration} {
		cfg, err := load(path)
		if err != nil {
			t.Fatalf("official config rejected: %v", err)
		}
		encoded, err := yaml.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "webSession: true") {
			t.Fatal("deprecated play session remained enabled")
		}
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != text {
		t.Fatalf("read/preview rewrote file: %v", err)
	}
}

func TestSessionSafetyMigrationRunsOnlyOnceForOfficialLegacy(t *testing.T) {
	for _, item := range []struct {
		version string
		want    bool
	}{{"1.2.3", false}, {"1.2.7", false}, {"1.2.8", true}, {"1.2.9", true}, {"1.2.2-grails.1", true}, {"1.2.3-grails", true}, {"v1.2.3-grails", true}} {
		t.Run(item.version, func(t *testing.T) {
			path := writeCompatibilityConfig(t, fmt.Sprintf("version: %s\nfansgroup:\n  webSession: true # 用户说明\n", item.version))
			if err := AutoUpgradeConfig(path); err != nil {
				t.Fatal(err)
			}
			cfg, err := New(path)
			if err != nil || cfg.FansGroup.WebSession != item.want {
				t.Fatalf("session = %+v, %v", cfg, err)
			}
			data, _ := os.ReadFile(path)
			data = bytes.Replace(data, []byte("webSession: false # 用户说明"), []byte("webSession: true # 用户说明"), 1)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := AutoUpgradeConfigIfNeeded(path); err != nil {
				t.Fatal(err)
			}
			cfg, err = New(path)
			if err != nil || !cfg.FansGroup.WebSession {
				t.Fatalf("manual choice changed on next upgrade: %v", err)
			}
		})
	}
}

func TestSessionSafetyMigrationRecognizesNativeGrailsVersions(t *testing.T) {
	for _, item := range []struct {
		version string
		want    bool
	}{{"1.2.3", true}, {"v1.2.3", true}, {"1.2.2-grails.1", false}, {"1.2.3-grails", false}, {"v1.2.3-grails", false}, {"1.2.3-grals", true}, {"v1.2.3-grals", true}} {
		t.Run(item.version, func(t *testing.T) {
			if got := needsOfficialSessionSafetyMigration(item.version); got != item.want {
				t.Fatalf("needsOfficialSessionSafetyMigration(%q) = %t，预期 %t", item.version, got, item.want)
			}
		})
	}
}

func TestUpgradePreservesSourceCommentsStyleAndUntouchedText(t *testing.T) {
	const source = "# 我的配置 🎵\r\nversion: '1.2.1' # 版本备注\r\n\r\n\r\n# 网络原说明\r\nnetwork:\r\n    # 超时原说明\r\n    timeout: '31s' # 超时行尾\r\n\r\n# 账号原说明\r\naccounts: {main: './我的账号.json', antiCheatTokens: {a: token-a}} # inline mapping\r\n\r\n# 日志原说明\r\nlog:\r\n    app: |\r\n        名字 # 不是注释\r\n        第二行\r\n    level: 'debug' # 日志行尾\r\n"
	path := writeCompatibilityConfig(t, source)
	if err := AutoUpgradeConfig(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"# 我的配置 🎵\r\n", "version: '" + GetDefault().Version + "' # 版本备注", "\r\n\r\n\r\n# 网络原说明", "    # 超时原说明\r\n    timeout: '31s' # 超时行尾", "# 账号原说明", "# inline mapping", "    app: |\r\n        名字 # 不是注释\r\n        第二行\r\n    level: 'debug' # 日志行尾"} {
		if !bytes.Contains(data, []byte(part)) {
			t.Errorf("source text lost: %q", part)
		}
	}
	if _, err := New(path); err != nil {
		t.Fatalf("merged YAML invalid: %v", err)
	}
}

func TestUpgradeRejectsUnknownFieldsWithoutWriting(t *testing.T) {
	for _, source := range []string{"version: 1.2.9\nfutureField: keep-me\n", "version: 1.2.10\n", "version: 2.0.0\n"} {
		path := writeCompatibilityConfig(t, source)
		if err := AutoUpgradeConfig(path); err == nil {
			t.Fatal("unknown configuration accepted")
		}
		got, _ := os.ReadFile(path)
		if string(got) != source {
			t.Fatal("rejected configuration was modified")
		}
	}
}

func TestUpgradePreservesComplexYAMLAndIsIdempotent(t *testing.T) {
	for name, source := range map[string]string{
		"aliases":     "version: 1.2.1\naccounts:\n  main: &main './音乐🎵.json' # 主账号\n  secondary: [*main] # 别名列表\nnetwork:\n  timeout: 21s\nlog:\n  app: >-\n    多行\n    名字\n  level: debug\n...\n",
		"flow":        "# 原流式文档\n{version: '1.2.1', accounts: {main: './a.json'}, log: {app: a}, network: {timeout: 21s}} # 最后备注\n",
		"legacy keys": "# 保留段\nversion: 1.1.14\naccounts:\n  primary: './a.json' # 保留账号\nmusicianVip:\n  enableMain: true\ntask:\n  musicianVip: true\nfansGroup:\n  webSession: true\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeCompatibilityConfig(t, source)
			if err := AutoUpgradeConfigIfNeeded(path); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := New(path); err != nil {
				t.Fatal(err)
			}
			if err := AutoUpgradeConfigIfNeeded(path); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("second start rewrote the file")
			}
			for _, comment := range []string{"# 保留段", "# 保留账号", "# 主账号", "# 别名列表", "# 原流式文档", "# 最后备注"} {
				if strings.Contains(source, comment) && !bytes.Contains(after, []byte(comment)) {
					t.Fatalf("comment lost: %s", comment)
				}
			}
			if name == "aliases" && (!bytes.Contains(after, []byte("&main './音乐🎵.json'")) || !bytes.Contains(after, []byte("secondary: [*main]")) || !bytes.Contains(after, []byte("  app: >-\n    多行\n    名字\n"))) {
				t.Fatal("alias or folded scalar changed")
			}
		})
	}
}

func TestUpgradeRejectsAmbiguousYAMLWithoutWriting(t *testing.T) {
	for _, source := range []string{"version: 1.2.9\nversion: 1.2.8\n", "version: 1.2.9\n---\nversion: 1.2.2\n", "version: 1.2.9\naccounts:\n  primary: a\n  main: b\n"} {
		path := writeCompatibilityConfig(t, source)
		if err := AutoUpgradeConfig(path); err == nil {
			t.Fatal("ambiguous YAML was accepted")
		}
		got, _ := os.ReadFile(path)
		if string(got) != source {
			t.Fatal("ambiguous source was changed")
		}
	}
}

func TestDefaultBackupWindowKeepsFiveDifferentVersions(t *testing.T) {
	path := writeCompatibilityConfig(t, "version: 1.2.1\n")
	now := time.Date(2000, time.January, 2, 3, 4, 5, 647000000, time.UTC)
	var latest []string
	for i := 0; i < 7; i++ {
		content := []byte(fmt.Sprintf("version: 1.2.1\n# 编辑 %d\n", i))
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
		backup, err := backupConfigFileAt(path, 0, now)
		if err != nil {
			t.Fatal(err)
		}
		for _, previous := range latest {
			if backup == previous {
				t.Fatalf("different contents reused backup path %q", backup)
			}
		}
		if got, err := os.ReadFile(backup); err != nil || !bytes.Equal(got, content) {
			t.Fatalf("new backup content = %q, %v", got, err)
		}
		if err := os.Chtimes(backup, now, now); err != nil {
			t.Fatal(err)
		}
		latest = append(latest, backup)
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(path), "backups", "config_*.yaml"))
	if err != nil || len(files) != 5 {
		t.Fatalf("backup count = %d, %v", len(files), err)
	}
	for i, backup := range latest {
		got, err := os.ReadFile(backup)
		if i < 2 {
			if !os.IsNotExist(err) {
				t.Fatalf("old backup not pruned: %s, %v", backup, err)
			}
			continue
		}
		if want := fmt.Sprintf("version: 1.2.1\n# 编辑 %d\n", i); err != nil || string(got) != want {
			t.Fatalf("latest backup content = %q, %v; want %q", got, err, want)
		}
	}
	if duplicate, err := backupConfigFileAt(path, 0, now); err != nil || duplicate != latest[len(latest)-1] {
		t.Fatalf("latest identical backup was not reused: %q, %v", duplicate, err)
	}
}

func TestBackupOrderingWithLegacyNamesAndEqualModificationTimes(t *testing.T) {
	path := writeCompatibilityConfig(t, "version: 1.2.1\n")
	now := time.Date(2000, time.January, 2, 3, 4, 5, 647000000, time.UTC)
	dir := filepath.Join(filepath.Dir(path), "backups")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	var paths []string
	var contents []string
	stamp := now.Format("20060102_150405")
	for i, name := range []string{"config_" + stamp + ".yaml", "config_" + stamp + "_001.yaml", "config_" + stamp + "_999.yaml"} {
		backup := filepath.Join(dir, name)
		content := fmt.Sprintf("version: 1.2.1\n# 旧备份 %d\n", i)
		if err := os.WriteFile(backup, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(backup, now, now); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, backup)
		contents = append(contents, content)
	}
	for i := 0; i < 4; i++ {
		content := fmt.Sprintf("version: 1.2.1\n# 新备份 %d\n", i)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		backup, err := backupConfigFileAt(path, 0, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(backup, now, now); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, backup)
		contents = append(contents, content)
		if duplicate, err := backupConfigFileAt(path, 0, now); err != nil || duplicate != backup {
			t.Fatalf("newest equal-mtime backup not reused: %q, %v; want %q", duplicate, err, backup)
		}
	}
	files, err := filepath.Glob(filepath.Join(dir, "config_*.yaml"))
	if err != nil || len(files) != 5 {
		t.Fatalf("retained backups = %d, %v", len(files), err)
	}
	for i, backup := range paths {
		got, err := os.ReadFile(backup)
		if i < 2 {
			if !os.IsNotExist(err) {
				t.Fatalf("old legacy backup not pruned: %s, %v", backup, err)
			}
			continue
		}
		if err != nil || string(got) != contents[i] {
			t.Fatalf("retained backup = %q, %v; want %q", got, err, contents[i])
		}
	}
}

func TestBackupDoesNotOverwriteOrPruneReservedName(t *testing.T) {
	path := writeCompatibilityConfig(t, "version: 1.2.1\n")
	now := time.Date(2000, time.January, 2, 3, 4, 5, 647000000, time.UTC)
	dir := filepath.Join(filepath.Dir(path), "backups")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	reserved := filepath.Join(dir, "config_"+now.Format("20060102_150405")+"_00000000000000000001.yaml")
	if err := os.WriteFile(reserved, nil, 0600); err != nil {
		t.Fatal(err)
	}
	backup, err := backupConfigFileAt(path, 1, now)
	if err != nil || backup == reserved {
		t.Fatalf("reserved name was reused: %q, %v", backup, err)
	}
	if data, err := os.ReadFile(reserved); err != nil || len(data) != 0 {
		t.Fatalf("incomplete reservation was overwritten or pruned: %q, %v", data, err)
	}
}

func TestNewDefaultsMatchPublished129WithoutChangingNativePartner(t *testing.T) {
	cfg := GetDefault()
	if !cfg.Task.MusicianVip || cfg.Task.DailySongShare || cfg.Sign.EnableSecondaries || cfg.FansGroup.EnableSecondaries || !cfg.VipMemberGift.EnableSecondaries {
		t.Fatal("new account/task defaults still differ from public 1.2.9")
	}
	if cfg.PlayIds.DailyMin != 80 || cfg.PlayIds.DailyMax != 200 || cfg.PlayIds.RunMin != 40 || cfg.PlayIds.RunMax != 80 || cfg.PlayIds.IDs != "3366663042" || len(cfg.PlayIds.PlaylistIDs) != 1 || cfg.PlayIds.PlaylistIDs[0] != "18425795840" {
		t.Fatal("new playback defaults still differ from public 1.2.9")
	}
	if cfg.Task.MusicPartner || cfg.MusicPartner.ScoreMin != 3 || cfg.MusicPartner.ScoreMax != 4 || cfg.MusicPartner.WaitMin != 15 || cfg.MusicPartner.WaitMax != 20 {
		t.Fatal("native partner defaults changed")
	}
}

func TestLegacySessionAliasDoesNotPreventSafeMigration(t *testing.T) {
	path := writeCompatibilityConfig(t, "version: 1.2.7\ntask:\n  sign: &enabled true\nfansgroup:\n  webSession: *enabled # 仅重置乐迷团\n")
	if err := AutoUpgradeConfig(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := New(path)
	if err != nil || cfg.FansGroup.WebSession || !cfg.Task.Sign {
		t.Fatalf("migration changed unrelated value: %+v, %v", cfg, err)
	}
}

func TestMappingAliasesKeepIndependentConfigurationValues(t *testing.T) {
	for _, source := range []string{
		"version: 1.2.9\nplay: &session {webSession: true}\nfansgroup: *session\n",
		"version: 1.2.9\nfansgroup: &session {webSession: true}\nplay: *session\n",
	} {
		path := writeCompatibilityConfig(t, source)
		if _, err := New(path); err != nil {
			t.Fatalf("source unreadable: %v", err)
		}
		if err := AutoUpgradeConfig(path); err != nil {
			t.Fatal(err)
		}
		cfg, err := New(path)
		if err != nil || !cfg.FansGroup.WebSession || cfg.Play.WebSession {
			t.Fatalf("shared mapping changed unrelated session values: %+v, %v", cfg, err)
		}
	}
}

func TestVersionAnchorDoesNotChangeOtherValues(t *testing.T) {
	path := writeCompatibilityConfig(t, "version: &release 1.2.9\nlog: {app: *release}\n")
	if err := AutoUpgradeConfig(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := New(path)
	if err != nil || cfg.Log.App != "1.2.9" {
		t.Fatalf("version changed log app: %+v, %v", cfg, err)
	}
}

func TestMergedYAMLValuesAreNotReplacedByDefaults(t *testing.T) {
	for _, source := range []string{
		"version: 1.2.9\n<<: {accounts: {main: './mine.json'}, task: {sign: false}, playids: {daily_min: 13}}\n",
		"version: 1.2.9\naccounts: {<<: {main: './mine.json'}}\ntask: {<<: [{sign: false}, {sign: true}]}\nplayids: {<<: {daily_min: 13}}\n",
	} {
		path := writeCompatibilityConfig(t, source)
		if _, err := New(path); err != nil {
			t.Fatalf("source unreadable: %v", err)
		}
		if err := AutoUpgradeConfig(path); err != nil {
			t.Fatal(err)
		}
		cfg, err := New(path)
		if err != nil || cfg.Accounts.Main != "./mine.json" || cfg.Task.Sign || cfg.PlayIds.DailyMin != 13 {
			t.Fatalf("merged user values overwritten: %+v, %v", cfg, err)
		}
	}
}

func TestUpgradeSupportsBOMAndBlockVersion(t *testing.T) {
	for _, source := range []string{"\ufeffversion: 1.2.9\n", "version: >-\n  1.2.9\n# next\nlog: {app: mine}\n", "version: | # 版本\r\n    1.2.9\r\n\r\n# next\r\nlog: {app: mine}\r\n"} {
		path := writeCompatibilityConfig(t, source)
		if err := AutoUpgradeConfig(path); err != nil {
			t.Fatal(err)
		}
		if _, err := New(path); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(path)
		if strings.HasPrefix(source, "\ufeff") && !bytes.HasPrefix(got, []byte("\xef\xbb\xbf")) {
			t.Fatal("BOM lost")
		}
		if strings.Contains(source, "version: >-") && !bytes.Contains(got, []byte("version: >-\n  "+GetDefault().Version+"\n# next")) {
			t.Fatal("block scalar style or comment lost")
		}
	}
}

func TestUpgradeRejectsRecursiveAliasesWithoutWriting(t *testing.T) {
	source := "version: 1.2.9\nfansgroup: &session {webSession: false, groupIds: *session}\n"
	path := writeCompatibilityConfig(t, source)
	if err := AutoUpgradeConfig(path); err == nil {
		t.Fatal("recursive alias accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != source {
		t.Fatalf("rejected source changed: %s, %v", got, err)
	}
}

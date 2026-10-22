package ncmm

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/pkg/database"
	"github.com/3899/ncmm/pkg/log"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func TestRootDataPathsUseSelectedHomeWithoutChangingWorkingDirectory(t *testing.T) {
	for _, mode := range []string{"explicit-home", "environment-home", "discovered-home", "explicit-config"} {
		t.Run(mode, func(t *testing.T) {
			cwd := t.TempDir()
			t.Chdir(cwd)
			t.Setenv("NCMM_HOME", "")
			t.Setenv("NCMM_DOCKER_OFFICIAL", "")
			t.Setenv("NCMM_WEB_CHILD", "1")
			t.Setenv("NCMM_UPDATER_CHECK", "false")
			t.Setenv("NCMM_UPDATER_AUTO_UPDATE", "false")
			home := filepath.Join(cwd, "instance")
			configPath := filepath.Join(home, "config.yaml")
			root := New()
			root.Opts.Home = home
			switch mode {
			case "explicit-home":
				_ = root.cmd.PersistentFlags().Set("home", "instance")
			case "environment-home":
				t.Setenv("NCMM_HOME", "instance")
			case "discovered-home":
				home = filepath.Join(cwd, "run")
				configPath = filepath.Join(home, "config.yml")
			case "explicit-config":
				configPath = filepath.Join(cwd, "configuration", "custom.yaml")
				_ = root.cmd.PersistentFlags().Set("config", configPath)
			}
			cfg, err := newDefaultRuntimeConfig()
			if err != nil {
				t.Fatal(err)
			}
			cfg.Notify.Enabled = false
			cfg.Accounts.Main = "./cookie.json"
			cfg.Accounts.Secondary = []string{"./fan.json"}
			cfg.Accounts.AntiCheatTokens = map[string]string{"./cookie.json": "main-test-token", "${HOME}/fan.json": "fan-test-token"}
			cfg.FansGroup.CommentCheckTokens = map[string]string{"cookie.json": "web-test-token"}
			outside := filepath.Join(cwd, "absolute-source.txt")
			cfg.PlayIds.IDsFile = config.StringOrSlice{"songs.txt", outside, "https://example.com/songs.txt"}
			cfg.Musician.Play.IDsFile = config.StringOrSlice{"${HOME}/musician.txt"}
			cfg.Note.TitlesFile = config.StringOrSlice{"titles.txt"}
			cfg.Note.MessagesFile = config.StringOrSlice{"messages.txt"}
			cfg.Note.ImageURLs = config.StringOrSlice{"cover.png", "https://example.com/cover.png"}
			cfg.DailySongShare.TitlesFile = config.StringOrSlice{"daily-titles.txt"}
			cfg.MusicPartner.Comments = map[string][]string{"3": {"保留乐评"}}
			partner := *cfg.MusicPartner
			original, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(home, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configPath, original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, "cookie.json"), []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cwd, "cookie.json"), []byte("not-json-cwd-marker"), 0600); err != nil {
				t.Fatal(err)
			}
			previousLogger := log.Default
			t.Cleanup(func() {
				if root.l != nil {
					_ = root.l.Close()
				}
				log.Default = previousLogger
			})
			if err := root.cmd.PersistentPreRunE(&cobra.Command{Use: "update"}, nil); err != nil {
				t.Fatal(err)
			}
			if root.Opts.Home != home || root.CfgPath != configPath {
				t.Fatalf("selected home/config changed: %q, %q", root.Opts.Home, root.CfgPath)
			}
			for field, paths := range map[string][2]string{
				"main":            {root.Cfg.Accounts.Main, filepath.Join(home, "cookie.json")},
				"secondary":       {root.Cfg.Accounts.Secondary[0], filepath.Join(home, "fan.json")},
				"network cookie":  {root.Cfg.Network.Cookie.Filepath, filepath.Join(home, "cookie.json")},
				"database":        {root.Cfg.Database.Path, filepath.Join(home, "database", "badger")},
				"log":             {root.Cfg.Log.Rotate.Filename, filepath.Join(home, "log", "ncm.log")},
				"song file":       {root.Cfg.PlayIds.IDsFile[0], filepath.Join(home, "songs.txt")},
				"absolute source": {root.Cfg.PlayIds.IDsFile[1], outside},
				"remote source":   {root.Cfg.PlayIds.IDsFile[2], "https://example.com/songs.txt"},
				"musician file":   {root.Cfg.Musician.Play.IDsFile[0], filepath.Join(home, "musician.txt")},
				"title file":      {root.Cfg.Note.TitlesFile[0], filepath.Join(home, "titles.txt")},
				"message file":    {root.Cfg.Note.MessagesFile[0], filepath.Join(home, "messages.txt")},
				"local image":     {root.Cfg.Note.ImageURLs[0], filepath.Join(home, "cover.png")},
				"remote image":    {root.Cfg.Note.ImageURLs[1], "https://example.com/cover.png"},
				"daily title":     {root.Cfg.DailySongShare.TitlesFile[0], filepath.Join(home, "daily-titles.txt")},
			} {
				if paths[0] != paths[1] {
					t.Errorf("%s = %q; want %q", field, paths[0], paths[1])
				}
			}
			if root.Cfg.Accounts.AntiCheatTokenFor(filepath.Join(home, "cookie.json")) != "main-test-token" || root.Cfg.Accounts.AntiCheatTokenFor(filepath.Join(home, "fan.json")) != "fan-test-token" || root.Cfg.FansGroup.CommentCheckTokenFor(filepath.Join(home, "cookie.json")) != "web-test-token" {
				t.Error("account token mapping no longer matches the instance paths")
			}
			if !reflect.DeepEqual(partner, *root.Cfg.MusicPartner) {
				t.Error("music partner settings changed")
			}
			current, _ := os.ReadFile(configPath)
			if !bytes.Equal(current, original) {
				t.Error("runtime normalization rewrote the YAML configuration")
			}
			actualCWD, _ := os.Getwd()
			if actualCWD != cwd {
				t.Fatal("configuration initialization changed the process working directory")
			}
			root.Cfg.Network.Cookie.ReadOnly = true
			client, err := api.NewClient(root.Cfg.Network, nil)
			if err != nil {
				t.Fatalf("client read the wrong cookie: %v", err)
			}
			_ = client.Close(context.Background())
			db, err := database.NewWithOptions(root.Cfg.Database, 1, 0, true)
			if err != nil {
				t.Fatal(err)
			}
			_ = db.Close(context.Background())
			log.Info("isolated path regression")
			for _, path := range []string{filepath.Join(home, "database", "badger"), filepath.Join(home, "log", "ncm.log")} {
				if _, err := os.Stat(path); err != nil {
					t.Errorf("home data missing: %s", path)
				}
			}
			for _, path := range []string{filepath.Join(cwd, "database"), filepath.Join(cwd, "log")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("data leaked into caller directory: %s", path)
				}
			}
		})
	}
}

func TestRuntimePathsAreIdempotentAndKeepNotifyConfigRelative(t *testing.T) {
	home := t.TempDir()
	cfg, err := newDefaultRuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Accounts.Main = "./cookie.json"
	cfg.Notify.File = "./channels/notify.yaml"
	cfg.Accounts.AntiCheatTokens = map[string]string{"cookie.json": "", "./cookie.json": "same", "${HOME}/cookie.json": "same", filepath.Join(home, "cookie.json"): "same"}
	cfg.FansGroup.CommentCheckTokens = map[string]string{"cookie.json": "same", "./cookie.json": "same", "${HOME}/cookie.json": "same", filepath.Join(home, "cookie.json"): "same"}
	if err := normalizeRuntimePaths(cfg, home); err != nil {
		t.Fatal(err)
	}
	first, _ := yaml.Marshal(cfg)
	if err := normalizeRuntimePaths(cfg, home); err != nil {
		t.Fatal(err)
	}
	second, _ := yaml.Marshal(cfg)
	if !bytes.Equal(first, second) {
		t.Fatal("normalizing resolved paths changed the configuration again")
	}
	if cfg.Notify.File != "./channels/notify.yaml" {
		t.Fatal("notify.file must stay relative to the configuration file")
	}
	if len(cfg.Accounts.AntiCheatTokens) != 1 || len(cfg.FansGroup.CommentCheckTokens) != 1 || cfg.Accounts.AntiCheatTokenFor(cfg.Accounts.Main) != "same" || cfg.FansGroup.CommentCheckTokenFor(cfg.Accounts.Main) != "same" {
		t.Fatal("equivalent account aliases did not retain the effective token")
	}
}

func TestResolveHomePathPreservesAbsoluteAndRemoteSources(t *testing.T) {
	home := t.TempDir()
	for _, source := range []string{filepath.Join(home, "${HOME}", "source.txt"), "https://example.com/source.txt?value=${HOME}", "HTTP://example.com/source.txt"} {
		if got := resolveHomePath(source, home); got != source {
			t.Fatalf("resolved source = %q; want unchanged %q", got, source)
		}
	}
}

func TestRuntimePathsRejectConflictingAccountTokenAliases(t *testing.T) {
	for _, field := range []string{"accounts", "fansGroup"} {
		t.Run(field, func(t *testing.T) {
			cfg, err := newDefaultRuntimeConfig()
			if err != nil {
				t.Fatal(err)
			}
			tokens := map[string]string{"cookie.json": "private-one", "${HOME}/cookie.json": "private-two"}
			if field == "accounts" {
				cfg.Accounts.AntiCheatTokens = tokens
			} else {
				cfg.FansGroup.CommentCheckTokens = tokens
			}
			err = normalizeRuntimePaths(cfg, t.TempDir())
			if err == nil || strings.Contains(err.Error(), "private-") {
				t.Fatal("conflicting aliases must fail without printing either token")
			}
		})
	}
}

func TestDefaultRuntimeConfigIsNotSharedAcrossHomes(t *testing.T) {
	before, _ := yaml.Marshal(config.GetDefault())
	for range 2 {
		cfg, err := newDefaultRuntimeConfig()
		if err != nil {
			t.Fatal(err)
		}
		home := t.TempDir()
		if err := normalizeRuntimePaths(cfg, home); err != nil {
			t.Fatal(err)
		}
		if cfg.Accounts.Main != filepath.Join(home, "cookie.json") {
			t.Fatal("default runtime configuration leaked a previous home")
		}
	}
	after, _ := yaml.Marshal(config.GetDefault())
	if !bytes.Equal(before, after) {
		t.Fatal("runtime configuration mutated shared defaults")
	}
}

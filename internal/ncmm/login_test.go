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
	"github.com/3899/ncmm/internal/loginresult"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func TestSaveLoginResultNoConfigWrite(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "config.yaml")
	original := config.DefaultYAML()
	if err := os.WriteFile(configPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.New(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ReplaceMagicVariables("HOME", home)
	cfg.Network.Cookie.Filepath = filepath.Join(home, "cookie.json")
	root := &Root{Cfg: cfg, CfgPath: configPath, Opts: RootOpts{Home: home}}
	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&output)
	login := &Login{root: root, cmd: cmd, noConfigWrite: true, jsonResult: true}
	tempCookie := filepath.Join(home, "incoming.cookie")
	if err := os.WriteFile(tempCookie, []byte(`{"MUSIC_U":"test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := login.saveLoginResult(context.Background(), "tester", "https://p1.music.126.net/avatar.jpg", 123, tempCookie, "fan1.json", ""); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, original) {
		t.Fatal("--no-config-write modified config.yaml")
	}
	result, err := loginresult.Parse(output.String())
	if err != nil {
		t.Fatal(err)
	}
	if result.UID != 123 || result.AvatarURL != "https://p1.music.126.net/avatar.jpg" || result.Main || filepath.Base(result.CookiePath) != "fan1.json" {
		t.Fatalf("unexpected structured result: %+v", result)
	}
	if _, err := os.Stat(result.CookiePath); err != nil {
		t.Fatalf("Cookie output missing: %v", err)
	}
}

func TestFormatHomePathUsesInstanceBoundary(t *testing.T) {
	home := filepath.Join(t.TempDir(), "instance")
	for _, item := range []struct{ path, want string }{
		{filepath.Join(home, "new-fan.json"), "${HOME}/new-fan.json"},
		{filepath.Join(home+"-sibling", "fan.json"), filepath.ToSlash(filepath.Join(home+"-sibling", "fan.json"))},
	} {
		if got := formatHomePath(item.path, home); got != item.want {
			t.Fatalf("formatHomePath = %q; want %q", got, item.want)
		}
	}
}

func TestLoginCookieInputFileRemainsRelativeToCaller(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "incoming.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "incoming.json"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := newDefaultRuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := normalizeRuntimePaths(cfg, home); err != nil {
		t.Fatal(err)
	}
	login := &Login{root: &Root{Cfg: cfg, Opts: RootOpts{Home: home}}, cmd: &cobra.Command{}}
	command := &loginCookieCmd{root: login, cmd: &cobra.Command{}, File: "incoming.json", format: "json"}
	err = command.execute(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "EOF") {
		t.Fatal("-f did not read the caller directory; both fixtures are invalid and must fail before network access")
	}
	if actual, _ := os.Getwd(); actual != cwd {
		t.Fatal("login changed the caller working directory")
	}
}

func TestMainLoginPreservesStoredHomeReferenceAndResolvedRuntimePath(t *testing.T) {
	home := t.TempDir()
	cfg, err := newDefaultRuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Accounts.Main = "${HOME}/cookie.json"
	cfg.Accounts.AntiCheatTokens = map[string]string{"${HOME}/cookie.json": "main-test"}
	configPath := filepath.Join(home, "config.yaml")
	data, _ := yaml.Marshal(cfg)
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := normalizeRuntimePaths(cfg, home); err != nil {
		t.Fatal(err)
	}
	root := &Root{Cfg: cfg, CfgPath: configPath, Opts: RootOpts{Home: home}}
	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&output)
	login := &Login{root: root, cmd: cmd, isMain: true, jsonResult: true}
	incoming := filepath.Join(home, "incoming.cookie")
	if err := os.WriteFile(incoming, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := login.saveLoginResult(context.Background(), "main", "", 123, incoming, "", ""); err != nil {
		t.Fatal(err)
	}
	result, err := loginresult.Parse(output.String())
	if err != nil || result.AccountPath != "${HOME}/cookie.json" || root.Cfg.Accounts.Main != filepath.Join(home, "cookie.json") || root.Cfg.Network.Cookie.Filepath != root.Cfg.Accounts.Main {
		t.Fatal("main login changed the stored path identity or runtime path")
	}
	var stored config.Config
	current, _ := os.ReadFile(configPath)
	if err := yaml.Unmarshal(current, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Accounts.Main != "${HOME}/cookie.json" || stored.Accounts.AntiCheatTokens["${HOME}/cookie.json"] != "main-test" {
		t.Fatal("main login rewrote the account reference or token key")
	}
}

func TestLoginKeepsInstancePathsAndExistingAccountReferences(t *testing.T) {
	for _, accountPath := range []string{"fan.json", "./fan.json", "${HOME}/fan.json", "absolute"} {
		t.Run(accountPath, func(t *testing.T) {
			base := t.TempDir()
			cwd := filepath.Join(base, "caller")
			if err := os.Mkdir(cwd, 0700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(cwd)
			home := filepath.Join(base, "instance")
			otherHome := filepath.Join(base, "other-instance")
			for _, dir := range []string{home, otherHome} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if accountPath == "absolute" {
				accountPath = filepath.Join(home, "fan.json")
			}
			cfg, err := newDefaultRuntimeConfig()
			if err != nil {
				t.Fatal(err)
			}
			cfg.Accounts.Main = "${HOME}/cookie.json"
			cfg.Accounts.Secondary = []string{accountPath, filepath.Join(otherHome, "fan.json")}
			cfg.Accounts.AntiCheatTokens = map[string]string{accountPath: "mobile-test"}
			cfg.FansGroup.CommentCheckTokens = map[string]string{accountPath: "web-test"}
			configPath := filepath.Join(home, "config.yaml")
			original, _ := yaml.Marshal(cfg)
			if err := os.WriteFile(configPath, original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(otherHome, "fan.json"), []byte("other-instance"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := normalizeRuntimePaths(cfg, home); err != nil {
				t.Fatal(err)
			}
			root := &Root{Cfg: cfg, CfgPath: configPath, Opts: RootOpts{Home: home}}
			var output bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&output)
			login := &Login{root: root, cmd: cmd, jsonResult: true}
			for range 2 {
				incoming := filepath.Join(home, "incoming.cookie")
				if err := os.WriteFile(incoming, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
				output.Reset()
				if err := login.saveLoginResult(context.Background(), "tester", "", 123, incoming, "fan.json", ""); err != nil {
					t.Fatal(err)
				}
				result, err := loginresult.Parse(output.String())
				if err != nil || result.AccountPath != accountPath || result.CookiePath != filepath.Join(home, "fan.json") {
					t.Fatal("login changed the existing account reference or saved into another home")
				}
				if !reflect.DeepEqual(root.Cfg.Accounts.Secondary, []string{filepath.Join(home, "fan.json"), filepath.Join(otherHome, "fan.json")}) || root.Cfg.Accounts.Main != filepath.Join(home, "cookie.json") || root.Cfg.Network.Cookie.Filepath != root.Cfg.Accounts.Main {
					t.Fatalf("login replaced resolved runtime paths or duplicated the account: main=%q, network=%q, secondary=%q; home=%q", root.Cfg.Accounts.Main, root.Cfg.Network.Cookie.Filepath, root.Cfg.Accounts.Secondary, home)
				}
			}
			var stored config.Config
			data, _ := os.ReadFile(configPath)
			if err := yaml.Unmarshal(data, &stored); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(stored.Accounts.Secondary, []string{accountPath, filepath.Join(otherHome, "fan.json")}) || stored.Accounts.Main != "${HOME}/cookie.json" || stored.Accounts.AntiCheatTokens[accountPath] != "mobile-test" || stored.FansGroup.CommentCheckTokens[accountPath] != "web-test" {
				t.Fatal("login rewrote unrelated account paths or token map keys")
			}
			other, _ := os.ReadFile(filepath.Join(otherHome, "fan.json"))
			if string(other) != "other-instance" {
				t.Fatal("login overwrote another instance cookie")
			}
		})
	}
}

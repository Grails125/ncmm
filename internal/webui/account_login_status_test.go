package webui

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/internal/loginresult"
	"gopkg.in/yaml.v3"
)

func TestVerifiedLoginPersistsCookieObservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, configForTest(), 0600); err != nil {
		t.Fatal(err)
	}
	store := newConfigStore(path)
	before, err := store.get()
	if err != nil {
		t.Fatal(err)
	}
	if before.AccountProfiles["./cookie.json"].CookieVerifiedAt != "" {
		t.Fatal("existing config without a verified login must remain unknown")
	}
	started := time.Now()
	updated, err := store.updateAccount(before.Revision, loginresult.Result{
		UID: 123, Nickname: "已验证账号", CookiePath: filepath.Join(filepath.Dir(path), "cookie.json"), AccountPath: "./cookie.json", Main: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := time.Parse(time.RFC3339Nano, updated.AccountProfiles["./cookie.json"].CookieVerifiedAt)
	if err != nil || observed.Before(started) || observed.After(time.Now()) {
		t.Fatalf("invalid successful login observation %q, error %v", updated.AccountProfiles["./cookie.json"].CookieVerifiedAt, err)
	}
	reloaded, err := newConfigStore(path).get()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.AccountProfiles["./cookie.json"].CookieVerifiedAt != updated.AccountProfiles["./cookie.json"].CookieVerifiedAt {
		t.Fatal("login observation lost after reload")
	}
	if _, err := store.updateAccount(before.Revision, loginresult.Result{UID: 456, CookiePath: "fan.json", AccountPath: "fan.json"}); !errors.Is(err, errConfigRevisionConflict) {
		t.Fatalf("stale update error = %v", err)
	}
	current, _ := store.get()
	if current.Revision != updated.Revision {
		t.Fatal("failed config commit changed the verified observation")
	}
	// 表单编辑会保留 YAML 注释，改成另一路径时不得继承之前的验证结果。
	data := current.Data.(map[string]any)
	data["accounts"].(map[string]any)["main"] = "other/cookie.json"
	changed, err := store.saveData(current.Revision, data)
	if err != nil {
		t.Fatal(err)
	}
	if changed.AccountProfiles["other/cookie.json"].CookieVerifiedAt != "" {
		t.Fatal("changing an account path transferred another Cookie's verified status")
	}
}

func TestUnverifiedAccountResultDoesNotUpdateCookieObservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, configForTest(), 0600); err != nil {
		t.Fatal(err)
	}
	store := newConfigStore(path)
	doc, _ := store.get()
	_, err := store.updateAccount(doc.Revision, loginresult.Result{CookiePath: "cookie.json", AccountPath: "./cookie.json", Main: true})
	if err == nil {
		t.Fatal("an incomplete login result must not be marked verified")
	}
	after, _ := store.get()
	if after.Revision != doc.Revision || strings.Contains(after.Raw, "Cookie 验证时间") {
		t.Fatal("incomplete login result wrote verification metadata")
	}
}

func TestAccountLoginObservationSurvivesAccountReordering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, configForTest(), 0600); err != nil {
		t.Fatal(err)
	}
	store := newConfigStore(path)
	doc, _ := store.get()
	for _, accountPath := range []string{"./a/cookie.json", "./b/cookie.json"} {
		var err error
		doc, err = store.updateAccount(doc.Revision, loginresult.Result{UID: 123, Nickname: accountPath, CookiePath: accountPath, AccountPath: accountPath})
		if err != nil {
			t.Fatal(err)
		}
	}
	data := doc.Data.(map[string]any)
	data["accounts"].(map[string]any)["secondary"] = []string{"./b/cookie.json", "./a/cookie.json", "./new/cookie.json"}
	updated, err := store.saveData(doc.Revision, data)
	if err != nil {
		t.Fatal(err)
	}
	for _, accountPath := range []string{"./a/cookie.json", "./b/cookie.json"} {
		if updated.AccountProfiles[accountPath] != doc.AccountProfiles[accountPath] {
			t.Errorf("reordering changed the observation for %s", accountPath)
		}
	}
	if updated.AccountProfiles["./new/cookie.json"].CookieVerifiedAt != "" {
		t.Fatal("same filename inherited another account's login observation")
	}
}

func TestAccountLoginLineCommentOverridesOldHeadComment(t *testing.T) {
	var document yaml.Node
	err := yaml.Unmarshal([]byte("accounts:\n  # Cookie 验证时间: 2026-10-22T09:00:00Z | Cookie 验证路径: other/cookie.json\n  main: ./cookie.json # Cookie 验证时间: 2026-10-22T10:00:00Z | Cookie 验证路径: ./cookie.json\n"), &document)
	if err != nil {
		t.Fatal(err)
	}
	profile := collectAccountProfiles(&document)["./cookie.json"]
	if profile.CookieVerifiedAt != "2026-10-22T10:00:00Z" {
		t.Fatalf("old heading comment overrode the latest login: %+v", profile)
	}
}

func TestAccountLoginWindowsPathCaseMatchesConfigUpdate(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows account paths are case-insensitive")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, configForTest(), 0600); err != nil {
		t.Fatal(err)
	}
	store := newConfigStore(path)
	doc, _ := store.get()
	first, err := store.updateAccount(doc.Revision, loginresult.Result{UID: 123, Nickname: "旧登录", CookiePath: "FAN.JSON", AccountPath: "./FAN.JSON"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.updateAccount(first.Revision, loginresult.Result{UID: 123, Nickname: "新登录", CookiePath: "fan.json", AccountPath: "./fan.json"})
	if err != nil {
		t.Fatal(err)
	}
	profile := updated.AccountProfiles["./FAN.JSON"]
	if profile.CookieVerifiedAt == "" || profile.Nickname != "新登录" || profile.CookieVerifiedAt == first.AccountProfiles["./FAN.JSON"].CookieVerifiedAt {
		t.Fatalf("case-insensitive login did not refresh the original path: %+v", profile)
	}
}

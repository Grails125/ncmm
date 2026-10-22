package cookie

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadOnlyCookieNeverPersistsChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookie.json")
	u, _ := url.Parse("https://music.163.com/")
	writable, err := NewCookie(WithFilePath(path), WithSyncInterval(0))
	if err != nil {
		t.Fatal(err)
	}
	writable.SetCookies(u, []*http.Cookie{{Name: "MUSIC_U", Value: "test-original-session", Path: "/"}})
	if err := writable.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, interval := range []time.Duration{0, time.Nanosecond} {
		readonly, err := NewCookie(WithFilePath(path), WithSyncInterval(interval), WithReadOnly(true))
		if err != nil {
			t.Fatal(err)
		}
		if readonly.async {
			t.Fatal("read-only cookie must not start background persistence")
		}
		readonly.SetCookies(u, []*http.Cookie{{Name: "MUSIC_U", Value: "test-updated-session", Path: "/"}})
		if got := readonly.Cookies(u); len(got) != 1 || got[0].Value != "test-updated-session" {
			t.Fatal("read-only session must accept in-memory updates")
		}
		if err := readonly.export(); err != nil {
			t.Fatal(err)
		}
		if err := readonly.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		current, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(current, original) {
			t.Fatal("read-only session changed the source cookie file")
		}
	}
}

func TestReadOnlyCookieMissingFileCreatesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "must-not-create")
	jar, err := NewCookie(WithFilePath(filepath.Join(dir, "cookie.json")), WithReadOnly(true))
	if err == nil || jar != nil {
		t.Fatal("missing read-only cookie file must fail")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("read-only cookie created its source directory")
	}
}

func TestReadOnlyCookieCannotOverwriteNewerSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookie.json")
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	jar, err := NewCookie(WithFilePath(path), WithReadOnly(true))
	if err != nil {
		t.Fatal(err)
	}
	newer := []byte("{\n}\n")
	if err := os.WriteFile(path, newer, 0600); err != nil {
		t.Fatal(err)
	}
	if err := jar.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, newer) {
		t.Fatal("read-only close overwrote another session's latest cookies")
	}
	encoded, err := json.Marshal(Config{ReadOnly: true})
	if err != nil || bytes.Contains(bytes.ToLower(encoded), []byte("readonly")) {
		t.Fatal("read-only process option leaked into persisted config")
	}
}

type readOnlyTestSuffixList struct{}

func (readOnlyTestSuffixList) PublicSuffix(string) string { return "com" }
func (readOnlyTestSuffixList) String() string             { return "test-suffix-list" }

func TestReadOnlyCookiePreservesPublicSuffixOption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookie.json")
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	jar, err := NewCookie(WithFilePath(path), WithReadOnly(true), WithPublicSuffixList(readOnlyTestSuffixList{}))
	if err != nil {
		t.Fatal(err)
	}
	defer jar.Close(context.Background())
	if jar.jar.psList == nil || jar.jar.psList.String() != "test-suffix-list" {
		t.Fatal("read-only session lost custom public suffix policy")
	}
}

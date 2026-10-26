package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3899/ncmm/pkg/cookie"
)

func TestUserAgentAutoUpdateClientIsOptInAndPreservesConfig(t *testing.T) {
	previous := automaticUserAgentVersions
	t.Cleanup(func() { automaticUserAgentVersions = previous })
	var calls atomic.Int32
	automaticUserAgentVersions = newUserAgentVersionCache(func(ctx context.Context, source userAgentVersionSource) (string, error) {
		calls.Add(1)
		return map[userAgentVersionSource]string{uaChrome: "157.0.8103.15", uaMac: "3.1.8.3330", uaIOS: "9.6.13"}[source], nil
	})
	cfg := &Config{UserAgent: UserAgentConfig{Default: DefaultWEAPIUserAgent, WEAPI: defaultBrowserUserAgent, EAPI: defaultIOSDisplayUserAgent}, Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}
	client, err := NewClient(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("disabled updater made a request")
	}
	_ = client.Close(t.Context())
	cfg.UserAgent.AutoUpdate = true
	client, err = NewClient(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	if !strings.Contains(client.UserAgent(CryptoModeWEAPI), "Chrome/157.0.0.0") || !strings.Contains(client.UserAgent(CryptoModeEAPI), "neteasemusic/9.6.13 ") || !strings.Contains(client.cfg.UserAgent.Default, "3.1.8.3330") {
		t.Fatal("built-in UA versions were not refreshed")
	}
	if cfg.UserAgent.WEAPI != defaultBrowserUserAgent || cfg.UserAgent.EAPI != defaultIOSDisplayUserAgent || cfg.UserAgent.Default != DefaultWEAPIUserAgent {
		t.Fatal("caller configuration was mutated")
	}
	if client.UserAgent(CryptoModeXEAPI) != "" {
		t.Fatal("Android UA was invented")
	}
}

func TestUserAgentAutoUpdatePreservesCustomBuildAndSession(t *testing.T) {
	cache := newUserAgentVersionCache(func(ctx context.Context, source userAgentVersionSource) (string, error) { return "99.99.99.9999", nil })
	for _, ua := range []UserAgentConfig{
		{AutoUpdate: true, Default: "custom-desktop", WEAPI: "custom-web", EAPI: DefaultEAPIUserAgent, XEAPI: "NeteaseMusic/9.6.13.8888 (Android 16)"},
		{AutoUpdate: true, Default: DefaultWEAPIUserAgent, EAPI: defaultIOSDisplayUserAgent},
	} {
		original := ua
		cache.refresh(t.Context(), &ua, true)
		if ua != original {
			t.Fatalf("session/custom UA changed: %#v", ua)
		}
	}
	ua := UserAgentConfig{AutoUpdate: true, EAPI: DefaultEAPIUserAgent}
	cache.refresh(t.Context(), &ua, false)
	if ua.EAPI != DefaultEAPIUserAgent {
		t.Fatal("iOS internal build was replaced by public version")
	}
}

func TestUserAgentVersionCacheConcurrentAndFailureRetention(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	cache := newUserAgentVersionCache(func(ctx context.Context, source userAgentVersionSource) (string, error) {
		calls.Add(1)
		if fail.Load() {
			return "", fmt.Errorf("unavailable")
		}
		time.Sleep(10 * time.Millisecond)
		return "9.6.13", nil
	})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v := cache.version(t.Context(), uaIOS); v != "9.6.13" {
				t.Errorf("got %q", v)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent refreshes: %d", calls.Load())
	}
	cache.mu.Lock()
	cache.entries[uaIOS].retryAfter = time.Time{}
	cache.mu.Unlock()
	fail.Store(true)
	if v := cache.version(t.Context(), uaIOS); v != "9.6.13" {
		t.Fatal("failed refresh discarded successful version")
	}
	if v := cache.version(t.Context(), uaIOS); v != "9.6.13" || calls.Load() != 2 {
		t.Fatal("failure backoff did not retain cache")
	}
}

func TestUserAgentAutoUpdateConcurrentClients(t *testing.T) {
	previous := automaticUserAgentVersions
	t.Cleanup(func() { automaticUserAgentVersions = previous })
	var calls atomic.Int32
	automaticUserAgentVersions = newUserAgentVersionCache(func(ctx context.Context, source userAgentVersionSource) (string, error) {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		return "157.0.8103.15", nil
	})
	var wg sync.WaitGroup
	for range 12 {
		cfg := &Config{UserAgent: UserAgentConfig{AutoUpdate: true, WEAPI: defaultBrowserUserAgent}, Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}
		wg.Add(1)
		go func() {
			defer wg.Done()
			client, err := NewClient(cfg, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer client.Close(context.Background())
			if !strings.Contains(client.UserAgent(CryptoModeWEAPI), "Chrome/157.0.0.0") {
				t.Error("client did not use refreshed UA")
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent clients made %d metadata calls", calls.Load())
	}
}

func TestUserAgentAutoUpdateSavedIdentityPreservesNativeUA(t *testing.T) {
	previous := automaticUserAgentVersions
	t.Cleanup(func() { automaticUserAgentVersions = previous })
	var nativeCalls atomic.Int32
	automaticUserAgentVersions = newUserAgentVersionCache(func(ctx context.Context, source userAgentVersionSource) (string, error) {
		if source != uaChrome {
			nativeCalls.Add(1)
		}
		return "157.0.8103.15", nil
	})
	for _, test := range []struct{ name, endpoint, path string }{
		{"os", "https://music.163.com/", "/"},
		{"osver", "https://music.163.com/", "/"},
		{"appver", "https://music.163.com/", "/"},
		{"buildver", "https://music.163.com/", "/"},
		{"appver", "https://interface3.music.163.com/eapi/resource/", "/eapi"},
		{"buildver", "https://interfacepc.music.163.com/eapi/login/qrcode/", "/eapi/login"},
	} {
		t.Run(test.name+test.path, func(t *testing.T) {
			cfg := &Config{UserAgent: UserAgentConfig{Default: DefaultWEAPIUserAgent, EAPI: defaultIOSDisplayUserAgent, WEAPI: defaultBrowserUserAgent}, Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}
			client, err := NewClient(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			base, _ := url.Parse(test.endpoint)
			client.SetCookies(base, []*http.Cookie{{Name: test.name, Value: "saved-identity", Path: test.path}})
			_ = client.Close(t.Context())
			cfg.UserAgent.AutoUpdate = true
			client, err = NewClient(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })
			if client.cfg.UserAgent.Default != DefaultWEAPIUserAgent || client.UserAgent(CryptoModeEAPI) != defaultIOSDisplayUserAgent {
				t.Fatal("saved session identity was changed")
			}
			if value, ok := client.Cookie(base.String(), test.name); !ok || value.Value != "saved-identity" {
				t.Fatal("saved cookie changed")
			}
		})
	}
	if nativeCalls.Load() != 0 {
		t.Fatal("session-bound native UA caused a metadata request")
	}
}

func TestUserAgentAutoUpdateFailureAndOlderVersionKeepOriginal(t *testing.T) {
	for _, result := range []string{"", "1.0.0", "invalid"} {
		cache := newUserAgentVersionCache(func(ctx context.Context, source userAgentVersionSource) (string, error) {
			if result == "" {
				return "", fmt.Errorf("offline")
			}
			return result, nil
		})
		ua := UserAgentConfig{AutoUpdate: true, Default: DefaultWEAPIUserAgent, WEAPI: defaultBrowserUserAgent, EAPI: defaultIOSDisplayUserAgent}
		original := ua
		cache.refresh(t.Context(), &ua, false)
		if ua != original {
			t.Fatalf("%q overwrote existing UA", result)
		}
	}
}

func TestUserAgentVersionTimeoutPreservesExistingUA(t *testing.T) {
	cache := newUserAgentVersionCache(func(ctx context.Context, source userAgentVersionSource) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	ua := UserAgentConfig{AutoUpdate: true, Default: DefaultWEAPIUserAgent, WEAPI: defaultBrowserUserAgent, EAPI: defaultIOSDisplayUserAgent}
	original := ua
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	cache.refresh(ctx, &ua, false)
	if ua != original || time.Since(started) > time.Second {
		t.Fatal("timeout changed UA or blocked excessively")
	}
}

func TestOfficialUserAgentVersionMetadata(t *testing.T) {
	for _, test := range []struct {
		name                 string
		source               userAgentVersionSource
		status               int
		body, location, want string
	}{
		{"Chrome", uaChrome, 200, `{"releases":[{"version":"157.0.8103.15","fraction":0.01}]}`, "", "157.0.8103.15"},
		{"iOS", uaIOS, 200, `{"resultCount":1,"results":[{"trackId":590338362,"bundleId":"com.netease.cloudmusic","version":"9.6.13"}]}`, "", "9.6.13"},
		{"macOS", uaMac, 302, "", "https://d1.music.126.net/dmusic/NeteaseCloudMusic_Music_official_3.1.8.3330.dmg", "3.1.8.3330"},
		{"wrong bundle", uaIOS, 200, `{"resultCount":1,"results":[{"trackId":590338362,"bundleId":"other","version":"9.6.13"}]}`, "", ""},
		{"missing build", uaMac, 302, "", "https://d1.music.126.net/dmusic/NeteaseCloudMusic_Music_official_3.1.8.dmg", ""},
		{"untrusted redirect", uaMac, 302, "", "https://evil.example/NeteaseCloudMusic_Music_official_3.1.8.3330.dmg", ""},
		{"bad version", uaChrome, 200, `{"releases":[{"version":"157.0.1.2\r\nX-Foo: bar","fraction":1}]}`, "", ""},
		{"failure", uaIOS, 503, `{}`, "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
					t.Error("metadata request leaked session")
				}
				if test.source == uaMac && r.Method != http.MethodHead {
					t.Error("download metadata must use HEAD")
				}
				if test.location != "" {
					w.Header().Set("Location", test.location)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			got, err := fetchOfficialUserAgentVersion(t.Context(), newUserAgentMetadataClient(), test.source, server.URL)
			if got != test.want || ((err != nil) != (test.want == "")) {
				t.Fatalf("got %q err=%v", got, err)
			}
		})
	}
}

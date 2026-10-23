package upgradepolicy

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCompareNativeAcceptsGrailsWithoutRevision(t *testing.T) {
	for _, tc := range []struct {
		left, right string
		want        int
	}{
		{"1.2.3-grails", "v1.2.3-grails", 0},
		{"v1.2.3-grails", "1.2.2-grails.99", 1},
		{"1.2.2-grails.2", "v1.2.3-grails", -1},
		{"1.2.3-grails", "1.2.3-grails.0", 0},
		{"1.2.3-grails", "1.2.3-grails.1", -1},
		{"1.2.3-grails.2", "1.2.3-grails.1", 1},
	} {
		if got, ok := CompareNative(tc.left, tc.right); !ok || got != tc.want {
			t.Errorf("CompareNative(%q, %q) = %d, %t，预期 %d, true", tc.left, tc.right, got, ok, tc.want)
		}
	}
	for _, invalid := range []string{"1.2.9", "1.2.3", "1.2.3-grals", "1.2.3-grals.1", "1.2.3-grails.foo", "v1.2.3-grails/other", "v1.2.3-grails?redirect=other", "1.2.3-grails\n", "1.2.3-grails+other", "1.2.3-grails.18446744073709551616"} {
		if _, ok := CompareNative(invalid, "v1.2.3-grails"); ok {
			t.Errorf("非原生或非法版本被接受：%q", invalid)
		}
	}
}

func TestPolicyCanRequireGrailsWithoutRevisionAfterLegacyRelease(t *testing.T) {
	published := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	now := published.AddDate(0, 1, 0)
	cache := Cache{LastCheckTime: now}
	cache.Observe("v1.2.3-grails", Marker, published)
	if status := Evaluate(cache, "1.2.2-grails.2", now); !status.Required || status.RequiredVersion != "v1.2.3-grails" {
		t.Fatalf("旧原生版本未识别新后缀升级要求：%+v", status)
	}
	if status := Evaluate(cache, "1.2.3-grails", now); status.Required {
		t.Fatalf("已升级的新后缀版本仍被拦截：%+v", status)
	}
}

func TestPolicyRequiresExplicitNativeReleaseAfterGrace(t *testing.T) {
	published := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	now := published.AddDate(0, 1, 0)
	cache := Cache{LastCheckTime: now, RequiredVersion: "v1.2.2-grails.2", RequiredPublishedAt: published}
	for _, tc := range []struct {
		name, current string
		at            time.Time
		want          bool
	}{
		{"grace", "1.2.2-grails.1", now.Add(-time.Second), false},
		{"deadline", "1.2.2-grails.1", now, true},
		{"updated", "1.2.2-grails.2", now, false},
		{"newer", "1.2.2-grails.3", now, false},
		{"unknown binary", "unknown", now, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Evaluate(cache, tc.current, tc.at); got.Required != tc.want {
				t.Fatalf("policy = %+v", got)
			}
		})
	}
	cache.RequiredVersion = "v1.2.9"
	if Evaluate(cache, "1.2.2-grails.1", now).Required {
		t.Fatal("foreign release can require upgrade")
	}
}

func TestMissingOfflineCorruptAndFutureCacheFailOpen(t *testing.T) {
	now := time.Now()
	cache := Cache{LastCheckTime: now, RequiredVersion: "v1.2.2-grails.2", RequiredPublishedAt: now.AddDate(0, -2, 0)}
	for _, variant := range []string{"offline", "stale", "future check", "future release", "missing time"} {
		t.Run(variant, func(t *testing.T) {
			c := cache
			switch variant {
			case "offline":
				c.LastCheckFailed = true
			case "stale":
				c.LastCheckTime = now.Add(-25 * time.Hour)
			case "future check":
				c.LastCheckTime = now.Add(time.Hour)
			case "future release":
				c.RequiredPublishedAt = now.Add(time.Hour)
			case "missing time":
				c.RequiredPublishedAt = time.Time{}
			}
			if Evaluate(c, "1.2.2-grails.1", now).Required {
				t.Fatal("unverified policy blocked task")
			}
		})
	}
	home := t.TempDir()
	if Read(home, "1.2.2-grails.1", now).Required {
		t.Fatal("missing cache blocked")
	}
	if err := os.WriteFile(filepath.Join(home, "ncmm-update.json"), []byte(`{"required_version":`), 0600); err != nil {
		t.Fatal(err)
	}
	if Read(home, "1.2.2-grails.1", now).Required {
		t.Fatal("corrupt cache blocked")
	}
}

func TestReleaseMarkerAndRequiredFloorPersist(t *testing.T) {
	published := time.Now().AddDate(0, -2, 0)
	var cache Cache
	cache.Observe("v1.2.2-grails.2", "ordinary release", published)
	if cache.RequiredVersion != "" {
		t.Fatal("ordinary release became mandatory")
	}
	cache.Observe("v1.2.9", "<!-- ncmm-force-upgrade -->", published)
	if cache.RequiredVersion != "" {
		t.Fatal("foreign release became mandatory")
	}
	cache.Observe("v1.2.2-grails.2", "reason\n<!-- ncmm-force-upgrade -->", published)
	cache.Observe("v1.2.2-grails.3", "ordinary later release", published.Add(time.Hour))
	if cache.RequiredVersion != "v1.2.2-grails.2" {
		t.Fatal("later release lost required floor")
	}
	cache.Observe("v1.2.2-grails.2", "marker removed", published)
	if cache.RequiredVersion != "" {
		t.Fatal("publisher cannot retract marker")
	}
}

func TestRecoveryCommandsStayAvailable(t *testing.T) {
	for _, path := range []string{"ncmm update", "ncmm web", "ncmm web stop", "ncmm auth reset-password", "ncmm login qrcode", "ncmm help", "ncmm version", "ncmm plugin list", "ncmm plugin install", "ncmm"} {
		if !AllowsCommand(path) {
			t.Fatalf("recovery command blocked: %s", path)
		}
	}
	for _, path := range []string{"ncmm task", "ncmm music-partner", "ncmm plugin run report", "ncmm musician vip"} {
		if AllowsCommand(path) {
			t.Fatalf("business command allowed: %s", path)
		}
	}
}

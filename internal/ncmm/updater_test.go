// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

package ncmm

import (
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		v1       string
		v2       string
		expected int // 1: v1 > v2, -1: v1 < v2, 0: v1 == v2
	}{
		{"v1.1.7", "v1.1.6", 1},
		{"1.1.7", "v1.1.7", 0},
		{"v1.1.6", "v1.1.7", -1},
		{"v1.1.10", "v1.1.9", 1},
		{"1.2.0", "v1.1.7", 1},
		{"1.0.0", "2.0.0", -1},
		{"v1.1.7-beta", "v1.1.7", 0}, // β is stripped/ignored, comparing digits
		{"v1", "v2", -1},
		{"v1.1", "v1.1.0", 0},
		{"v1.1.0", "v1.1", 0},
		{"v1.2.2-grails.2", "v1.2.2-grails.1", 1},
	}

	for _, tc := range tests {
		res := CompareVersions(tc.v1, tc.v2)
		if res != tc.expected {
			t.Errorf("CompareVersions(%q, %q) = %d, expected %d", tc.v1, tc.v2, res, tc.expected)
		}
	}
}

func TestNativeUpdateSourceBoundary(t *testing.T) {
	root := &Root{AppVersion: "1.2.2-grails.1"}
	state := root.initDefaultUpdateState()
	if !isNativeUpdate(state.LatestVersion, state.AssetFilename, state.DownloadURL) {
		t.Fatal("default update state rejected its own release URL")
	}
	if !strings.HasPrefix(state.DownloadURL, "https://github.com/Grails125/ncmm/releases/") {
		t.Fatal("wrong release owner")
	}
	for _, tc := range []struct{ tag, asset, url string }{
		{"v1.2.9", "ncmm.zip", "https://github.com/3899/ncmm/releases/download/v1.2.9/ncmm.zip"},
		{"v1.2.2-grails.2", "ncmm.zip", "https://github.com/3899/ncmm/releases/download/v1.2.2-grails.2/ncmm.zip"},
		{"v1.2.2-grails.2", "ncmm.zip", "https://github.com/Grails125/ncmm/releases/download/v1.2.2-grails.2/ncmm.zip?redirect=evil"},
	} {
		if _, err := root.downloadUpdate(tc.tag, tc.asset, tc.url); err == nil {
			t.Fatal("foreign cached update accepted")
		}
	}
	if !isNativeUpdate("v1.2.2-grails.2", "ncmm.zip", "https://github.com/Grails125/ncmm/releases/download/v1.2.2-grails.2/ncmm.zip") {
		t.Fatal("native source rejected")
	}
}

func TestApplyAvailableUpdateRejectsOfficialDocker(t *testing.T) {
	t.Setenv("NCMM_DOCKER_OFFICIAL", "1")
	root := &Root{AppVersion: "1.1.14", Opts: RootOpts{Home: t.TempDir()}}
	_, err := root.ApplyAvailableUpdate()
	if err == nil || !strings.Contains(err.Error(), "Docker") {
		t.Fatalf("ApplyAvailableUpdate() error = %v; want Docker rejection", err)
	}
}

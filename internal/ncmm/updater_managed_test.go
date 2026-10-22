package ncmm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/internal/managedruntime"
	"github.com/3899/ncmm/internal/selfupdate"
	"github.com/3899/ncmm/internal/upgradepolicy"
)

func TestUpdateCheckAllFailuresReleaseForcedGate(t *testing.T) {
	for _, reply := range []string{"network", "{", `{}`, `{"tag_name":"v1.2.9"}`} {
		t.Run(reply, func(t *testing.T) {
			root, path := updaterFixture(t)
			cached := root.initDefaultUpdateState()
			cached.LastCheckTime = time.Now()
			cached.RequiredVersion = "v1.2.2-grails.2"
			cached.RequiredPublishedAt = time.Now().AddDate(0, -2, 0)
			if err := root.saveUpdateState(root.getUpdateStatePath(), &cached); err != nil {
				t.Fatal(err)
			}
			if !upgradepolicy.Read(root.getHomePath(), root.AppVersion, time.Now()).Required {
				t.Fatal("fixture not gated")
			}
			old := http.DefaultClient
			t.Cleanup(func() { http.DefaultClient = old })
			http.DefaultClient = &http.Client{Transport: updaterTransport(func(r *http.Request) (*http.Response, error) {
				if reply == "network" {
					return nil, fmt.Errorf("offline")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(reply)), Header: make(http.Header)}, nil
			})}
			state, err := root.checkNewVersionSync(root.getUpdateStatePath(), root.AppVersion, path)
			if err == nil || !state.LastCheckFailed {
				t.Fatalf("failed check = %+v, %v", state, err)
			}
			if upgradepolicy.Read(root.getHomePath(), root.AppVersion, time.Now()).Required {
				t.Fatal("failed check retained forced gate")
			}
		})
	}
}

func TestNativeReleaseExplicitPolicyAndGrace(t *testing.T) {
	root, path := updaterFixture(t)
	published := time.Now().AddDate(0, -2, 0).UTC().Truncate(time.Second)
	old := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = old })
	payload, _ := json.Marshal(githubRelease{TagName: "v1.2.2-grails.2", Body: upgradepolicy.Marker, PublishedAt: published})
	http.DefaultClient = &http.Client{Transport: updaterTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(payload)), Header: make(http.Header)}, nil
	})}
	state, err := root.checkNewVersionSync(root.getUpdateStatePath(), root.AppVersion, path)
	if err != nil || state.RequiredVersion != "v1.2.2-grails.2" || !upgradepolicy.Read(root.getHomePath(), root.AppVersion, time.Now()).Required {
		t.Fatalf("native policy=%+v,%v", state, err)
	}
	if !upgradepolicy.AllowsCommand("ncmm update") || !upgradepolicy.AllowsCommand("ncmm web") || upgradepolicy.AllowsCommand("ncmm task") {
		t.Fatal("self rescue commands unavailable")
	}
}

func TestManagedUpdateStagesAndRetainsRollbackReceipt(t *testing.T) {
	root, image := updaterFixture(t)
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	if err := managedruntime.Initialize(runtimeRoot, image, root.AppVersion); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NCMM_RUNTIME_MANAGED", "1")
	t.Setenv("NCMM_RUNTIME_DIR", runtimeRoot)
	executable, err := managedruntime.CurrentExecutable(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	fakeUpdateRelease(t, "v1.2.2-grails.2", false)
	state, err := root.applyAvailableUpdateAt(executable)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := managedruntime.Read(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Pending.Path == "" || rs.Current.Version != root.AppVersion || !selfupdate.Matches(executable, selfupdate.Digest([]byte("old executable"))) {
		t.Fatal("running image replaced before validation")
	}
	if loaded := root.loadUpdateState(root.getUpdateStatePath(), executable); loaded.UpdateStatus != "success" {
		t.Fatalf("pending receipt lost: %+v", loaded)
	}
	if err := managedruntime.Reject(runtimeRoot, rs.Pending, "test health timeout"); err != nil {
		t.Fatal(err)
	}
	loaded := root.loadUpdateState(root.getUpdateStatePath(), executable)
	if loaded.UpdateStatus != "failed" || loaded.UpdatedVersion != "" || !strings.Contains(loaded.LastError, "test health timeout") {
		t.Fatalf("rejection hidden: %+v", loaded)
	}
	if err := managedruntime.Confirm(runtimeRoot, rs.Current); err != nil {
		t.Fatal(err)
	}
	after, _ := managedruntime.Read(runtimeRoot)
	if after.LastError == "" {
		t.Fatal("fallback confirmation discarded diagnostic")
	}
	if _, err := root.applyAvailableUpdateAt(executable); err == nil {
		t.Fatal("known bad update retried")
	}
	if !strings.Contains(state.DownloadURL, "Grails125/ncmm/") {
		t.Fatal("wrong origin")
	}
}

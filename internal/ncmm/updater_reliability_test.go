package ncmm

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3899/ncmm/internal/filelock"
	"github.com/3899/ncmm/internal/selfupdate"
)

type updaterTransport func(*http.Request) (*http.Response, error)

func (f updaterTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fakeUpdateRelease(t *testing.T, tag string, networkError bool) *atomic.Int32 {
	t.Helper()
	old := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = old })
	var downloads atomic.Int32
	_, _, ext := getPlatformInfo()
	var archive bytes.Buffer
	if ext == ".zip" {
		writer := zip.NewWriter(&archive)
		file, err := writer.Create("ncmm.exe")
		if err != nil {
			t.Fatal(err)
		}
		file.Write([]byte("new executable"))
		writer.Close()
	} else {
		gzipWriter := gzip.NewWriter(&archive)
		writer := tar.NewWriter(gzipWriter)
		if err := writer.WriteHeader(&tar.Header{Name: "ncmm", Size: 14, Mode: 0755}); err != nil {
			t.Fatal(err)
		}
		writer.Write([]byte("new executable"))
		writer.Close()
		gzipWriter.Close()
	}
	http.DefaultClient = &http.Client{Transport: updaterTransport(func(r *http.Request) (*http.Response, error) {
		if networkError {
			return nil, fmt.Errorf("simulated network failure")
		}
		data := []byte(fmt.Sprintf(`{"tag_name":%q,"body":"release notes"}`, tag))
		if !strings.Contains(r.URL.Path, "/releases/latest") {
			downloads.Add(1)
			data = archive.Bytes()
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})}
	return &downloads
}

func updaterFixture(t *testing.T) (*Root, string) {
	t.Helper()
	t.Setenv("NCMM_DOCKER_OFFICIAL", "")
	home := t.TempDir()
	path := filepath.Join(home, "ncmm")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	if err := os.WriteFile(path, []byte("old executable"), 0755); err != nil {
		t.Fatal(err)
	}
	return &Root{AppVersion: "1.2.2-grails.1", Opts: RootOpts{Home: home}}, path
}

func TestApplyChecksFreshReleaseWithoutCachedState(t *testing.T) {
	root, path := updaterFixture(t)
	downloads := fakeUpdateRelease(t, "v1.2.2-grails.2", false)
	state, err := root.applyAvailableUpdateAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.UpdateStatus != "success" || state.UpdatedVersion != "v1.2.2-grails.2" || downloads.Load() != 1 {
		t.Fatalf("unexpected state: %+v; downloads %d", state, downloads.Load())
	}
	if !selfupdate.Matches(path, state.InstalledSHA256) {
		t.Fatal("receipt does not match installed executable")
	}
}

func TestApplyCurrentIsSuccessfulNoOp(t *testing.T) {
	root, path := updaterFixture(t)
	downloads := fakeUpdateRelease(t, "v1.2.2-grails.1", false)
	state, err := root.applyAvailableUpdateAt(path)
	if err != nil || state.UpdateStatus != "idle" || downloads.Load() != 0 {
		t.Fatalf("current release: %+v, %v", state, err)
	}
	if !selfupdate.Matches(path, selfupdate.Digest([]byte("old executable"))) {
		t.Fatal("current binary changed")
	}
}

func TestApplyFreshCheckFailureNeverUsesStaleRelease(t *testing.T) {
	root, path := updaterFixture(t)
	fakeUpdateRelease(t, "v1.2.2-grails.2", true)
	state := root.initDefaultUpdateState()
	state.LatestVersion = "v1.2.2-grails.99"
	state.DownloadURL = strings.Replace(state.DownloadURL, "v1.2.2-grails.1", state.LatestVersion, 1)
	if err := root.saveUpdateState(root.getUpdateStatePath(), &state); err != nil {
		t.Fatal(err)
	}
	if _, err := root.applyAvailableUpdateAt(path); err == nil {
		t.Fatal("expected network failure")
	}
	if !selfupdate.Matches(path, selfupdate.Digest([]byte("old executable"))) {
		t.Fatal("stale release was installed")
	}
}

func TestRepeatedApplyUsesExecutableReceiptAndDetectsDowngrade(t *testing.T) {
	root, path := updaterFixture(t)
	downloads := fakeUpdateRelease(t, "v1.2.2-grails.2", false)
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := root.applyAvailableUpdateAt(path); errors <- err }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if downloads.Load() != 1 {
		t.Fatalf("duplicate downloads: %d", downloads.Load())
	}
	if err := os.WriteFile(path, []byte("downgraded executable"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := root.applyAvailableUpdateAt(path); err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != 2 {
		t.Fatal("stale success cache suppressed reinstall after downgrade")
	}
}

func TestUpdateLockPreventsInstallationAndCorruptStateRecovers(t *testing.T) {
	root, path := updaterFixture(t)
	downloads := fakeUpdateRelease(t, "v1.2.2-grails.2", false)
	lock, err := filelock.TryAcquire(path + ".update.lock")
	if err != nil {
		t.Fatal(err)
	}
	_, applyErr := root.applyAvailableUpdateAt(path)
	lock.Close()
	if applyErr == nil || downloads.Load() != 0 {
		t.Fatal("locked update proceeded")
	}
	if err := os.WriteFile(root.getUpdateStatePath(), []byte(`{"broken":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := root.applyAvailableUpdateAt(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(root.getUpdateStatePath())
	if err != nil || !json.Valid(data) {
		t.Fatalf("state not recovered: %s, %v", data, err)
	}
}

func TestInterruptedInstallationReceiptReconcilesWithDisk(t *testing.T) {
	root, path := updaterFixture(t)
	state := root.initDefaultUpdateState()
	state.UpdateStatus = "updating"
	state.InstalledSHA256 = selfupdate.Digest([]byte("old executable"))
	state.LatestVersion = "v1.2.2-grails.2"
	state.DownloadURL = strings.Replace(state.DownloadURL, "v1.2.2-grails.1", state.LatestVersion, 1)
	if err := root.saveUpdateState(root.getUpdateStatePath(), &state); err != nil {
		t.Fatal(err)
	}
	recovered := root.loadUpdateState(root.getUpdateStatePath(), path)
	if recovered.UpdateStatus != "success" || recovered.UpdatedVersion != state.LatestVersion {
		t.Fatalf("completed replacement not recovered: %+v", recovered)
	}
	if err := os.WriteFile(path, []byte("different executable"), 0755); err != nil {
		t.Fatal(err)
	}
	recovered = root.loadUpdateState(root.getUpdateStatePath(), path)
	if recovered.UpdateStatus != "failed" || recovered.UpdatedVersion != "" {
		t.Fatalf("interrupted replacement not detected: %+v", recovered)
	}
}

func TestTwoHomesSharingExecutablePreserveOriginalBackup(t *testing.T) {
	root, path := updaterFixture(t)
	fakeUpdateRelease(t, "v1.2.2-grails.2", false)
	if _, err := root.applyAvailableUpdateAt(path); err != nil {
		t.Fatal(err)
	}
	second := &Root{AppVersion: root.AppVersion, Opts: RootOpts{Home: t.TempDir()}}
	state, err := second.applyAvailableUpdateAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.UpdateStatus != "success" || !selfupdate.Matches(path, state.InstalledSHA256) {
		t.Fatalf("second home lacks receipt: %+v", state)
	}
	if !selfupdate.Matches(path+".old", selfupdate.Digest([]byte("old executable"))) {
		t.Fatal("second home overwrote the original backup")
	}
}

func TestUpdateLockHelperProcess(t *testing.T) {
	if os.Getenv("NCMM_UPDATE_TEST_LOCK") == "" {
		return
	}
	lock, err := filelock.TryAcquire(os.Getenv("NCMM_UPDATE_TEST_LOCK"))
	if err != nil {
		os.Exit(2)
	}
	defer lock.Close()
	if err := os.WriteFile(os.Getenv("NCMM_UPDATE_TEST_READY"), []byte("ready"), 0600); err != nil {
		os.Exit(3)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestOtherProcessBlocksUpdateAndExitReleasesLock(t *testing.T) {
	root, path := updaterFixture(t)
	downloads := fakeUpdateRelease(t, "v1.2.2-grails.2", false)
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestUpdateLockHelperProcess$")
	cmd.Env = append(os.Environ(), "NCMM_UPDATE_TEST_LOCK="+path+".update.lock", "NCMM_UPDATE_TEST_READY="+ready)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lock helper did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := root.applyAvailableUpdateAt(path); err == nil {
		t.Fatal("another process did not block installation")
	}
	if downloads.Load() != 0 {
		t.Fatal("download started without executable lock")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	if _, err := root.applyAvailableUpdateAt(path); err != nil {
		t.Fatal(err)
	}
}

func TestStateWriteFailurePreventsExecutableReplacement(t *testing.T) {
	root, path := updaterFixture(t)
	downloads := fakeUpdateRelease(t, "v1.2.2-grails.2", false)
	if err := os.Mkdir(root.getUpdateStatePath(), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := root.applyAvailableUpdateAt(path); err == nil {
		t.Fatal("unwritable state was ignored")
	}
	if downloads.Load() != 0 || !selfupdate.Matches(path, selfupdate.Digest([]byte("old executable"))) {
		t.Fatal("replacement started before durable state")
	}
}

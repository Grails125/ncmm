package managedruntime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/3899/ncmm/internal/selfupdate"
)

func fixture(t *testing.T) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "runtime")
	image := filepath.Join(t.TempDir(), "image")
	if err := os.WriteFile(image, []byte("image one"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(root, image, "1.2.2-grails.1"); err != nil {
		t.Fatal(err)
	}
	return root, image
}

func TestStageKeepsCurrentUntilHealthConfirmed(t *testing.T) {
	root, _ := fixture(t)
	before, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := Stage(root, "v1.2.2-grails.2", []byte("image two"))
	if err != nil {
		t.Fatal(err)
	}
	after, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if after.Current.Path != before.Current.Path || after.Pending.Path != pending.Path {
		t.Fatalf("unverified version became current: %+v", after)
	}
	if err := Confirm(root, pending); err != nil {
		t.Fatal(err)
	}
	after, err = Read(root)
	if err != nil || after.Current.Path != pending.Path || after.Previous.Path != before.Current.Path || after.Pending.Path != "" {
		t.Fatalf("confirmation = %+v, %v", after, err)
	}
}

func TestBrokenCandidateRollsBackAndIsNotRetried(t *testing.T) {
	root, image := fixture(t)
	before, _ := Read(root)
	pending, err := Stage(root, "v1.2.2-grails.2", []byte("bad binary"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Reject(root, pending, "health timeout"); err != nil {
		t.Fatal(err)
	}
	after, _ := Read(root)
	if after.Current.Path != before.Current.Path || after.Pending.Path != "" || after.LastError == "" {
		t.Fatalf("rollback = %+v", after)
	}
	if _, err := Stage(root, "v1.2.2-grails.2", []byte("bad binary")); err == nil {
		t.Fatal("known bad binary staged again")
	}
	if err := Initialize(root, image, "1.2.2-grails.1"); err != nil {
		t.Fatal(err)
	}
	after, _ = Read(root)
	if after.Current.Path != before.Current.Path {
		t.Fatal("restart changed healthy current")
	}
}

func TestImageUpgradeAndSameVersionRebuildPreserveNewerRuntime(t *testing.T) {
	root, image := fixture(t)
	v2, err := Stage(root, "1.2.2-grails.2", []byte("remote two"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Confirm(root, v2); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(root, image, "1.2.2-grails.1"); err != nil {
		t.Fatal(err)
	}
	state, _ := Read(root)
	if state.Current.Path != v2.Path || state.Pending.Path != "" {
		t.Fatal("old image replaced newer runtime")
	}
	if err := os.WriteFile(image, []byte("image three"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(root, image, "1.2.2-grails.3"); err != nil {
		t.Fatal(err)
	}
	state, _ = Read(root)
	if state.Pending.Version != "1.2.2-grails.3" {
		t.Fatalf("new image not staged: %+v", state)
	}
	if err := Confirm(root, state.Pending); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(image, []byte("rebuilt three"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(root, image, "1.2.2-grails.3"); err != nil {
		t.Fatal(err)
	}
	state, _ = Read(root)
	if state.Pending.SHA256 != selfupdate.Digest([]byte("rebuilt three")) {
		t.Fatal("same-version rebuilt image ignored")
	}
}

func TestRuntimePathsAndCorruptState(t *testing.T) {
	root, image := fixture(t)
	for _, version := range []string{"../escape", "v1.2.9", "1.2.2-grails.3/escape"} {
		if _, err := Stage(root, version, []byte("payload")); err == nil {
			t.Fatalf("unsafe version accepted: %s", version)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "state.json"), []byte(`{"current":`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(root, image, "1.2.2-grails.1"); err != nil {
		t.Fatal(err)
	}
	state, err := Read(root)
	if err != nil || state.Current.Path == "" {
		t.Fatalf("image recovery = %+v, %v", state, err)
	}
	bad := state.Current
	bad.Path = "../outside"
	if err := Confirm(root, bad); err == nil {
		t.Fatal("escaped slot confirmed")
	}
}

func TestPendingReceiptAndConcurrentNewerCandidate(t *testing.T) {
	root, _ := fixture(t)
	t.Setenv("NCMM_RUNTIME_MANAGED", "1")
	t.Setenv("NCMM_RUNTIME_DIR", root)
	state, _ := Read(root)
	executable := filepath.Join(root, filepath.FromSlash(state.Current.Path))
	v2, err := Stage(root, "1.2.2-grails.2", []byte("two"))
	if err != nil {
		t.Fatal(err)
	}
	if !InstalledMatches(executable, "v1.2.2-grails.2", v2.SHA256) {
		t.Fatal("staged receipt not recognized")
	}
	v3, err := Stage(root, "1.2.2-grails.3", []byte("three"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Confirm(root, v2); err != nil {
		t.Fatal(err)
	}
	state, _ = Read(root)
	if state.Pending.Path != v3.Path || state.Current.Path != v2.Path {
		t.Fatalf("confirmation lost concurrently staged candidate: %+v", state)
	}
}

func TestOnlyCurrentAndPreviousProgramsAreRetained(t *testing.T) {
	root, _ := fixture(t)
	for _, version := range []string{"1.2.2-grails.2", "1.2.2-grails.3", "1.2.2-grails.4"} {
		slot, err := Stage(root, version, []byte(version))
		if err != nil {
			t.Fatal(err)
		}
		if err := Confirm(root, slot); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "versions"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("retained directories: %v, %v", entries, err)
	}
}

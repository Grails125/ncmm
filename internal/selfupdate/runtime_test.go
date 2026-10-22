package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeManagedRequiresMarkerAndContainedVersion(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "versions", "v1.2.2-grails.2", "ncmm")
	if err := os.MkdirAll(filepath.Dir(executable), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NCMM_RUNTIME_DIR", root)
	t.Setenv("NCMM_RUNTIME_MANAGED", "1")
	if RuntimeManaged(executable) {
		t.Fatal("missing marker accepted")
	}
	marker := filepath.Join(root, "manager.json")
	if err := os.WriteFile(marker, []byte(`{"format":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if !RuntimeManaged(executable) {
		t.Fatal("managed runtime rejected")
	}
	for _, path := range []string{filepath.Join(root, "ncmm"), filepath.Join(root, "versions", "ncmm"), filepath.Join(root, "versions", "v1", "sub", "ncmm"), filepath.Join(t.TempDir(), "ncmm")} {
		if RuntimeManaged(path) {
			t.Fatalf("outside version path accepted: %s", path)
		}
	}
	t.Setenv("NCMM_RUNTIME_MANAGED", "0")
	if RuntimeManaged(executable) {
		t.Fatal("unmanaged environment accepted")
	}
	t.Setenv("NCMM_RUNTIME_MANAGED", "1")
	if err := os.WriteFile(marker, []byte(`{"format":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	if RuntimeManaged(executable) {
		t.Fatal("unknown manager format accepted")
	}
}

func TestRuntimeManagedRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "ncmm")
	if err := os.WriteFile(outside, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	versionDir := filepath.Join(root, "versions", "v1")
	if err := os.MkdirAll(versionDir, 0700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(versionDir, "ncmm")
	if err := os.Symlink(outside, executable); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "manager.json"), []byte(`{"format":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NCMM_RUNTIME_DIR", root)
	t.Setenv("NCMM_RUNTIME_MANAGED", "1")
	if RuntimeManaged(executable) {
		t.Fatal("escaped executable accepted")
	}
}

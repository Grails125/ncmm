package webui

import (
	"github.com/3899/ncmm/internal/managedruntime"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedDockerUpdateViewPendingAndRollback(t *testing.T) {
	t.Setenv("NCMM_DOCKER_OFFICIAL", "1")
	t.Setenv("NCMM_RUNTIME_MANAGED", "1")
	root := filepath.Join(t.TempDir(), "runtime")
	t.Setenv("NCMM_RUNTIME_DIR", root)
	image := filepath.Join(t.TempDir(), "image")
	if err := os.WriteFile(image, []byte("current executable"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := managedruntime.Initialize(root, image, "1.2.2-grails.1"); err != nil {
		t.Fatal(err)
	}
	executable, err := managedruntime.CurrentExecutable(root)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := managedruntime.Stage(root, "1.2.2-grails.2", []byte("next executable"))
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{opts: Options{Executable: executable, Version: "1.2.2-grails.1"}}
	receipt := updateState{LatestVersion: "v1.2.2-grails.2", UpdatedVersion: "v1.2.2-grails.2", UpdateStatus: "success", InstalledSHA256: pending.SHA256}
	view := server.makeUpdateView(receipt)
	if !view.RestartRequired || view.CanApply {
		t.Fatalf("pending view = %+v", view)
	}
	if err := managedruntime.Reject(root, pending, "test version mismatch"); err != nil {
		t.Fatal(err)
	}
	view = server.makeUpdateView(receipt)
	if view.RestartRequired || view.UpdateStatus != "failed" || !strings.Contains(view.LastError, "test version mismatch") {
		t.Fatalf("rollback view = %+v", view)
	}
}

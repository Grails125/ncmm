package webui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/3899/ncmm/internal/selfupdate"
)

func TestUpdateViewAllowsReinstallAfterBinaryDowngrade(t *testing.T) {
	t.Setenv("NCMM_DOCKER_OFFICIAL", "")
	path := filepath.Join(t.TempDir(), "ncmm")
	if err := os.WriteFile(path, []byte("downgraded executable"), 0755); err != nil {
		t.Fatal(err)
	}
	server := &Server{opts: Options{Version: "1.2.2-grails.1", Executable: path}}
	view := server.makeUpdateView(updateState{LatestVersion: "v1.2.2-grails.2", UpdatedVersion: "v1.2.2-grails.2", UpdateStatus: "success", InstalledSHA256: selfupdate.Digest([]byte("new executable"))})
	if !view.CanApply || view.RestartRequired || view.UpdatedVersion != "" || view.UpdateStatus != "idle" {
		t.Fatalf("stale success blocks reinstall: %+v", view)
	}
}

func TestUpdateViewApplyNoOpDoesNotRequestRestart(t *testing.T) {
	t.Setenv("NCMM_DOCKER_OFFICIAL", "")
	server := &Server{opts: Options{Version: "1.2.2-grails.1"}}
	view := server.makeUpdateView(updateState{LatestVersion: "v1.2.2-grails.1"})
	if view.RestartRequired || view.Available || view.CanApply {
		t.Fatalf("no-op update requires restart: %+v", view)
	}
}

func TestDockerUpdateViewRequiresManagedRuntime(t *testing.T) {
	t.Setenv("NCMM_DOCKER_OFFICIAL", "1")
	t.Setenv("NCMM_RUNTIME_MANAGED", "1")
	root := t.TempDir()
	t.Setenv("NCMM_RUNTIME_DIR", root)
	executable := filepath.Join(root, "versions", "v1-build", "ncmm")
	if err := os.MkdirAll(filepath.Dir(executable), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("current fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	s := &Server{opts: Options{Executable: executable, Version: "1.2.2-grails.1"}}
	state := updateState{LatestVersion: "1.2.2-grails.2"}
	if s.makeUpdateView(state).CanApply {
		t.Fatal("environment alone allowed container update")
	}
	if err := os.WriteFile(filepath.Join(root, "manager.json"), []byte(`{"format":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if !s.makeUpdateView(state).CanApply {
		t.Fatal("managed runtime cannot apply update")
	}
}

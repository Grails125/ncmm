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

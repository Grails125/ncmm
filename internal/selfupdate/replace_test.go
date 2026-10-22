package selfupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestReplacePreservesBackupAndPublishesCompleteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ncmm")
	if err := os.WriteFile(path, []byte("previous executable"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := Replace(path, []byte("complete new executable")); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{path: "complete new executable", path + ".old": "previous executable"} {
		data, err := os.ReadFile(name)
		if err != nil || string(data) != want {
			t.Fatalf("%s = %q, %v", name, data, err)
		}
	}
	if !Matches(path, Digest([]byte("complete new executable"))) {
		t.Fatal("installed digest did not match")
	}
}

func TestReplaceFailureKeepsOriginal(t *testing.T) {
	for _, scenario := range []string{"empty", "backup blocked"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ncmm")
			if err := os.WriteFile(path, []byte("original"), 0755); err != nil {
				t.Fatal(err)
			}
			data := []byte("replacement")
			if scenario == "empty" {
				data = nil
			} else {
				if err := os.Mkdir(path+".old", 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path+".old", "keep"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := Replace(path, data); err == nil {
				t.Fatal("expected failure")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != "original" {
				t.Fatalf("original lost: %q, %v", got, err)
			}
			matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".ncmm-update-*"))
			if err != nil || len(matches) != 0 {
				t.Fatalf("staging files leaked: %v, %v", matches, err)
			}
		})
	}
}

func TestMatchesRejectsMissingAndChangedExecutables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ncmm")
	if Matches(path, "") || Matches(path, Digest([]byte("expected"))) {
		t.Fatal("missing executable matched")
	}
	if err := os.WriteFile(path, []byte("downgraded"), 0755); err != nil {
		t.Fatal(err)
	}
	if Matches(path, Digest([]byte("expected"))) {
		t.Fatal("downgraded executable matched cached receipt")
	}
}

func TestFailedFinalRenameRestoresOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ncmm")
	if err := os.WriteFile(path, []byte("original"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := installStaged(path+".missing-stage", path); err == nil {
		t.Fatal("expected final rename failure")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "original" {
		t.Fatalf("original not restored: %q, %v", got, err)
	}
}

func TestRunningExecutableHelper(t *testing.T) {
	ready := os.Getenv("NCMM_REPLACE_TEST_READY")
	if ready == "" {
		return
	}
	if err := os.WriteFile(ready, []byte("ready"), 0600); err != nil {
		os.Exit(2)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestReplaceRunningExecutableKeepsProcessAndBackup(t *testing.T) {
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ncmm")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	if err := os.WriteFile(path, original, 0755); err != nil {
		t.Fatal(err)
	}
	ready := path + ".ready"
	cmd := exec.Command(path, "-test.run=^TestRunningExecutableHelper$")
	cmd.Env = append(os.Environ(), "NCMM_REPLACE_TEST_READY="+ready)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { cmd.Process.Kill(); <-done })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := Replace(path, []byte("new executable")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		done <- err
		t.Fatalf("replacement stopped running process: %v", err)
	default:
	}
	if !Matches(path+".old", Digest(original)) || !Matches(path, Digest([]byte("new executable"))) {
		t.Fatal("running executable replacement lost a complete file")
	}
}

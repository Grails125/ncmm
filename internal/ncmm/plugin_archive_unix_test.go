//go:build !windows

package ncmm

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestPluginArchiveRejectsFIFOWithoutBlocking(t *testing.T) {
	oldDir, newDir := t.TempDir(), t.TempDir()
	fifo := filepath.Join(oldDir, "plugin-ipc")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- preservePluginFiles(oldDir, newDir) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted as plugin data")
		}
	case <-time.After(time.Second):
		t.Fatal("copy blocked opening FIFO")
	}
	if _, err := os.Stat(filepath.Join(newDir, "plugin-ipc")); !os.IsNotExist(err) {
		t.Fatal("FIFO copied")
	}
}

func TestPluginArchivePreservesPrivateConfigPermissions(t *testing.T) {
	p := NewPlugin(&Root{Opts: RootOpts{Home: t.TempDir()}}, nil)
	_, err := p.InstallFromArchive(pluginArchiveFiles(t, map[string]string{"ncmm-demo": "old", "config.yaml": "secret: old"}), "old.zip", "")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(p.DefaultPluginDir(), "ncmm-demo", "config.yaml")
	if err = os.Chmod(target, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = p.InstallFromArchive(pluginArchiveFiles(t, map[string]string{"ncmm-demo": "new", "config.yaml": "default: true"}), "new.zip", "")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private config permissions changed: %v %v", info, err)
	}
}

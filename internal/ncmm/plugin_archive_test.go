package ncmm

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func pluginArchiveFiles(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	for name, content := range files {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestPluginArchiveUpgradePreservesConfigAndLocalData(t *testing.T) {
	home := t.TempDir()
	plugin := NewPlugin(&Root{Opts: RootOpts{Home: home}}, nil)
	old := pluginArchiveFiles(t, map[string]string{"ncmm-demo.exe": "old", "config.yaml": "mine: true", "local.db": "keep"})
	if _, err := plugin.InstallFromArchive(old, "demo.zip", ""); err != nil {
		t.Fatal(err)
	}
	new := pluginArchiveFiles(t, map[string]string{"ncmm-demo.exe": "new", "config.yaml": "mine: false", "plugin.json": `{"name":"demo","version":"v2"}`})
	if _, err := plugin.InstallFromArchive(new, "demo.zip", ""); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"ncmm-demo.exe": "new", "config.yaml": "mine: true", "local.db": "keep"} {
		got, err := os.ReadFile(filepath.Join(home, "plugins", "ncmm-demo", name))
		if err != nil || string(got) != want {
			t.Fatalf("%s changed: %q, %v", name, got, err)
		}
	}
}

func TestPluginArchiveRejectsTraversalAndKeepsInstalledVersion(t *testing.T) {
	home := t.TempDir()
	plugin := NewPlugin(&Root{Opts: RootOpts{Home: home}}, nil)
	if _, err := plugin.InstallFromArchive(pluginArchiveFiles(t, map[string]string{"ncmm-demo.exe": "old"}), "demo.zip", ""); err != nil {
		t.Fatal(err)
	}
	malformed := pluginArchiveFiles(t, map[string]string{"ncmm-demo.exe": "new", "../outside": "invalid"})
	if _, err := plugin.InstallFromArchive(malformed, "demo.zip", ""); err == nil {
		t.Fatal("unsafe archive accepted")
	}
	got, err := os.ReadFile(filepath.Join(home, "plugins", "ncmm-demo", "ncmm-demo.exe"))
	if err != nil || string(got) != "old" {
		t.Fatalf("old installation changed on failure: %q %v", got, err)
	}
}

package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPreviewConfigMigrationReadsLegacyFieldsWithoutWriting(t *testing.T) {
	var legacy map[string]interface{}
	if err := yaml.Unmarshal(DefaultYAML(), &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["version"] = "1.1.14"
	legacy["musicianVip"] = legacy["musician"]
	delete(legacy, "musician")
	tasks := legacy["task"].(map[string]interface{})
	tasks["musicianVip"] = true
	delete(tasks, "musician-sign")
	data, err := yaml.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewConfigMigration(path)
	if err != nil || preview.Musician == nil || !preview.Task.MusicianSign {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatalf("preview changed the source configuration: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || entries[0].Name() != "config.yaml" {
		t.Fatalf("preview created migration files: %v, %v", entries, err)
	}
}

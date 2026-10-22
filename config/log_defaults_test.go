package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultLogRotationSizeAndMigrationPreserveCustomSize(t *testing.T) {
	if got := GetDefault().Log.Rotate.MaxSize; got != 5 {
		t.Errorf("default log size=%d; want 5 MB", got)
	}
	for _, size := range []int{100, 37, 500} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(fmt.Sprintf("version: 1.0\nlog:\n  rotate:\n    maxsize: %d\n", size)), 0600); err != nil {
				t.Fatal(err)
			}
			if err := AutoUpgradeConfig(path); err != nil {
				t.Fatal(err)
			}
			cfg, err := New(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Log.Rotate.MaxSize; got != size {
				t.Fatalf("existing log size changed: %d -> %d", size, got)
			}
		})
	}
}

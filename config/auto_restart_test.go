package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutoRestartDefaultsAndExplicitDisable(t *testing.T) {
	for _, value := range []string{"true", "false", "omitted"} {
		t.Run(value, func(t *testing.T) {
			data := string(DefaultYAML())
			if value == "omitted" {
				data = strings.Replace(data, "  auto_restart: true", "", 1)
			} else {
				data = strings.Replace(data, "  auto_restart: true", "  auto_restart: "+value, 1)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := New(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			if cfg.Updater.AutoRestart == nil || *cfg.Updater.AutoRestart != (value != "false") {
				t.Fatalf("auto_restart=%v", cfg.Updater.AutoRestart)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != data {
				t.Fatal("reading auto_restart changed existing config")
			}
		})
	}
}

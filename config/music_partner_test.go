package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMusicPartnerDefaultsAndValidation(t *testing.T) {
	c := &Config{Task: &TaskConf{}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	p := c.MusicPartner
	if c.Task.MusicPartner || !p.EnableMain || p.EnableSecondaries || p.Score != 3 || !p.ExtraTasks || p.FullExtraTasks || p.ExtraLimit != 7 || p.WaitMin != 15 || p.WaitMax != 20 || p.SyncYunCircle {
		t.Fatalf("unexpected defaults: %+v", p)
	}
	for _, tc := range []MusicPartnerConf{{Score: -1}, {Score: 5}, {ExtraLimit: -1}, {ExtraLimit: 501}, {WaitMin: -1}, {WaitMin: 21, WaitMax: 20}, {WaitMax: 301}} {
		c := &Config{MusicPartner: &tc}
		if err := c.Validate(); err == nil {
			t.Fatalf("accepted invalid config: %+v", tc)
		}
	}
}

func TestMusicPartnerConfigMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	old := `version: 1.2.2
accounts:
  main: ./mine.json
  secondary: []
task:
  music-partner: false
  slow_tasks: [playids]
musicPartner:
  enableMain: false
  extraTasks: false
  score: 4
`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	if err := AutoUpgradeConfigIfNeeded(path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Accounts.Main != "./mine.json" || len(c.Accounts.Secondary) != 0 || c.Task.MusicPartner || c.MusicPartner.EnableMain || c.MusicPartner.ExtraTasks || c.MusicPartner.Score != 4 || c.MusicPartner.WaitMin != 15 || len(c.Task.SlowTasks) != 1 {
		t.Fatalf("migration lost explicit user values: %s", b)
	}
}

func TestNewerSourceConfigCannotBeDowngraded(t *testing.T) {
	for _, version := range []string{"1.2.9", "1.3.0", "2.0.0"} {
		t.Run(version, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			original := []byte("version: " + version + "\nfutureField: keep-me\n")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			for _, upgrade := range []func(string) error{AutoUpgradeConfigIfNeeded, AutoUpgradeConfig} {
				if err := upgrade(path); err == nil {
					t.Fatal("newer config accepted")
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(original, after) {
					t.Fatal("newer config modified")
				}
			}
			if err := (&Config{Version: version}).Validate(); err == nil {
				t.Fatal("newer config loaded")
			}
		})
	}
}

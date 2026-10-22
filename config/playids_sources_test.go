package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPlaylistsAcceptStringAndList(t *testing.T) {
	for _, text := range []string{"playlistIds: '1，2'", "playlistIds: ['1，2']"} {
		var cfg PlayIdsConfig
		if err := yaml.Unmarshal([]byte(text), &cfg); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(cfg.PlaylistIDs, StringOrSlice{"1，2"}) {
			t.Fatalf("playlistIds = %v", cfg.PlaylistIDs)
		}
	}
}

func TestNewPlayDefaultsPreserveExistingTaskChoicesAndPartnerConfig(t *testing.T) {
	var defaults Config
	if err := yaml.Unmarshal(DefaultYAML(), &defaults); err != nil {
		t.Fatal(err)
	}
	if defaults.Task.FansGroup {
		t.Fatal("new installations must leave fansgroup disabled")
	}
	file := filepath.Join(t.TempDir(), "config.yaml")
	const previous = `version: "1.2.2"
task:
  fansgroup: true
playids:
  ids: "1，2 # 我的备注"
  playlistIds: "https://music.163.com/#/playlist?id=123"
musicPartner:
  scoreMin: 2
  scoreMax: 5
  comments:
    "2": [保留我的乐评]
`
	if err := os.WriteFile(file, []byte(previous), 0600); err != nil {
		t.Fatal(err)
	}
	if err := AutoUpgradeConfig(file); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var got Config
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Task.FansGroup || got.PlayIds.IDs != "1，2 # 我的备注" || !reflect.DeepEqual(got.PlayIds.PlaylistIDs, StringOrSlice{"https://music.163.com/#/playlist?id=123"}) {
		t.Fatal("existing task and song input choices changed during merge")
	}
	if got.MusicPartner.ScoreMin != 2 || got.MusicPartner.ScoreMax != 5 || !reflect.DeepEqual(got.MusicPartner.Comments["2"], []string{"保留我的乐评"}) {
		t.Fatal("music partner preferences changed during merge")
	}
}

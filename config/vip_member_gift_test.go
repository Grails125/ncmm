package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestVipMemberGiftUpgradePreservesMusicPartnerAndDisablesSecondaryGifts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	const previous = `version: "1.2.2"
accounts:
  main: ./main.json
  secondary: [./fan.json]
vipMemberGift:
  enableMain: true
  enableSecondaries: true
  enableGift: true
  enableClaim: true
musicPartner:
  enableMain: true
  extraTasks: true
  scoreMin: 2
  scoreMax: 4
  extraLimitMin: 8
  extraLimitMax: 10
  waitMin: 15
  waitMax: 25
  comments:
    "2": [自定义乐评]
`
	if err := os.WriteFile(path, []byte(previous), 0600); err != nil {
		t.Fatal(err)
	}
	if err := AutoUpgradeConfig(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		VipMemberGift VipMemberGiftConf `yaml:"vipMemberGift"`
		MusicPartner  map[string]any    `yaml:"musicPartner"`
	}
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !got.VipMemberGift.EnableSecondaries || !got.VipMemberGift.EnableGift || !got.VipMemberGift.EnableClaim || got.VipMemberGift.EnableGiftSecondaries {
		t.Fatalf("unsafe gift defaults: %+v", got.VipMemberGift)
	}
	if got.MusicPartner["scoreMin"] != 2 || got.MusicPartner["scoreMax"] != 4 || got.MusicPartner["extraLimitMin"] != 8 || got.MusicPartner["extraLimitMax"] != 10 || got.MusicPartner["waitMax"] != 25 || got.MusicPartner["enableMain"] != true || got.MusicPartner["extraTasks"] != true {
		t.Fatalf("music partner configuration changed: %+v", got.MusicPartner)
	}
	comments, ok := got.MusicPartner["comments"].(map[string]any)
	if !ok {
		t.Fatalf("comments missing: %+v", got.MusicPartner)
	}
	values, ok := comments["2"].([]any)
	if !ok || len(values) != 1 || values[0] != "自定义乐评" {
		t.Fatalf("comments changed: %+v", comments)
	}
}

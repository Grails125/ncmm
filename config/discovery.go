package config

import (
	"bytes"
	"os"

	"gopkg.in/yaml.v3"
)

// PreviewConfigMigration 只在内存中迁移旧字段并严格校验，自动发现阶段不改写候选文件。
func PreviewConfigMigration(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	var version verStruct
	if err := document.Decode(&version); err != nil {
		return nil, err
	}
	partnerUpgrade, err := migrateMusicPartnerRangeNodes(&document)
	if err != nil {
		return nil, err
	}
	if version.Version != defaultConfig.Version || partnerUpgrade {
		migrateNode(&document)
	}
	data, err = yaml.Marshal(&document)
	if err != nil {
		return nil, err
	}
	v := newConfigViper()
	if err := v.ReadConfig(bytes.NewReader(data)); err != nil {
		return nil, err
	}
	return decodeConfig(v)
}

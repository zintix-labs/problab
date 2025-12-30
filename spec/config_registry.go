package spec

import (
	"encoding/json"

	"github.com/zintix-labs/problab/errs"
	"gopkg.in/yaml.v3"
)

// GetGameSettingByYAML
// 會讀取 YAML 設定、初始化各子設定並執行基本檢查後回傳。
func GetGameSettingByYAML(data []byte) (*GameSetting, error) {
	gs := &GameSetting{}
	if err := yaml.Unmarshal(data, gs); err != nil {
		return nil, errs.Wrap(err, "failed to unmarshall yaml")
	}

	// 設定檔初始化
	if err := gs.init(); err != nil {
		return nil, errs.Wrap(err, "game setting initialized err")
	}

	return gs, nil
}

// GetGameSettingByJSON
// 會讀取 Json 設定、初始化各子設定並執行基本檢查後回傳
func GetGameSettingByJSON(data []byte) (*GameSetting, error) {
	gs := &GameSetting{}
	if err := json.Unmarshal(data, gs); err != nil {
		return nil, errs.Wrap(err, "can not unmarshall json byte")
	}

	// 設定檔初始化
	if err := gs.init(); err != nil {
		return nil, errs.Wrap(err, "game setting initialized err")
	}

	return gs, nil
}

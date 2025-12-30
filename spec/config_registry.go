// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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

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

// GameModeSetting 將單一遊戲模式（如主遊戲、免費遊戲等）所需設定統整在一起。
type GameModeSetting struct {
	ScreenSetting    ScreenSetting    `yaml:"screen_setting"     json:"screen_setting"`
	GenScreenSetting GenScreenSetting `yaml:"gen_screen_setting" json:"gen_screen_setting"`
	SymbolSetting    SymbolSetting    `yaml:"symbol_setting"     json:"symbol_setting"`
	HitSetting       HitSetting       `yaml:"hit_setting"        json:"hit_setting"`
}

func (gms *GameModeSetting) init() error {
	if err := gms.ScreenSetting.Init(); err != nil {
		return err
	}
	if err := gms.GenScreenSetting.Init(); err != nil {
		return err
	}
	if err := gms.SymbolSetting.Init(); err != nil {
		return err
	}
	if err := gms.HitSetting.Init(); err != nil {
		return err
	}
	return nil
}

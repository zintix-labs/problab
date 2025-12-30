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

import "github.com/zintix-labs/problab/errs"

// ScreenSetting 描述盤面樣式的設定。
//
// Fields:
//   - Columns: 盤面軸數（列數）
//   - Rows: 盤面列數
//   - Damp: 額外上下圖標顆數
//   - Mask: 盤面遮罩，0 為空、1 代表有格子；若留空則代表完整矩陣。
type ScreenSetting struct {
	Columns    int     `yaml:"columns"   json:"columns"`
	Rows       int     `yaml:"rows"      json:"rows"`
	Damp       int     `yaml:"damp"      json:"damp"`
	Mask       []uint8 `yaml:"mask"      json:"mask"`
	ScreenSize int     `yaml:"-"         json:"-"`
	initFlag   bool
}

// Init 檢查不合法的設定
func (ss *ScreenSetting) Init() error {
	// 檢查初始化旗標
	if ss.initFlag {
		return nil
	}
	// 檢查合法性
	ss.ScreenSize = ss.Rows * ss.Columns
	// 如果Mask 不是nil，Columns x Rows 要等於 Mask長度
	if ss.Mask != nil {
		if len(ss.Mask) != ss.ScreenSize {
			return errs.NewFatal("len(mask) != screen size")
		}
	}
	ss.initFlag = true
	return nil
}

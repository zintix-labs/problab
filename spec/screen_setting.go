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
	"fmt"

	"github.com/zintix-labs/problab/errs"
)

// ScreenSetting 描述盤面樣式的設定。
//
// Fields:
//   - Columns: 盤面軸數（列數）
//   - Rows: 盤面列數
//   - Damp: 額外上下圖標顆數
//   - Trim: 每軸上方裁切；省略代表全 0，0代表不裁切, 1代表裁切一格，長度必須等於columns
type ScreenSetting struct {
	Columns        int   `yaml:"columns"   json:"columns"`
	Rows           int   `yaml:"rows"      json:"rows"`
	Damp           int   `yaml:"damp"      json:"damp"`
	Trim           []int `yaml:"trim"      json:"trim"`
	ScreenSize     int   `yaml:"-"         json:"-"`
	FullScreenSize int   `yaml:"-"         json:"-"`
	initFlag       bool
}

// Init 檢查不合法的設定
func (ss *ScreenSetting) Init() error {
	// 檢查初始化旗標
	if ss.initFlag {
		return nil
	}
	// 檢查合法性
	err := screenSettingValid(ss)
	if err != nil {
		return err
	}
	ss.ScreenSize = ss.Rows * ss.Columns
	ss.FullScreenSize = (ss.Rows + ss.Damp*2) * ss.Columns
	// 如果Trim 是nil 把 make([]int, ss.Columns) 補上
	if ss.Trim == nil {
		ss.Trim = make([]int, ss.Columns)
	}
	ss.initFlag = true
	return nil
}

func screenSettingValid(ss *ScreenSetting) error {
	// 檢查 columns >= 1
	if ss.Columns < 1 {
		return errs.NewFatal("columns must be at least 1")
	}
	// 檢查 rows >= 1
	if ss.Rows < 1 {
		return errs.NewFatal("rows must be at least 1")
	}
	// 檢查 Damp >= 0
	if ss.Damp < 0 {
		return errs.NewFatal("damp must be at least 0")
	}
	// 檢查 trim
	if ss.Trim != nil {
		// trim 長度 == columns
		if len(ss.Trim) != ss.Columns {
			return errs.NewFatal("len(trim) != columns size")
		}
		// trim[v] >= 0 && trim[v] < ss.Row
		rw := ss.Rows - 1
		for i, v := range ss.Trim {
			if (v < 0) || (v > rw) {
				return errs.NewFatal(fmt.Sprintf("trim %d must be between 0 to %d, %d given", i, rw, v))
			}
		}
	}
	return nil
}

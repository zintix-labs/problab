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
	"maps"
	"slices"

	"github.com/zintix-labs/problab/errs"
)

// BetType 定義下注形態
type BetType int

const (
	BetTypeLineLTR BetType = iota
	BetTypeLineRTL
	BetTypeLineBoth
	BetTypeWayLTR
	BetTypeWayRTL
	BetTypeWayBoth
	BetTypeCount
	BetTypeCluster
	BetTypeClusterByNeighbor
)

var betTypeMap = map[string]BetType{
	"line_ltr":  BetTypeLineLTR,
	"line_rtl":  BetTypeLineRTL,
	"line_both": BetTypeLineBoth,
	"way_ltr":   BetTypeWayLTR,
	"way_rtl":   BetTypeWayRTL,
	"way_both":  BetTypeWayBoth,
	"count":     BetTypeCount,
	"cluster":   BetTypeCluster,

	"cluster_by_neighbor": BetTypeClusterByNeighbor,
}

func ParseBetType(s string) (BetType, bool) {
	betType, ok := betTypeMap[s]
	return betType, ok
}

// IsBetTypeLine 回傳是否屬於線類型下注
func IsBetTypeLine(b BetType) bool {
	return b == BetTypeLineLTR || b == BetTypeLineRTL || b == BetTypeLineBoth
}

// IsBetTypeWay 回傳是否屬於 Ways 類型下注
func IsBetTypeWay(b BetType) bool {
	return b == BetTypeWayLTR || b == BetTypeWayRTL || b == BetTypeWayBoth
}

// IsBetTypeCount 回傳是否屬於 Count 類型下注。
func IsBetTypeCount(b BetType) bool {
	return b == BetTypeCount
}

// IsBetTypeCluster 回傳是否屬於 Cluster 類型下注（含自訂相鄰格）。
func IsBetTypeCluster(b BetType) bool {
	return b == BetTypeCluster || b == BetTypeClusterByNeighbor
}

// IsLeftToRight 回傳下注是否包含由左至右的走線邏輯。
func IsLeftToRight(b BetType) bool {
	return (b == BetTypeLineLTR) || (b == BetTypeLineBoth) || (b == BetTypeWayLTR) || (b == BetTypeWayBoth)
}

// IsRightToLeft 回傳下注是否包含由右至左的走線邏輯。
func IsRightToLeft(b BetType) bool {
	return (b == BetTypeLineRTL) || (b == BetTypeLineBoth) || (b == BetTypeWayRTL) || (b == BetTypeWayBoth)
}

// HitSetting 描述遊戲模式輸贏計算的押注型態與線表。
//
// Fields:
//   - Neighbor: 僅 cluster_by_neighbor 使用；格子索引（row*columns+col）對應其相鄰格清單，
//     未列出的格子沒有相鄰格，相鄰關係必須雙向列出
type HitSetting struct {
	BetType    BetType         `yaml:"-"           json:"-"`
	BetTypeStr string          `yaml:"bet_type"    json:"bet_type"`
	LineTable  [][]int16       `yaml:"line_table"  json:"line_table"`
	Neighbor   map[int][]int16 `yaml:"neighbor,omitempty"    json:"neighbor,omitempty"`
	initFlag   bool
}

// Init 標記 HitSetting 已初始完成，如後續需要新增驗證可在此擴充。
func (hs *HitSetting) Init() error {
	if hs.initFlag {
		return nil
	}

	if len(hs.BetTypeStr) == 0 {
		return errs.NewFatal("bet_type is required")
	}

	bt, ok := ParseBetType(hs.BetTypeStr)
	if !ok {
		return errs.NewFatal(fmt.Sprintf("bet_type error: %s", hs.BetTypeStr))
	}
	hs.BetType = bt

	if IsBetTypeLine(hs.BetType) && len(hs.LineTable) == 0 {
		return errs.NewFatal("bet_type is line but line_table is empty")
	}
	if hs.BetType == BetTypeClusterByNeighbor && len(hs.Neighbor) == 0 {
		return errs.NewFatal("bet_type is cluster_by_neighbor but neighbor is empty")
	}
	if hs.BetType != BetTypeClusterByNeighbor && len(hs.Neighbor) > 0 {
		return errs.NewFatal(fmt.Sprintf("neighbor is only allowed with bet_type cluster_by_neighbor, got %s", hs.BetTypeStr))
	}
	hs.initFlag = true
	return nil
}

// hitNeighborValid 依盤面檢查 neighbor：索引在盤面內、不可是被 trim 的格子、
// 不可相鄰自己或重複，且相鄰關係必須雙向列出。
// 單向的相鄰會讓 cluster 結果取決於掃描起點，因此直接報錯而不自動補齊。
func hitNeighborValid(hs *HitSetting, ss *ScreenSetting) error {
	if hs.BetType != BetTypeClusterByNeighbor {
		return nil
	}
	size := ss.Rows * ss.Columns
	usable := func(i int) error {
		if i < 0 || i >= size {
			return errs.NewFatal(fmt.Sprintf("neighbor cell %d must be between 0 to %d", i, size-1))
		}
		if i/ss.Columns < ss.Trim[i%ss.Columns] {
			return errs.NewFatal(fmt.Sprintf("neighbor cell %d is trimmed", i))
		}
		return nil
	}
	// 依索引排序檢查，讓錯誤訊息固定
	for _, i := range slices.Sorted(maps.Keys(hs.Neighbor)) {
		if err := usable(i); err != nil {
			return err
		}
		list := hs.Neighbor[i]
		for k, v := range list {
			j := int(v)
			if err := usable(j); err != nil {
				return err
			}
			if j == i {
				return errs.NewFatal(fmt.Sprintf("neighbor cell %d lists itself", i))
			}
			if slices.Contains(list[:k], v) {
				return errs.NewFatal(fmt.Sprintf("neighbor cell %d lists %d more than once", i, j))
			}
			if !slices.Contains(hs.Neighbor[j], int16(i)) {
				return errs.NewFatal(fmt.Sprintf("neighbor cell %d lists %d but %d does not list %d", i, j, j, i))
			}
		}
	}
	return nil
}

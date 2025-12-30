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

// IsBetTypeCluster 回傳是否屬於 Cluster 類型下注。
func IsBetTypeCluster(b BetType) bool {
	return b == BetTypeCluster
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
type HitSetting struct {
	BetType    BetType   `yaml:"-"           json:"-"`
	BetTypeStr string    `yaml:"bet_type"    json:"bet_type"`
	LineTable  [][]int16 `yaml:"line_table"  json:"line_table"`
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
	hs.initFlag = true
	return nil
}

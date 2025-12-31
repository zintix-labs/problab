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

package calc

import (
	"log"

	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/spec"
)

// CalcScreenFn 定義計算盤面的函式型態，接收盤面與算分器並回傳結果。
type CalcScreenFn func(betMult int, screen []int16, gmr *buf.GameModeResult, sc *ScreenCalculator)

var fromBetTypeGetCalcScreenFn = map[spec.BetType]CalcScreenFn{
	spec.BetTypeLineLTR:  CalcByLine,
	spec.BetTypeLineRTL:  CalcByLine,
	spec.BetTypeLineBoth: CalcByLine,
	spec.BetTypeWayLTR:   CalcByWay,
	spec.BetTypeWayRTL:   CalcByWay,
	spec.BetTypeWayBoth:  CalcByWay,
	spec.BetTypeCount:    CalcByCount,
	spec.BetTypeCluster:  CalcByCluster,
}

// SymbolMask 使用 uint64 以支援最多 64 種不同的圖標
// 使用方式為將圖標的索引位置對應到遮罩的位元位置
// 例如 : 若圖標索引為 3，則對應的遮罩位元為 1 << 3
// 判斷圖標是否在遮罩中，可以使用位元運算 (mask >> index) & 1 == 1
type SymbolMask = uint64

// ScreenCalculator 負責根據盤面計算輸贏結果
type ScreenCalculator struct {
	// 讀取自設定檔
	ScreenSetting *spec.ScreenSetting
	SymbolSetting *spec.SymbolSetting
	HitSetting    *spec.HitSetting

	// Screen 設定的預處理資料(快取)
	Cols       int // 快取軸數
	Rows       int // 快取列數
	ScreenSize int // 快取盤面大小

	// Symbol 設定的預處理資料
	wildMask      SymbolMask // Wild符號遮罩
	paidMask      SymbolMask // 具有派彩的符號遮罩
	PayTableFlat  []int      // 平坦化的派彩表 (重複使用)
	PayTableIndex []int      // 派彩表索引 (重複使用)
	minPayCount   []int      // 紀錄每個圖標最低中獎顆數

	// Hit 設定的預處理資料
	LTR bool // 是否計算LTR
	RTL bool // 是否計算RTL

	// ---------- Line 熱路徑暫存資料 ----------
	LineCount        int     // 線表數量 (重複使用)
	LineTableFlat    []int16 // 平坦化的線表 (重複使用)
	LineTableIndex   []int   // 線表索引 (重複使用)
	LineTableFlatRTL []int16 // 反轉的平坦化線表 (重複使用)

	// ----------  Way 熱路徑暫存資料 ----------
	symbolInCols   []int   // 盤面每個圖標每個軸上的數量
	symbolCounts   []int   // 盤面每個圖標出現的總數
	hitMapFlat     []int16 // 每個圖標 每個軸上的數量(一維攤平)
	wildInCols     []int   // 所有wild當作一種
	wildHitMapFlat []int16 // 所有wild當作一種

	// ----------  Collect 熱路徑暫存資料 ----------
	wildCounts int // 計算wild盤面上出現總數

	// ----------  Cluster 熱路徑暫存資料 ----------
	clusterBuf *clusterBuf

	// calcScreen預封装
	calcScreenFn CalcScreenFn
}

// NewScreenCalculator 建立算分器。
func NewScreenCalculator(gameModeSetting *spec.GameModeSetting) *ScreenCalculator {
	sc := &ScreenCalculator{
		ScreenSetting: &gameModeSetting.ScreenSetting,
		SymbolSetting: &gameModeSetting.SymbolSetting,
		HitSetting:    &gameModeSetting.HitSetting,
	}
	sc.init()
	return sc
}

// CalcScreen 以預先決定的熱路徑計算盤面並回傳結果。
func (sc *ScreenCalculator) CalcScreen(betMult int, screen []int16, gmr *buf.GameModeResult) {
	sc.calcScreenFn(betMult, screen, gmr, sc)
}

// ============================================================
// ** 以下內部方法 **
// ============================================================

// init 初始化算分器的快取資料與熱路徑依賴
func (sc *ScreenCalculator) init() {
	sc.initSettings()   // 錯誤防禦
	sc.initScreen()     // Screen 設定
	sc.initSymbols()    // Symbol設定 wildMask 與 payMask
	sc.initHitSetting() // 中獎設定
	sc.initCalcFn()     // 預封装盤面算分函數
}

func (sc *ScreenCalculator) initSettings() {
	_ = sc.ScreenSetting.Init()
	_ = sc.SymbolSetting.Init()
	_ = sc.HitSetting.Init()
}

func (sc *ScreenCalculator) initScreen() {
	sc.Cols = sc.ScreenSetting.Columns
	sc.Rows = sc.ScreenSetting.Rows
	sc.ScreenSize = sc.ScreenSetting.Columns * sc.ScreenSetting.Rows
}

func (sc *ScreenCalculator) initSymbols() {
	// wildMask : Wild符號遮罩
	// payMask : 具有派彩的符號遮罩
	for i, st := range sc.SymbolSetting.SymbolTypes {
		if st == spec.SymbolTypeWild {
			sc.wildMask |= (1 << uint(i))
		}
		arr := sc.SymbolSetting.PayTable[i]
		for _, v := range arr {
			if v > 0 {
				sc.paidMask |= (1 << uint(i))
				break
			}
		}
	}
	sc.PayTableFlat = sc.SymbolSetting.PayTableFlat
	sc.PayTableIndex = sc.SymbolSetting.PayTableIndex
	sc.minPayCount = make([]int, sc.SymbolSetting.SymbolCount)
	pid := append(sc.PayTableIndex, len(sc.PayTableFlat))

	for s := 0; s < sc.SymbolSetting.SymbolCount; s++ {
		idx := 0
		for i := pid[s]; i < pid[s+1]; i++ {
			idx++
			if sc.PayTableFlat[i] != 0 {
				sc.minPayCount[s] = idx
				break
			}
		}
	}
}

func (sc *ScreenCalculator) initHitSetting() {
	betType := sc.HitSetting.BetType
	if spec.IsBetTypeLine(betType) {
		sc.initBetTypeLine()
		sc.initDirection()
		return
	}
	if spec.IsBetTypeWay(betType) {
		sc.initBetTypeWay()
		sc.initDirection()
	}
	if spec.IsBetTypeCount(betType) {
		sc.initBetTypeCount()
	}
	if spec.IsBetTypeCluster(betType) {
		sc.initClusterBuf()
		// sc.initClusterBufOld() // Cluster 暫存緩衝
	}

}

func (sc *ScreenCalculator) initDirection() {
	sc.LTR = spec.IsLeftToRight(sc.HitSetting.BetType)
	sc.RTL = spec.IsRightToLeft(sc.HitSetting.BetType)
}

func (sc *ScreenCalculator) initBetTypeLine() {
	// LineTableFlat 與 LineTableIndex
	numLines := len(sc.HitSetting.LineTable)
	sc.LineCount = numLines
	cols := sc.ScreenSetting.Columns
	sc.LineTableFlat = make([]int16, numLines*cols)
	sc.LineTableFlatRTL = make([]int16, numLines*cols)
	sc.LineTableIndex = make([]int, numLines)

	write := 0
	for i, line := range sc.HitSetting.LineTable {
		if len(line) != cols {
			panic("LineTable length mismatch with screen columns")
		}
		sc.LineTableIndex[i] = write
		for c, r := range line {
			idx := int16(int(r)*cols + c)

			sc.LineTableFlat[write+c] = idx             // 正向
			sc.LineTableFlatRTL[write+(cols-1-c)] = idx // 反向
		}
		write += cols
	}
}

func (sc *ScreenCalculator) initBetTypeWay() {
	sc.symbolInCols = make([]int, sc.SymbolSetting.SymbolCount*sc.ScreenSetting.Columns)
	sc.hitMapFlat = make([]int16, sc.SymbolSetting.SymbolCount*sc.ScreenSetting.Columns*sc.ScreenSetting.Rows)
	sc.symbolCounts = make([]int, sc.SymbolSetting.SymbolCount)
	sc.wildHitMapFlat = make([]int16, sc.ScreenSetting.Columns*sc.ScreenSetting.Rows)
	sc.wildInCols = make([]int, sc.ScreenSetting.Columns)
}

func (sc *ScreenCalculator) initBetTypeCount() {
	sc.symbolCounts = make([]int, sc.SymbolSetting.SymbolCount)
	sc.hitMapFlat = make([]int16, sc.SymbolSetting.SymbolCount*sc.ScreenSetting.Columns*sc.ScreenSetting.Rows)
	sc.wildHitMapFlat = make([]int16, sc.ScreenSetting.Columns*sc.ScreenSetting.Rows)
	sc.wildCounts = 0
}

// 新增方法 initClusterBuf
func (sc *ScreenCalculator) initClusterBuf() {
	// 準備 cluster 緩衝（只配置一次；之後每回合重用）
	if sc.clusterBuf == nil {
		sc.clusterBuf = &clusterBuf{}
	}
	// 將目前的 Rows/Cols 與遮罩快取到 buffer
	sc.clusterBuf.resetSizes(sc.Rows, sc.Cols)
}

func (sc *ScreenCalculator) initCalcFn() {
	if fn, ok := fromBetTypeGetCalcScreenFn[sc.HitSetting.BetType]; ok {
		sc.calcScreenFn = fn
	} else {
		log.Fatalf("miss match bettype %v to calc screen function", sc.HitSetting.BetType)
	}
}

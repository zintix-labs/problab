package spec

import (
	"fmt"

	"github.com/zintix-labs/problab/errs"
)

// SymbolSetting 統整模式中的所有符號，並記錄衍生屬性（類型、賠付表、總數等）。
type SymbolSetting struct {
	SymbolUsedStr []string     `yaml:"symbol_used"  json:"symbol_used"`
	PayTable      [][]int      `yaml:"pay_table"    json:"pay_table"`
	SymbolUsed    []Symbol     `yaml:"-"           json:"-"`
	SymbolTypes   []SymbolType `yaml:"-"           json:"-"`
	SymbolCount   int          `yaml:"-"           json:"-"`
	PayTableFlat  []int        `yaml:"-"           json:"-"`
	PayTableIndex []int        `yaml:"-"           json:"-"`
	initFlag      bool
}

// Init 檢查設定並賦值
func (ss *SymbolSetting) Init() error {
	// 檢查初始化旗標
	if ss.initFlag {
		return nil
	}
	// 解析SymbolUsed
	if ss.SymbolUsed == nil {
		ss.SymbolUsed = make([]Symbol, len(ss.SymbolUsedStr))
		for id, str := range ss.SymbolUsedStr {
			su, ok := ParseSymbol(str)
			if !ok {
				return errs.NewFatal(fmt.Sprintf("symbol used has wrong elem %s", str))
			}
			ss.SymbolUsed[id] = su
		}
	}

	if len(ss.SymbolUsed) != len(ss.PayTable) {
		return errs.NewFatal("len(simbol_used) != len(pay_table)")
	}
	// 檢查 PayTable
	if len(ss.PayTable) == 0 {
		return errs.NewFatal("pay_table is empty")
	}
	payLen := len(ss.PayTable[0])
	ss.PayTableFlat = make([]int, len(ss.SymbolUsed)*payLen)
	ss.PayTableIndex = make([]int, len(ss.SymbolUsed))
	write := 0
	for rowIdx, payRow := range ss.PayTable {
		if len(payRow) != payLen {
			return errs.NewFatal("inconsistent pay table lengths")
		}
		ss.PayTableIndex[rowIdx] = write
		for i, v := range payRow {
			ss.PayTableFlat[write+i] = v
		}
		write += payLen
	}
	// 賦值
	for _, s := range ss.SymbolUsed {
		ss.SymbolTypes = append(ss.SymbolTypes, s.GetSymbolType())
	}
	ss.SymbolCount = len(ss.SymbolUsed)
	// set 初始化旗標
	ss.initFlag = true
	return nil
}

type Symbol int

const (
	// Z系列圖標(Zero) : 代表沒有得分圖標 None
	Z1 Symbol = iota // Z系列圖標 : Zero | None 圖標代表沒有得分圖標
	Z2               // Z系列圖標 : Zero | None 圖標代表沒有得分圖標
	Z3               // Z系列圖標 : Zero | None 圖標代表沒有得分圖標
	Z4               // Z系列圖標 : Zero | None 圖標代表沒有得分圖標
	Z5               // Z系列圖標 : Zero | None 圖標代表沒有得分圖標
	Z6               // Z系列圖標 : Zero | None 圖標代表沒有得分圖標
	Z7               // Z系列圖標 : Zero | None 圖標代表沒有得分圖標
	Z8               // Z系列圖標 : Zero | None 圖標代表沒有得分圖標
	Z9               // Z系列圖標 : Zero | None 圖標代表沒有得分圖標

	// S系列圖標：Special圖標是特殊符號
	S1 // S系列圖標：Special圖標是特殊符號
	S2 // S系列圖標：Special圖標是特殊符號
	S3 // S系列圖標：Special圖標是特殊符號
	S4 // S系列圖標：Special圖標是特殊符號
	S5 // S系列圖標：Special圖標是特殊符號
	S6 // S系列圖標：Special圖標是特殊符號
	S7 // S系列圖標：Special圖標是特殊符號
	S8 // S系列圖標：Special圖標是特殊符號
	S9 // S系列圖標：Special圖標是特殊符號

	// C系列圖標 : Scatter 圖標是分散符號
	C1 // C系列圖標 : Scatter 圖標是分散符號
	C2 // C系列圖標 : Scatter 圖標是分散符號
	C3 // C系列圖標 : Scatter 圖標是分散符號
	C4 // C系列圖標 : Scatter 圖標是分散符號
	C5 // C系列圖標 : Scatter 圖標是分散符號
	C6 // C系列圖標 : Scatter 圖標是分散符號
	C7 // C系列圖標 : Scatter 圖標是分散符號
	C8 // C系列圖標 : Scatter 圖標是分散符號
	C9 // C系列圖標 : Scatter 圖標是分散符號

	// W系列圖標 : Wild 圖標是百搭符號
	W1 // W系列圖標 : Wild 圖標是百搭符號
	W2 // W系列圖標 : Wild 圖標是百搭符號
	W3 // W系列圖標 : Wild 圖標是百搭符號
	W4 // W系列圖標 : Wild 圖標是百搭符號
	W5 // W系列圖標 : Wild 圖標是百搭符號
	W6 // W系列圖標 : Wild 圖標是百搭符號
	W7 // W系列圖標 : Wild 圖標是百搭符號
	W8 // W系列圖標 : Wild 圖標是百搭符號
	W9 // W系列圖標 : Wild 圖標是百搭符號

	// H系列圖標 : High 圖標是高分符號
	H1 // H系列圖標 : High 圖標是高分符號
	H2 // H系列圖標 : High 圖標是高分符號
	H3 // H系列圖標 : High 圖標是高分符號
	H4 // H系列圖標 : High 圖標是高分符號
	H5 // H系列圖標 : High 圖標是高分符號
	H6 // H系列圖標 : High 圖標是高分符號
	H7 // H系列圖標 : High 圖標是高分符號
	H8 // H系列圖標 : High 圖標是高分符號
	H9 // H系列圖標 : High 圖標是高分符號

	// L系列圖標 : Low 圖標是低分符號
	L1 // L系列圖標 : Low 圖標是低分符號
	L2 // L系列圖標 : Low 圖標是低分符號
	L3 // L系列圖標 : Low 圖標是低分符號
	L4 // L系列圖標 : Low 圖標是低分符號
	L5 // L系列圖標 : Low 圖標是低分符號
	L6 // L系列圖標 : Low 圖標是低分符號
	L7 // L系列圖標 : Low 圖標是低分符號
	L8 // L系列圖標 : Low 圖標是低分符號
	L9 // L系列圖標 : Low 圖標是低分符號
)

var symbolMap = map[string]Symbol{
	"Z1": Z1,
	"Z2": Z2,
	"Z3": Z3,
	"Z4": Z4,
	"Z5": Z5,
	"Z6": Z6,
	"Z7": Z7,
	"Z8": Z8,
	"Z9": Z9,
	"S1": S1,
	"S2": S2,
	"S3": S3,
	"S4": S4,
	"S5": S5,
	"S6": S6,
	"S7": S7,
	"S8": S8,
	"S9": S9,
	"C1": C1,
	"C2": C2,
	"C3": C3,
	"C4": C4,
	"C5": C5,
	"C6": C6,
	"C7": C7,
	"C8": C8,
	"C9": C9,
	"W1": W1,
	"W2": W2,
	"W3": W3,
	"W4": W4,
	"W5": W5,
	"W6": W6,
	"W7": W7,
	"W8": W8,
	"W9": W9,
	"H1": H1,
	"H2": H2,
	"H3": H3,
	"H4": H4,
	"H5": H5,
	"H6": H6,
	"H7": H7,
	"H8": H8,
	"H9": H9,
	"L1": L1,
	"L2": L2,
	"L3": L3,
	"L4": L4,
	"L5": L5,
	"L6": L6,
	"L7": L7,
	"L8": L8,
	"L9": L9,
}

func ParseSymbol(s string) (Symbol, bool) {
	sym, ok := symbolMap[s]
	return sym, ok
}

// IsSymbolNone 回傳符號是否屬於 None 類型。
func IsSymbolNone(s Symbol) bool { return (s >= Z1) && (s <= Z9) }

// IsSymbolSpecial 回傳符號是否屬於特殊符號。
func IsSymbolSpecial(s Symbol) bool { return (s >= S1) && (s <= S9) }

// IsSymbolScatter 回傳符號是否屬於 Scatter 符號。
func IsSymbolScatter(s Symbol) bool { return (s >= C1) && (s <= C9) }

// IsSymbolWild 回傳符號是否屬於 Wild 符號。
func IsSymbolWild(s Symbol) bool { return (s >= W1) && (s <= W9) }

// IsSymbolHigh 回傳符號是否屬於高分符號。
func IsSymbolHigh(s Symbol) bool { return (s >= H1) && (s <= H9) }

// IsSymbolLow 回傳符號是否屬於低分符號。
func IsSymbolLow(s Symbol) bool { return (s >= L1) && (s <= L9) }

type SymbolType int

const (
	SymbolTypeNone = iota
	SymbolTypeSpecial
	SymbolTypeScatter
	SymbolTypeWild
	SymbolTypeHigh
	SymbolTypeLow
)

// GetSymbolType 依符號類別回傳對應的 SymbolType。
func (s Symbol) GetSymbolType() SymbolType {
	if IsSymbolNone(s) {
		return SymbolTypeNone
	}
	if IsSymbolSpecial(s) {
		return SymbolTypeSpecial
	}
	if IsSymbolScatter(s) {
		return SymbolTypeScatter
	}
	if IsSymbolWild(s) {
		return SymbolTypeWild
	}
	if IsSymbolHigh(s) {
		return SymbolTypeHigh
	}
	if IsSymbolLow(s) {
		return SymbolTypeLow
	}
	return SymbolTypeNone
}

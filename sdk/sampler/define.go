// Package sampler 提供一系列高效能的加權抽樣演算法與工具
//
// 本檔案 (define.go) 定義了 sampler 套件中通用的泛型約束 (Generic Constraints)
//
// 目的：
//   - 統一數值型別的定義，支援各類整數與浮點數。
//   - 簡化函數簽章，提升代碼可讀性與復用性。

package sampler

// Integers 定義所有底層實現為整數型別的集合
type Integers interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

// Floaters 定義所有底層實現為浮點數型別的集合
type Floaters interface {
	~float32 | ~float64
}

// Numbers 定義所有底層實現為數值型別的集合（整數與浮點數）
type Numbers interface {
	Integers | Floaters
}

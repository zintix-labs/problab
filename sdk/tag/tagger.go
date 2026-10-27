// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package tag

import (
	"fmt"
	"slices"

	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/spec"
)

// BitMasks 是多段位元遮罩：第 i 個 tag 對應 bm[i/64] 的第 i%64 個位元。
// 段數由編譯出它的 Tagger 決定；最後一段中未使用的位元一律為零。
//
// BitMasks 是 slice：普通賦值會共用底層儲存區，不是複製。需要獨立保存某一刻的
// 結果時，請自行複製內容。
type BitMasks []uint64

// Tagger 是編譯後的標籤判斷器：依位元順序保存名稱與 IsTag。
//
// Tagger 編譯後唯讀、不持有可變的結果緩衝區，可以跨 worker 共用；它不深拷貝 IsTag
// 本身，因此共用時 IsTag 必須能安全地並行呼叫。判斷結果寫入由呼叫端持有的工作遮罩
// （NewMasks），每個 worker 或獨立的執行流程各自持有一份。
//
// 傳給 Tag、Add、Match、Equal、None、ToNames 的遮罩，長度必須等於這個 Tagger 的
// 段數，且只能和同一個 Tagger（或以相同名稱、相同順序編譯的 Tagger）一起使用；段數
// 相同不代表位元語意相同。這些是呼叫端契約，不在熱路徑檢查，違反時行為未定義。
type Tagger struct {
	gid   spec.GID
	names []string
	fns   []IsTag
	index map[string]int
	words int
}

// Op 表示錯誤發生在哪個操作。
type Op uint8

const (
	OpCompile Op = iota + 1 // GameTagCatalog.Compile、CompileBy
	OpWant                  // Tagger.Want
)

// ErrorKind 是編譯或 Want 失敗的分類。
type ErrorKind uint8

const (
	GameNotFound      ErrorKind = iota + 1 // 目錄中沒有該遊戲
	NameNotFound                           // 指定的名稱不存在
	InvalidDefinition                      // 名稱為空字串，或 IsTag 為 nil
)

// CompileError 是 Compile、CompileBy 與 Want 回傳的錯誤；以 errors.As 取得操作、
// 分類、遊戲、名稱與位置。
type CompileError struct {
	Op    Op
	Kind  ErrorKind
	GID   spec.GID
	Name  string // 問題名稱；GameNotFound 時為空
	Index int    // NameNotFound：該名稱在呼叫參數中的位置
}

func (e *CompileError) Error() string {
	switch {
	case e.Kind == GameNotFound:
		return fmt.Sprintf("tags for game %d not found", e.GID)
	case e.Kind == NameNotFound && e.Op == OpWant:
		return fmt.Sprintf("tag not found in tagger: %s", e.Name)
	case e.Kind == NameNotFound:
		return fmt.Sprintf("tag %d not found: %s", e.Index, e.Name)
	case e.Name == "":
		return "tag name is empty"
	default:
		return fmt.Sprintf("tag %q predicate is nil", e.Name)
	}
}

// Compile 編譯該遊戲的全部定義，名稱依字典序排序以固定位元順序；任何一個定義不合法
// 即回傳錯誤，因此也用來驗證該遊戲的全部定義。遊戲不存在時回傳 GameNotFound；
// 定義為空時回傳沒有任何 tag 的 Tagger。編譯後修改目錄不影響 Tagger。
func (c GameTagCatalog) Compile(gid spec.GID) (*Tagger, error) {
	defs, ok := c[gid]
	if !ok {
		return nil, &CompileError{Op: OpCompile, Kind: GameNotFound, GID: gid}
	}
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	slices.Sort(names)
	fns := make([]IsTag, len(names))
	for i, name := range names {
		if name == "" || defs[name] == nil {
			return nil, &CompileError{Op: OpCompile, Kind: InvalidDefinition, GID: gid, Name: name}
		}
		fns[i] = defs[name]
	}
	return newTagger(gid, names, fns), nil
}

// CompileBy 只編譯指定的名稱，依參數順序回報第一個問題；重複的名稱去重，位元順序以
// 第一次出現為準。只驗證被指定名稱的定義。遊戲不存在時回傳 GameNotFound；沒有名稱時
// 回傳沒有任何 tag 的 Tagger。編譯後修改目錄不影響 Tagger。
func (c GameTagCatalog) CompileBy(gid spec.GID, names ...string) (*Tagger, error) {
	defs, ok := c[gid]
	if !ok {
		return nil, &CompileError{Op: OpCompile, Kind: GameNotFound, GID: gid}
	}
	seen := make(map[string]bool, len(names))
	unique := make([]string, 0, len(names))
	fns := make([]IsTag, 0, len(names))
	for i, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		fn, exists := defs[name]
		if !exists {
			return nil, &CompileError{Op: OpCompile, Kind: NameNotFound, GID: gid, Name: name, Index: i}
		}
		if name == "" || fn == nil {
			return nil, &CompileError{Op: OpCompile, Kind: InvalidDefinition, GID: gid, Name: name}
		}
		unique = append(unique, name)
		fns = append(fns, fn)
	}
	return newTagger(gid, unique, fns), nil
}

func newTagger(gid spec.GID, names []string, fns []IsTag) *Tagger {
	index := make(map[string]int, len(names))
	for i, name := range names {
		index[name] = i
	}
	return &Tagger{gid: gid, names: names, fns: fns, index: index, words: (len(names) + 63) / 64}
}

// Len 回傳 tag 數；遮罩的長度（段數）是 ⌈Len ÷ 64⌉。
func (t *Tagger) Len() int { return len(t.names) }

// NewMasks 每次建立一份獨立、可寫的工作遮罩，長度等於段數。
func (t *Tagger) NewMasks() BitMasks { return make(BitMasks, t.words) }

// Want 在準備期把名稱編譯成唯讀的條件遮罩：重複的名稱視為同一項；不在這個 Tagger
// 中的名稱回傳錯誤；不帶名稱時回傳全零的條件。條件遮罩不與任何工作遮罩共用儲存區。
func (t *Tagger) Want(names ...string) (BitMasks, error) {
	want := make(BitMasks, t.words)
	for i, name := range names {
		bit, ok := t.index[name]
		if !ok {
			return nil, &CompileError{Op: OpWant, Kind: NameNotFound, GID: t.gid, Name: name, Index: i}
		}
		want[bit>>6] |= 1 << (bit & 63)
	}
	return want, nil
}

// Tag 先清空 bm，再依位元順序呼叫每個 tag 一次，寫入命中的位元（新的一局）。不配置記憶體。
func (t *Tagger) Tag(bm BitMasks, sr *buf.SpinResult) {
	clear(bm)
	t.Add(bm, sr)
}

// Add 依位元順序呼叫每個 tag 一次，只把命中的位元加進 bm，不清除既有位元（累積聯集）。
// 不配置記憶體。
func (t *Tagger) Add(bm BitMasks, sr *buf.SpinResult) {
	for i, fn := range t.fns {
		if fn(sr) {
			bm[i>>6] |= 1 << (i & 63)
		}
	}
}

// Match 回報 bm 是否包含 want 的全部位元（允許額外命中）。全零的條件恆為真。
func (t *Tagger) Match(bm, want BitMasks) bool {
	for w := range want {
		if bm[w]&want[w] != want[w] {
			return false
		}
	}
	return true
}

// Equal 回報 bm 與 want 的每一段是否完全相等。
func (t *Tagger) Equal(bm, want BitMasks) bool {
	for w := range want {
		if bm[w] != want[w] {
			return false
		}
	}
	return true
}

// None 回報 bm 與 want 是否沒有任何共同的命中位元。全零的條件恆為真。
func (t *Tagger) None(bm, want BitMasks) bool {
	for w := range want {
		if bm[w]&want[w] != 0 {
			return false
		}
	}
	return true
}

// ToNames 依位元順序回傳 bm 中命中的名稱；每次配置新的 slice，供顯示與除錯使用。
func (t *Tagger) ToNames(bm BitMasks) []string {
	out := []string{}
	for i, name := range t.names {
		if bm[i>>6]&(1<<(i&63)) != 0 {
			out = append(out, name)
		}
	}
	return out
}

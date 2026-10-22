// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

// Package tag compiles named result predicates into immutable bit assignments.
package tag

import (
	"fmt"

	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/spec"
)

// GameTagCatalog groups named predicates by game ID; it is the only entry point
// for resolving tags. Names are game-scoped; the catalog does not register tags
// automatically. Applications assemble it and inject it into consumers, which
// compile the names they use into a Tagger with Compile or CompileBy.
// Do not mutate its maps while consumers are compiling from it.
type GameTagCatalog map[spec.GID]map[string]IsTag

// IsTag 判斷一份借用的結果是否帶有某個標籤。契約：
//   - 只根據傳入的結果判斷，不依賴呼叫次數；
//   - 不修改結果，也不修改 RNG 或其他執行與分析狀態；
//   - 必須能對所屬遊戲的每一份合法單段結果執行，不依賴事件範圍、贏分區間或其他
//     tag 先成立（消費端可能對每一份結果都判斷全部引用的 tag）；
//   - 編譯出的 Tagger 被多個 worker 共用時，必須能安全地並行呼叫。
type IsTag func(sr *buf.SpinResult) bool

// Registry owns its definitions. Its zero value is usable. Registration must
// not overlap registration or BitSet compilation; compiled BitSets are immutable
// and independent of later registration. Copying a Registry value is not cloning.
type Registry struct{ fns map[string]IsTag }

// NewRegistry validates and copies definitions. Nil creates an empty registry.
// Predicate closures themselves are not cloned.
func NewRegistry(tagMap map[string]IsTag) (*Registry, error) {
	r := &Registry{}
	if err := r.RegisterTag(tagMap); err != nil {
		return nil, err
	}
	return r, nil
}

// RegisterTag 在Registry 中追加tags，會先檢查是否有重複，有問題會中斷先回error。都沒問題才追加
func (r *Registry) RegisterTag(tagMap map[string]IsTag) error {
	for s, fn := range tagMap {
		if s == "" {
			return fmt.Errorf("tag name is empty")
		}
		if fn == nil {
			return fmt.Errorf("tag %q predicate is nil", s)
		}
		if _, ok := r.fns[s]; ok {
			return fmt.Errorf("duplicate tag name: %s", s)
		}
	}
	if r.fns == nil {
		r.fns = make(map[string]IsTag, len(tagMap))
	}
	for s, f := range tagMap {
		r.fns[s] = f
	}
	return nil
}

// BitSet 凍結一次收集的位元指派：bit i 永遠對應 names[i]。
// 函式在此一次解析完成，熱路徑不再查表。
type BitSet struct {
	names []string
	fns   []IsTag
}

// BitSet resolves 1..64 distinct names in the supplied order and owns a copy of
// that order. Later registry additions cannot change its predicates or masks.
func (r *Registry) BitSet(names ...string) (*BitSet, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("tags are required")
	}
	if len(names) > 64 {
		return nil, fmt.Errorf("tags must be at most 64: %d given", len(names))
	}
	seen := make(map[string]bool, len(names))
	for i, n := range names {
		if seen[n] {
			return nil, fmt.Errorf("duplicate tag name: %s", n)
		}
		seen[n] = true
		if _, ok := r.fns[n]; !ok {
			return nil, fmt.Errorf("tag %d not found: %s", i, n)
		}
	}
	bs := &BitSet{
		names: append([]string(nil), names...),
		fns:   make([]IsTag, 0, len(names)),
	}
	for _, n := range names {
		fn := r.fns[n]
		bs.fns = append(bs.fns, fn)
	}
	return bs, nil
}

// Tagging 回傳這一局命中的標籤位元集合
func (bs *BitSet) Tagging(sr *buf.SpinResult) uint64 {
	u := uint64(0)
	for i, fn := range bs.fns {
		if fn(sr) {
			u |= 1 << i
		}
	}
	return u
}

// Mask rejects unknown names instead of silently dropping a filter. No names
// means no restriction and returns zero. Repeated requested names are harmless.
func (bs *BitSet) Mask(names ...string) (uint64, error) {
	u := uint64(0)
	for _, s := range names {
		found := false
		for i, n := range bs.names {
			if n == s {
				u |= 1 << i
				found = true
				break
			}
		}
		if !found {
			return 0, fmt.Errorf("tag not found in bitset: %s", s)
		}
	}
	return u, nil
}

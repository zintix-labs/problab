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

package optimizer

import (
	"github.com/zintix-labs/problab/sdk/buf"
)

type IsTag func(sr *buf.SpinResult) bool

type tagFn func(sr *buf.SpinResult) (string, bool)

var tagers = map[string]tagFn{
	"bg": IsOnlyBg,
	"fg": IsEntryFree,
}

func IsOnlyBg(sr *buf.SpinResult) (string, bool) {
	if sr.GameModeCount == 1 {
		return "bg", true
	}
	return "", false
}

func IsEntryFree(sr *buf.SpinResult) (string, bool) {
	if sr.GameModeCount > 1 && (sr.GameModeList[0].TotalWin/sr.Bet) < 4 && ((sr.TotalWin-sr.GameModeList[0].TotalWin)/sr.Bet) > 6 {
		return "fg", true
	}
	return "", false
}

func RegisterTager(tag string, isTag IsTag) bool {
	if _, ok := tagers[tag]; !ok {
		tagers[tag] = func(sr *buf.SpinResult) (string, bool) {
			if isTag(sr) {
				return tag, true
			}
			return "", false
		}
		return true
	}
	return false
}

type Tagers struct {
	tags []tagFn
}

func GetTager(tags ...string) *Tagers {
	if len(tags) == 0 {
		return &Tagers{}
	}
	m := make(map[string]struct{})
	for _, t := range tags {
		m[t] = struct{}{}
	}
	result := &Tagers{
		tags: make([]tagFn, 0, len(m)),
	}
	for t := range m {
		if fn, ok := tagers[t]; ok {
			result.tags = append(result.tags, fn)
		}
	}
	return result
}

// TagInto 將符合的 tags 寫入 dst（會先清空長度），用於在熱路徑避免分配。
// 回傳值為同一個底層陣列的 slice（可能與 dst 相同）。
func (t *Tagers) TagInto(sr *buf.SpinResult, dst []string) []string {
	if t == nil || len(t.tags) == 0 {
		return dst[:0]
	}
	r := dst[:0]
	for _, fn := range t.tags {
		if s, ok := fn(sr); ok {
			r = append(r, s)
		}
	}
	return r
}

func sub(check, tester []string) bool {
	if len(check) > len(tester) {
		return false
	}
	for _, s := range check {
		ok := false
		for _, t := range tester {
			if s == t {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

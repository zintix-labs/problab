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
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/buf"
)

const (
	bg = "bg"
	fg = "fg"
)

type IsTag func(sr *buf.SpinResult) bool

type tagFn func(sr *buf.SpinResult) (string, bool)

var tagFns = map[string]tagFn{
	bg: IsOnlyBg,
	fg: IsEntryFree,
}

func IsOnlyBg(sr *buf.SpinResult) (string, bool) {
	if sr.GameModeCount == 1 {
		return bg, true
	}
	return "", false
}

func IsEntryFree(sr *buf.SpinResult) (string, bool) {
	if sr.GameModeCount > 1 && ((sr.TotalWin-sr.GameModeList[0].TotalWin)/sr.Bet) > 5 {
		return fg, true
	}
	return "", false
}

func RegisterTag(tag string, isTag IsTag) bool {
	if _, ok := tagFns[tag]; !ok {
		tagFns[tag] = func(sr *buf.SpinResult) (string, bool) {
			if isTag(sr) {
				return tag, true
			}
			return "", false
		}
		return true
	}
	return false
}

type Tagger struct {
	tags []string
	fns  []tagFn
}

func GetTagger(tags ...string) (*Tagger, error) {
	if len(tags) == 0 {
		return nil, errs.Warnf("tags is required")
	}
	if len(tags) > 64 {
		return nil, errs.Warnf("tags must be less than 64: %d given", len(tags))
	}
	tg := &Tagger{
		tags: tags,
		fns:  make([]tagFn, 0, len(tags)),
	}
	for i, t := range tags {
		if fn, ok := tagFns[t]; ok {
			tg.fns = append(tg.fns, fn)
		} else {
			return nil, errs.Warnf("tag %d not found: %s", i, t)
		}
	}
	return tg, nil
}

func (t *Tagger) Tagging(sr *buf.SpinResult) uint64 {
	u := uint64(0)
	for i, fn := range t.fns {
		if _, ok := fn(sr); ok {
			u |= 1 << i
		}
	}
	return u
}

func (t *Tagger) Mask(tags ...string) uint64 {
	u := uint64(0)
	for _, s := range tags {
		for i, g := range t.tags {
			if g == s {
				u |= 1 << i
				break
			}
		}
	}
	return u
}

func (t *Tagger) IsCover(now uint64, fit uint64) bool {
	return (now & fit) == fit
}

func (t *Tagger) IsEqual(now uint64, fit uint64) bool {
	return (now == fit)
}

func (t *Tagger) IsAnyMatch(now uint64, fit uint64) bool {
	return ((now & fit) != 0)
}

func (t *Tagger) IsNoMatch(now uint64, fit uint64) bool {
	return !(t.IsAnyMatch(now, fit))
}

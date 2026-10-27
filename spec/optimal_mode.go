// Copyright 2026 Zintix Labs
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
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"gopkg.in/yaml.v3"
)

// OptimalMode 是一個 bet mode 的 optimal 起點來源。Start 是初始段（Cycle 0）的庫；After 以
// 入口值（ChooseNext[Choice]）為鍵，是這個 bet mode 共用的續段庫，不描述流程。開啟 optimal
// 時，遊戲提供的每個入口都必須列在 After 中；沒有列出的入口會被拒絕。
//
// 遊戲設定整體以寬鬆方式解析，這個區塊例外：它自己檢查每個鍵，拼錯或多寫都是錯誤。
type OptimalMode struct {
	Start *GachaSlot         `yaml:"start"           json:"start"`
	After map[int]*GachaSlot `yaml:"after,omitempty" json:"after,omitempty"`
}

// OptimalModes 是 optimal_setting.modes：modes[i] 對應 bet mode i。
type OptimalModes []OptimalMode

// UnmarshalYAML 拒絕寫成 null 的元素。yaml.v3 會直接略過序列中的 null 元素，後面的 mode
// 因此往前移、對到別的 bet mode。
func (ms *OptimalModes) UnmarshalYAML(node *yaml.Node) error {
	node = yamlTarget(node)
	if node.Kind != yaml.SequenceNode {
		return fmt.Errorf("line %d: optimal_setting.modes: must be a list with one entry per bet mode", node.Line)
	}
	modes := make(OptimalModes, len(node.Content))
	for i, item := range node.Content {
		item = yamlTarget(item)
		if yamlNull(item) {
			return fmt.Errorf("line %d: optimal_setting.modes[%d]: must not be null", item.Line, i)
		}
		if err := modes[i].UnmarshalYAML(item); err != nil {
			return err
		}
	}
	*ms = modes
	return nil
}

// GachaSlot 是庫的位置。Dist 與 Bank 都有值時從這一對抽 seed；都為空時這一段照常使用 RNG
// （設定中寫成 {dist: null, bank: null}）。路徑的解析方式與 gachas／seed_bank 相同。
type GachaSlot struct {
	Dist string
	Bank string
}

// Seeded 回報這個位置是否抽庫。
func (s GachaSlot) Seeded() bool { return s.Dist != "" }

func (s GachaSlot) valid() error {
	if (s.Dist == "") != (s.Bank == "") {
		return fmt.Errorf("dist and bank must both be paths or both be null")
	}
	return nil
}

const slotHint = "write {dist: null, bank: null} to use the machine RNG"

// ============================================================
// ** YAML **
// ============================================================

func (m *OptimalMode) UnmarshalYAML(node *yaml.Node) error {
	node = yamlTarget(node)
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: optimal_setting.modes[]: must be a mapping with start and after", node.Line)
	}
	var mode OptimalMode
	seen := make(map[string]bool, 2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], yamlTarget(node.Content[i+1])
		if seen[key.Value] {
			return fmt.Errorf("line %d: optimal_setting.modes[]: duplicate key %q", key.Line, key.Value)
		}
		seen[key.Value] = true
		switch key.Value {
		case "start":
			if yamlNull(value) {
				return fmt.Errorf("line %d: optimal_setting.modes[].start: must not be null; %s", value.Line, slotHint)
			}
			slot := new(GachaSlot)
			if err := slot.UnmarshalYAML(value); err != nil {
				return err
			}
			mode.Start = slot
		case "after":
			after, err := decodeAfterYAML(value)
			if err != nil {
				return err
			}
			mode.After = after
		default:
			return fmt.Errorf("line %d: optimal_setting.modes[]: unknown key %q (want start and after)", key.Line, key.Value)
		}
	}
	if mode.Start == nil {
		return fmt.Errorf("line %d: optimal_setting.modes[].start: is required; %s", node.Line, slotHint)
	}
	*m = mode
	return nil
}

func decodeAfterYAML(node *yaml.Node) (map[int]*GachaSlot, error) {
	if yamlNull(node) {
		return nil, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("line %d: optimal_setting.modes[].after: must be a mapping from entry values to gacha slots", node.Line)
	}
	after := make(map[int]*GachaSlot, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := yamlTarget(node.Content[i]), yamlTarget(node.Content[i+1])
		if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!int" {
			return nil, fmt.Errorf("line %d: optimal_setting.modes[].after: key %q must be an integer entry value", key.Line, key.Value)
		}
		var entry int
		if err := key.Decode(&entry); err != nil {
			return nil, fmt.Errorf("line %d: optimal_setting.modes[].after: %w", key.Line, err)
		}
		if _, dup := after[entry]; dup {
			return nil, fmt.Errorf("line %d: optimal_setting.modes[].after: duplicate entry %d", key.Line, entry)
		}
		if yamlNull(value) {
			return nil, fmt.Errorf("line %d: optimal_setting.modes[].after[%d]: must not be null; %s", value.Line, entry, slotHint)
		}
		slot := new(GachaSlot)
		if err := slot.UnmarshalYAML(value); err != nil {
			return nil, err
		}
		after[entry] = slot
	}
	return after, nil
}

func (s *GachaSlot) UnmarshalYAML(node *yaml.Node) error {
	node = yamlTarget(node)
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: optimal gacha slot: must be a mapping with dist and bank; %s", node.Line, slotHint)
	}
	values := make(map[string]*yaml.Node, 2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Value != "dist" && key.Value != "bank" {
			return fmt.Errorf("line %d: optimal gacha slot: unknown key %q (want dist and bank)", key.Line, key.Value)
		}
		if values[key.Value] != nil {
			return fmt.Errorf("line %d: optimal gacha slot: duplicate key %q", key.Line, key.Value)
		}
		values[key.Value] = yamlTarget(node.Content[i+1])
	}
	dist, distNull, err := yamlSlotPath(values["dist"])
	if err != nil {
		return fmt.Errorf("line %d: optimal gacha slot dist: %w", node.Line, err)
	}
	bank, bankNull, err := yamlSlotPath(values["bank"])
	if err != nil {
		return fmt.Errorf("line %d: optimal gacha slot bank: %w", node.Line, err)
	}
	if distNull != bankNull {
		return fmt.Errorf("line %d: optimal gacha slot: dist and bank must both be paths or both be null", node.Line)
	}
	*s = GachaSlot{Dist: dist, Bank: bank}
	return nil
}

// yamlSlotPath 讀取 dist 或 bank：null 回傳 isNull；非空字串回傳路徑；其餘是錯誤。
func yamlSlotPath(node *yaml.Node) (path string, isNull bool, err error) {
	switch {
	case node == nil:
		return "", false, fmt.Errorf("is required; %s", slotHint)
	case yamlNull(node):
		return "", true, nil
	case node.Kind != yaml.ScalarNode || node.ShortTag() != "!!str" || node.Value == "":
		return "", false, fmt.Errorf("must be a non-empty path or null")
	}
	return node.Value, false, nil
}

func (s GachaSlot) MarshalYAML() (any, error) {
	if !s.Seeded() {
		return map[string]any{"dist": nil, "bank": nil}, nil
	}
	return map[string]any{"dist": s.Dist, "bank": s.Bank}, nil
}

func yamlTarget(node *yaml.Node) *yaml.Node {
	for node != nil && node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	return node
}

func yamlNull(node *yaml.Node) bool {
	return node.Kind == yaml.ScalarNode && node.ShortTag() == "!!null"
}

// ============================================================
// ** JSON **
// ============================================================

type jsonField struct {
	key   string
	value json.RawMessage
}

// jsonObject 依序讀出 JSON object 的欄位，並拒絕重複的鍵（encoding/json 會讓後者覆蓋前者）。
func jsonObject(raw []byte, what string) ([]jsonField, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("%s: must be an object", what)
	}
	var fields []jsonField
	seen := make(map[string]bool)
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		key := tok.(string)
		if seen[key] {
			return nil, fmt.Errorf("%s: duplicate key %q", what, key)
		}
		seen[key] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		fields = append(fields, jsonField{key, value})
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return fields, nil
}

func jsonNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func (m *OptimalMode) UnmarshalJSON(raw []byte) error {
	if jsonNull(raw) {
		return fmt.Errorf("optimal_setting.modes[]: must not be null")
	}
	fields, err := jsonObject(raw, "optimal_setting.modes[]")
	if err != nil {
		return err
	}
	var mode OptimalMode
	for _, f := range fields {
		switch f.key {
		case "start":
			if jsonNull(f.value) {
				return fmt.Errorf("optimal_setting.modes[].start: must not be null; %s", slotHint)
			}
			slot := new(GachaSlot)
			if err := slot.UnmarshalJSON(f.value); err != nil {
				return err
			}
			mode.Start = slot
		case "after":
			if jsonNull(f.value) {
				continue
			}
			entries, err := jsonObject(f.value, "optimal_setting.modes[].after")
			if err != nil {
				return err
			}
			mode.After = make(map[int]*GachaSlot, len(entries))
			for _, e := range entries {
				entry, err := strconv.Atoi(e.key)
				if err != nil {
					return fmt.Errorf("optimal_setting.modes[].after: key %q must be an integer entry value", e.key)
				}
				if _, dup := mode.After[entry]; dup {
					return fmt.Errorf("optimal_setting.modes[].after: duplicate entry %d", entry)
				}
				if jsonNull(e.value) {
					return fmt.Errorf("optimal_setting.modes[].after[%d]: must not be null; %s", entry, slotHint)
				}
				slot := new(GachaSlot)
				if err := slot.UnmarshalJSON(e.value); err != nil {
					return err
				}
				mode.After[entry] = slot
			}
		default:
			return fmt.Errorf("optimal_setting.modes[]: unknown key %q (want start and after)", f.key)
		}
	}
	if mode.Start == nil {
		return fmt.Errorf("optimal_setting.modes[].start: is required; %s", slotHint)
	}
	*m = mode
	return nil
}

func (s *GachaSlot) UnmarshalJSON(raw []byte) error {
	fields, err := jsonObject(raw, "optimal gacha slot")
	if err != nil {
		return err
	}
	values := make(map[string]json.RawMessage, 2)
	for _, f := range fields {
		if f.key != "dist" && f.key != "bank" {
			return fmt.Errorf("optimal gacha slot: unknown key %q (want dist and bank)", f.key)
		}
		values[f.key] = f.value
	}
	dist, distNull, err := jsonSlotPath(values["dist"])
	if err != nil {
		return fmt.Errorf("optimal gacha slot dist: %w", err)
	}
	bank, bankNull, err := jsonSlotPath(values["bank"])
	if err != nil {
		return fmt.Errorf("optimal gacha slot bank: %w", err)
	}
	if distNull != bankNull {
		return fmt.Errorf("optimal gacha slot: dist and bank must both be paths or both be null")
	}
	*s = GachaSlot{Dist: dist, Bank: bank}
	return nil
}

func jsonSlotPath(raw json.RawMessage) (path string, isNull bool, err error) {
	switch {
	case raw == nil:
		return "", false, fmt.Errorf("is required; %s", slotHint)
	case jsonNull(raw):
		return "", true, nil
	}
	if err := json.Unmarshal(raw, &path); err != nil || path == "" {
		return "", false, fmt.Errorf("must be a non-empty path or null")
	}
	return path, false, nil
}

func (s GachaSlot) MarshalJSON() ([]byte, error) {
	if !s.Seeded() {
		return []byte(`{"dist":null,"bank":null}`), nil
	}
	return json.Marshal(struct {
		Dist string `json:"dist"`
		Bank string `json:"bank"`
	}{s.Dist, s.Bank})
}

// ============================================================
// ** 驗證 **
// ============================================================

// validModes 檢查以程式建構、未經解碼的設定：每個 mode 都有 start，每個位置都不是 nil，
// 且 dist 與 bank 同時有值或同時為空。
func validModes(modes OptimalModes) error {
	for i, mode := range modes {
		if mode.Start == nil {
			return fmt.Errorf("optimal_setting.modes[%d].start: is required; %s", i, slotHint)
		}
		if err := mode.Start.valid(); err != nil {
			return fmt.Errorf("optimal_setting.modes[%d].start: %w", i, err)
		}
		entries := make([]int, 0, len(mode.After))
		for entry := range mode.After {
			entries = append(entries, entry)
		}
		sort.Ints(entries)
		for _, entry := range entries {
			slot := mode.After[entry]
			if slot == nil {
				return fmt.Errorf("optimal_setting.modes[%d].after[%d]: must not be nil; %s", i, entry, slotHint)
			}
			if err := slot.valid(); err != nil {
				return fmt.Errorf("optimal_setting.modes[%d].after[%d]: %w", i, entry, err)
			}
		}
	}
	return nil
}

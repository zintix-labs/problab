package spec

import (
	"bytes"

	"github.com/zintix-labs/problab/errs"
	"gopkg.in/yaml.v3"
)

// DecodeFixed 會把 gs.Fixed 由 map[string]any 轉成你要的型別 T。
// T 應該是 struct 指標，例如 *MyGameFixed。
func DecodeFixed[T any](gs *GameSetting, out *T) error {
	// 先把 map[string]any -> YAML bytes
	bs, err := yaml.Marshal(gs.Fixed)
	if err != nil {
		return errs.Wrap(err, "spec.fixed_decoder : marshal failed")
	}
	// 再把 YAML bytes -> 自定義的型別
	dec := yaml.NewDecoder(bytes.NewReader(bs))
	dec.KnownFields(true) // 嚴格檢查：多寫/拼錯欄位就報錯
	if err = dec.Decode(out); err != nil {
		return errs.Wrap(err, "spec.fixed_decoder : decode failed")
	}
	return nil
}

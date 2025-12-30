package dto

import (
	"reflect"

	"github.com/zintix-labs/problab/spec"
)

var extendRenders = map[spec.LogicKey]func(any) any{}

// RegisterExtendRender 註冊遊戲 Extend 結果解析函數。
// T為該遊戲Extend結果的 型別指標 (不傳指標會panic)
func RegisterExtendRender[T any](lkey spec.LogicKey) {
	// 判斷送進來的T是否是指標型別
	var zero T
	rt := reflect.TypeOf(zero)
	if rt == nil || rt.Kind() != reflect.Ptr {
		panic("RegisterExtendRender 必須傳入 指標型別")
	}

	// 註冊extend型別json解析
	extendRenders[lkey] = func(v any) any {
		if val, ok := v.(T); ok {
			return val
		}
		return v
	}
}

func renderExtendResult(lkey spec.LogicKey, v any) any {
	if fn, ok := extendRenders[lkey]; ok {
		return fn(v)
	}
	return v
}

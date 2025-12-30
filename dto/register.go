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

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

package ops

import "github.com/zintix-labs/problab/sdk/layer"

// validateLayers 在任何寫入前檢查長度。nil 或長度不符直接 panic。
// 操作入口不提供並行安全或交易回滾；其餘參數須符合原操作契約。
// Layer 不可重複、彼此共用儲存，或與 screen 共用儲存；不在熱路徑檢查別名。
func validateLayers(screen []int16, layers []layer.SyncOps) {
	for _, lr := range layers {
		if lr == nil || lr.Len() != len(screen) {
			panic("ops: layer length differs from screen or layer is nil")
		}
	}
}

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

package ops

import "github.com/zintix-labs/problab/sdk/layer"

// Clear 消除標記位置的圖標(改為0)
// 負值格保持不變；忽略越界的 hitmap 索引。
//
//   - screen: 盤面數據 (將被原地修改)
//   - hitmap: 消除位置 (這些位置會被標記為 0)
//
// 可選 layers 將實際清除格重置為各自的預設值；未傳入時只操作 screen。
// 負值格與非法 hitmap 索引不影響 Layer。
// Layer 須無別名（見 layer.Layer）；長度不符或 nil 在寫入前 panic。
// 不提供並行安全，也不保證自訂 Layer 方法 panic 時回滾。
func Clear(screen []int16, hitmap []int16, layers ...layer.SyncOps) {
	validateLayers(screen, layers)
	for _, v := range hitmap {
		if v >= 0 && int(v) < len(screen) && screen[v] >= 0 {
			screen[v] = 0
			if len(layers) != 0 {
				for _, lr := range layers {
					lr.ResetIdx(int(v))
				}
			}
		}
	}
}

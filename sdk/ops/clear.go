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

// Clear 消除標記位置的圖標(改為0)
//
//   - screen: 盤面數據 (將被原地修改)
//   - hitmap: 消除位置 (這些位置會被標記為 0)
func Clear(screen []int16, hitmap []int16) {
	for _, v := range hitmap {
		if v < int16(len(screen)) { // 簡單防禦
			screen[v] = 0
		}
	}
}

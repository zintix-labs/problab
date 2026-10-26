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

package buf

import "github.com/zintix-labs/problab/spec"

// SpinRequest 描述一次投注請求。
// BetUnits[0] 是基礎投注單位，BetUnits[BetMode] 是所選模式的投注單位。
// 本次基礎投注額 = BetUnits[0] * BetMult。
// 本次實際投注額 = BetUnits[BetMode] * BetMult。
// 購買模式的價格倍率 = BetUnits[BetMode] / BetUnits[0] （數學比值，非整數除法）。
// 賠率表與 max_win_limit 以 BetMult=1 的金額定義（對應基礎投注額）
// 算分時乘 BetMult，不乘購買模式的價格倍率。
//
// 交給遊戲的 SpinRequest 由 Machine 持有，並在每次執行前重設；遊戲只讀，且不得保留
// 它或其內含參照。
//
// Machine 在呼叫遊戲前保證結構不變量：Cycle >= 0；Cycle 為 0 時 ChooseNext 為空、
// Choice 為 0；Cycle > 0 時 ChooseNext 非空且 0 <= Choice < len(ChooseNext)。因此續段
// 分支可以直接讀取 ChooseNext[Choice]。入口值與 Checkpoint 是否符合本遊戲此時的狀態，
// 由遊戲自行判斷，不合法時回傳 nil。RNG 快照只供引擎選擇起點，不交給遊戲。
type SpinRequest struct {
	GameName   string   // 要玩的遊戲
	GameId     spec.GID // 遊戲機台編號
	Bet        int      // 本次實際投注額，等於 BetUnits[BetMode] * BetMult；續段沿用第 0 段，不表示再次扣款。
	BetMode    int      // 投注模式索引，對應 BetUnits。
	BetMult    int      // 投注倍數，不包含購買模式的價格倍率。
	Cycle      int      // 第幾段，0 為新局
	Choice     int      // ChooseNext 的位置索引；Cycle 為 0 時為 0
	ChooseNext []int    // 上一段結果的選項（入口值）；Cycle 為 0 時為空。由 Machine 複製
	Checkpoint any      // 上一段結果輸出的 checkpoint；唯讀，不得保留參照
}

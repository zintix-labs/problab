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
type SpinRequest struct {
	UID        string   // 唯一識別碼
	GameName   string   // 要玩的遊戲
	GameId     spec.GID // 遊戲機台編號
	Bet        int      // 呼叫端提供的本次實際投注額，應符合 BetUnits[BetMode] * BetMult ；續局沿用，不表示再次扣款。
	BetMode    int      // 投注模式索引，對應 BetUnits。
	BetMult    int      // 投注倍數，不包含購買模式的價格倍率。
	Cycle      int      // 第幾段會話
	Choice     int      // 玩家在本段（cycle）所做的選擇值（允許為 0）。
	HasChoice  bool     // 是否有「提供選擇」。
	StartState *StartState
}

type StartState struct {
	StartCoreSnap []byte
	Checkpoint    any
}

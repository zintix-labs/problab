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

type SpinRequest struct {
	UID        string   // 唯一識別碼
	GameName   string   // 要玩的遊戲
	GameId     spec.GID // 遊戲機台編號
	Bet        int      // 投注額
	BetMode    int      // 投注模式(走BetUnit[i])
	BetMult    int      // 投注倍數(BetUnit[0]的幾倍)
	Cycle      int      // 第幾段會話
	Choice     int      // 玩家在本段（cycle）所做的選擇值（允許為 0）。
	HasChoice  bool     // 是否有「提供選擇」。
	StartState *StartState
}

type StartState struct {
	StartCoreSnap []byte
	Checkpoint    any
}

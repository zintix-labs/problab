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

package problab

import (
	"fmt"
	"testing"

	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/slot"
)

// stickyStateLogic 包住 demo 邏輯，但每次都把結果的 State 換成同一個物件，且不清除
// 其中的 RNG 快照，並在 Checkpoint 寫入呼叫次數。用來驗證 CaptureSnapshots 為 false
// 時，Machine 只清除前一次留下的 RNG 快照，不碰遊戲輸出的 Checkpoint。
type stickyStateLogic struct {
	inner slot.GameLogic
	state buf.SpinState
	calls int
}

func (l *stickyStateLogic) GetResult(r *buf.SpinRequest, g *slot.Game) *buf.SpinResult {
	sr := l.inner.GetResult(r, g)
	l.calls++
	l.state.Checkpoint = l.calls
	sr.State = &l.state
	return sr
}

func withStickyStateLogic(t testing.TB, m *Machine) *stickyStateLogic {
	t.Helper()
	key := m.gh.GameSetting.LogicKey
	logic := &stickyStateLogic{}
	reg := slot.NewLogicRegistry()
	if err := reg.Register(key, func(g *slot.Game) (slot.GameLogic, error) {
		inner, err := demo_logic.Logics.Build(key, g)
		logic.inner = inner
		return logic, err
	}); err != nil {
		t.Fatal(err)
	}
	gh, err := slot.NewGame(m.gh.GameSetting, reg, m.core, m.gh.OmitExtend())
	if err != nil {
		t.Fatal(err)
	}
	m.gh = gh
	return logic
}

func TestSpinUnsafeWithoutCaptureClearsOnlyRNGSnapshots(t *testing.T) {
	for _, optimal := range []bool{false, true} {
		t.Run(fmt.Sprint("optimal=", optimal), func(t *testing.T) {
			m := faultMachine(t, optimal)
			logic := withStickyStateLogic(t, m)

			captured := m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1, CaptureSnapshots: true})
			if captured.State != &logic.state || len(logic.state.StartCoreSnap) == 0 || len(logic.state.AfterCoreSnap) == 0 {
				t.Fatal("capturing call did not write snapshots into the game's State")
			}

			plain := m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1})
			if plain.State != &logic.state {
				t.Fatal("result State was replaced")
			}
			if plain.State.StartCoreSnap != nil || plain.State.AfterCoreSnap != nil {
				t.Fatal("non-capturing call kept the previous RNG snapshots")
			}
			if plain.State.Checkpoint != 2 {
				t.Fatalf("non-capturing call changed the game's Checkpoint: %v", plain.State.Checkpoint)
			}
		})
	}
}

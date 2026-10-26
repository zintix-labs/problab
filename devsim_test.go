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
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/zintix-labs/problab/corefmt"
	"github.com/zintix-labs/problab/sdk/core"
)

func devSimulator(t *testing.T, lab *Problab, seed int64) *DevSimulator {
	t.Helper()
	d, err := lab.NewDevSimulator(0, seed)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// 報告的統計與每局結果一致：BG 為第一個 GameMode 的贏分，FG 為其餘（TotalWin-BaseWin）。
func TestDevSpinsTotals(t *testing.T) {
	rep, err := devSimulator(t, faultLab(t, core.Default(), false), 77).Spins(0, 2000)
	if err != nil {
		t.Fatal(err)
	}
	bet, win, base := 0, 0, 0
	for _, r := range rep.Results {
		bet += r.Bet
		win += r.TotalWin
		base += r.GameModes[0].TotalWin
	}
	if rep.Round != len(rep.Results) || rep.TotalBet != bet || rep.TotalWin != win || rep.BaseWin != base {
		t.Fatalf("report %d/%d/%d/%d, results %d/%d/%d/%d", rep.Round, rep.TotalBet, rep.TotalWin, rep.BaseWin, len(rep.Results), bet, win, base)
	}
	if rep.FreeWin != win-base || rep.FreeWin == 0 {
		t.Fatalf("free_win %d, want %d (the batch must contain free games)", rep.FreeWin, win-base)
	}
	if want := 100.0 * float64(win) / float64(bet); rep.Rtp != want {
		t.Fatalf("rtp %v, want %v", rep.Rtp, want)
	}
}

// Before／After 是機台在整批前後的位置：以 Before 重跑得到同一批結果與同一個 After。
// 開啟 optimal 時，每局的起始快照是抽出的 seed，不是機台位置，因此要分開驗證。
func TestDevRestoreSpinsReplaysBatch(t *testing.T) {
	for _, optimal := range []bool{false, true} {
		lab := faultLab(t, core.Default(), optimal)
		d := devSimulator(t, lab, 77)
		start := corefmt.EncodeBase64URL(coreState(t, d.m))
		rep, err := d.Spins(0, 300)
		if err != nil {
			t.Fatal(err)
		}
		if rep.Before != start || rep.After != corefmt.EncodeBase64URL(coreState(t, d.m)) {
			t.Fatalf("optimal %v: before/after are not the machine positions around the batch", optimal)
		}
		again, err := devSimulator(t, lab, 78).RestoreSpins(rep.Before, 0, 300)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(again, rep) {
			t.Fatalf("optimal %v: RestoreSpins(before) does not reproduce the batch", optimal)
		}
	}
}

// PRNG 可以重用 Snapshot 的回傳緩衝區；整批期間的快照不得改寫已取得的 Before。
func TestDevSpinsBeforeSurvivesReusedSnapshotBuffer(t *testing.T) {
	d := devSimulator(t, faultLab(t, core.Default(), false), 77)
	d.m.core.PRNG = &offlineScratchPRNG{PRNG: d.m.core.PRNG}
	start := corefmt.EncodeBase64URL(coreState(t, d.m))
	rep, err := d.Spins(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Before != start {
		t.Fatal("before was overwritten by a later snapshot")
	}
}

func TestDevRestoreSimReplays(t *testing.T) {
	for _, optimal := range []bool{false, true} {
		lab := faultLab(t, core.Default(), optimal)
		rep, err := devSimulator(t, lab, 77).Sim(0, 5000)
		if err != nil {
			t.Fatal(err)
		}
		again, err := devSimulator(t, lab, 78).RestoreSim(rep.Before, 0, 5000)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(again, rep) {
			t.Fatalf("optimal %v: RestoreSim(before) does not reproduce the simulation", optimal)
		}
	}
}

// 遊戲回傳 nil 時，Spins 回報錯誤，不 panic、也不計入統計；未結束卻沒有選項屬於輸出違約，
// 與 Spin、BetRunner 一樣 panic。
func TestDevSpinsBrokenResults(t *testing.T) {
	d := devSimulator(t, brokenResultLab(t, "nil"), 77)
	if _, err := d.Spins(0, 10); !errors.Is(err, ErrGameRejected) {
		t.Fatalf("nil: Spins error %v, want %v", err, ErrGameRejected)
	}
	d = devSimulator(t, brokenResultLab(t, "incomplete"), 77)
	p := catchPanic(func() { _, _ = d.Spins(0, 10) })
	if err, ok := p.(error); !ok || !strings.Contains(err.Error(), "inconsistent segment") {
		t.Fatalf("incomplete: panic %v", p)
	}
}

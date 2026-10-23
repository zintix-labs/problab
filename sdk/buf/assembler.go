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

package buf

import (
	"errors"
	"fmt"
)

var (
	// ErrCycleOutOfOrder 表示段號不連續：不在局中卻收到 Cycle != 0，或局中收到的不是下一段。
	ErrCycleOutOfOrder = errors.New("spin assembler: cycle out of order")
	// ErrBetChanged 表示局中收到的 BetMode 或 BetMult 和第 0 段不同。
	ErrBetChanged = errors.New("spin assembler: bet changed within game")
)

// SpinAssembler 把一局的各段結果組成一份完整局的 SpinResult。
//
//   - 單段局（不在局中，收到 Cycle 0 且已結束）直接放行，回傳原結果，不複製。
//   - 多段局從第 0 段開始，逐段把結果複製進 Assembler 自己持有、可重用的緩衝；
//     最後一段結束時交出這份完整局結果。
//
// 呼叫前提（違反時行為未定義，可能 panic）：
//   - 傳入的結果不是 nil，是合法的段結果，至少包含一個 GameModeResult；
//   - 同一個 Assembler 不並行使用；
//   - 同一局的各段屬於同一個遊戲、Logic 與設定；Assembler 不逐段檢查身分；
//   - 傳入的段結果與交出的完整局結果，呼叫端都只能讀取；
//   - 不把 Assembler 交出的組裝結果，再傳回同一個 Assembler 的 Add。
//
// 交出的完整局結果必須在下一次 Spin、Add 或 Reset 之前使用完畢：單段局交出的是 Machine
// 的緩衝，多段局交出的是 Assembler 的緩衝。需要保留時自行複製（轉 DTO 即是複製）。
//
// 組裝結果的起始快照取自第 0 段、結束快照取自最後一段，只代表整局的邊界，不足以重播
// 中間各段。ExtendResult 共享參照，依 ExtendResult.Snapshot 的穩定契約成立。
// 緩衝容量只增不減；需要釋放時丟棄 Assembler 即可。
type SpinAssembler struct {
	res       SpinResult        // 完整局緩衝；res.State 指向 state
	state     SpinState         // 完整局的狀態
	modes     []*GameModeResult // 可重用的 GMR 池
	used      int               // 本局已使用的 GMR 數
	startSnap []byte            // 起始快照緩衝
	afterSnap []byte            // 結束快照緩衝
	inGame    bool              // 是否正在組裝一局
	next      int               // 局中時期待的下一個 Cycle
}

// NewSpinAssembler 建立空的 Assembler；第 0 段會寫入完整局結果的全部頂層欄位。
func NewSpinAssembler() *SpinAssembler {
	a := &SpinAssembler{}
	a.res.State = &a.state
	return a
}

// Add 接收一段結果：
//   - 回傳 nil, nil：這一局尚未完成，呼叫端準備下一段；
//   - 回傳 result, nil：完整局結果。單段局是傳入的結果本身，多段局是 Assembler 的緩衝；
//   - 回傳 nil, err：對帳失敗（ErrCycleOutOfOrder 或 ErrBetChanged），Assembler 的狀態不變，
//     呼叫端可以補送正確的段，或呼叫 Reset。
//
// 所有檢查都在修改狀態之前完成。
func (a *SpinAssembler) Add(sr *SpinResult) (*SpinResult, error) {
	if !a.inGame {
		if sr.Cycle != 0 {
			return nil, fmt.Errorf("%w: got cycle %d, want 0", ErrCycleOutOfOrder, sr.Cycle)
		}
		if sr.IsGameEnd {
			return sr, nil
		}
		a.begin(sr)
	} else {
		if sr.Cycle != a.next {
			return nil, fmt.Errorf("%w: got cycle %d, want %d", ErrCycleOutOfOrder, sr.Cycle, a.next)
		}
		if sr.BetMode != a.res.BetMode || sr.BetMult != a.res.BetMult {
			return nil, fmt.Errorf("%w: got mode %d mult %d, want mode %d mult %d",
				ErrBetChanged, sr.BetMode, sr.BetMult, a.res.BetMode, a.res.BetMult)
		}
	}
	a.appendSegment(sr)
	if !sr.IsGameEnd {
		a.inGame, a.next = true, sr.Cycle+1
		return nil, nil
	}
	a.finishGame(sr)
	a.inGame = false
	return &a.res, nil
}

// Reset 放棄未完成的局並保留緩衝容量；之前交出的組裝結果隨之失效。
// 它是正常放棄殘局的方式，不保證 Assembler 或 Machine 在 panic 之後可以恢復使用。
func (a *SpinAssembler) Reset() {
	a.inGame, a.next = false, 0
	a.clear()
}

// clear 清空完整局緩衝，保留容量。
func (a *SpinAssembler) clear() {
	a.used = 0
	a.res.GameModeList = a.res.GameModeList[:0]
	a.state.StartCoreSnap = nil
	a.state.AfterCoreSnap = nil
	a.state.Checkpoint = nil
}

// begin 以第 0 段開始一局：清空緩衝並寫入頂層欄位與起始快照。
func (a *SpinAssembler) begin(sr *SpinResult) {
	a.clear()
	r := &a.res
	r.TotalWin, r.GameModeCount, r.IsGameEnd = 0, 0, false
	r.GameName, r.GameID, r.Logic = sr.GameName, sr.GameID, sr.Logic
	r.BetUnits = sr.BetUnits // 唯讀設定，共享參照
	r.Bet, r.BetMode, r.BetMult = sr.Bet, sr.BetMode, sr.BetMult
	if sr.State != nil {
		a.state.StartCoreSnap = copySnap(&a.startSnap, sr.State.StartCoreSnap)
	}
}

// appendSegment 把一段的贏分、GMR 數與 GMR 複製品接到完整局上。
func (a *SpinAssembler) appendSegment(sr *SpinResult) {
	r := &a.res
	r.TotalWin += sr.TotalWin
	r.GameModeCount += sr.GameModeCount
	r.Cycle = sr.Cycle
	r.IsGameEnd = sr.IsGameEnd
	for _, src := range sr.GameModeList {
		if a.used == len(a.modes) {
			a.modes = append(a.modes, new(GameModeResult))
		}
		dst := a.modes[a.used]
		a.used++
		dst.copyFrom(src)
		r.GameModeList = append(r.GameModeList, dst)
	}
}

// finishGame 在最後一段寫入結束快照與 Checkpoint。Checkpoint 共享參照，保留期限與結果相同。
func (a *SpinAssembler) finishGame(sr *SpinResult) {
	if sr.State == nil {
		return
	}
	a.state.AfterCoreSnap = copySnap(&a.afterSnap, sr.State.AfterCoreSnap)
	a.state.Checkpoint = sr.State.Checkpoint
}

// copySnap 把 src 複製到 *dst 的緩衝並回傳結果：src 為 nil 時回傳 nil；非 nil 的空 slice
// 回傳非 nil 的空 slice（append 到 nil 不會配置，所以先確保緩衝不是 nil）。
func copySnap(dst *[]byte, src []byte) []byte {
	if src == nil {
		return nil
	}
	if *dst == nil {
		*dst = make([]byte, 0, len(src))
	}
	*dst = append((*dst)[:0], src...)
	return *dst
}

// copyFrom 把 src 複製到 dst 自己的可重用緩衝區；dst 與 src 必須是不同的 GMR。
// 不共享 src 的任何 slice 底層陣列或 TmpAct；ExtendResult 共享參照。
func (dst *GameModeResult) copyFrom(src *GameModeResult) {
	dst.TotalWin = src.TotalWin
	dst.GameModeId = src.GameModeId
	dst.IsModeEnd = src.IsModeEnd
	dst.Trigger = src.Trigger
	dst.BetType = src.BetType
	dst.ScreenSize = src.ScreenSize
	dst.omitExtend = src.omitExtend

	dst.ActResults = append(dst.ActResults[:0], src.ActResults...)
	dst.Screens = append(dst.Screens[:0], src.Screens...)
	dst.HitsFlat = append(dst.HitsFlat[:0], src.HitsFlat...)

	if dst.TmpAct == nil {
		dst.TmpAct = new(TmpAct)
	}
	if src.TmpAct != nil {
		// Details 不隨 Reset 截斷，長度可能包含歷史殘留，只複製有效範圍。
		dst.Details = append(dst.Details[:0], src.GetDetails()...)
		*dst.TmpAct = *src.TmpAct
		return
	}
	// 手工組成、沒有 TmpAct 的結果：呼叫端保證 Details 全部有效。
	dst.Details = append(dst.Details[:0], src.Details...)
	*dst.TmpAct = TmpAct{CurrDetail: len(dst.Details)}
}

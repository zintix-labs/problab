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
	"fmt"

	"github.com/zintix-labs/problab/sdk/buf"
)

// BetRunner 的錯誤。執行前的錯誤不改變進行中的局，修正後可以重送。
var (
	// ErrBetInProgress 表示一局尚未結束（或已不可繼續、尚未 Abandon）時又呼叫 Start。
	ErrBetInProgress = errors.New("bet runner: a bet is in progress")
	// ErrNoPendingChoice 表示目前沒有等待選擇的局。
	ErrNoPendingChoice = errors.New("bet runner: no pending choice")
	// ErrChoiceOutOfRange 表示 choice 不在 [0, len(ChooseNext)) 內。
	ErrChoiceOutOfRange = errors.New("bet runner: choice out of range")
	// ErrSegmentLimit 表示下一段會超過段數上限。
	ErrSegmentLimit = errors.New("bet runner: segment limit reached")
	// ErrBetNotFinished 表示本局尚未完成，沒有完整局可取得。
	ErrBetNotFinished = errors.New("bet runner: the bet is not finished")
	// ErrBetBroken 表示上一段執行後沒有有效結果，本局不可繼續，必須 Abandon。
	ErrBetBroken = errors.New("bet runner: the bet cannot continue; call Abandon")
)

type runnerState uint8

const (
	runnerIdle    runnerState = iota // 沒有進行中的局
	runnerPending                    // 局進行中，等待 Choose
	runnerDone                       // 本局完成，Result 可取得
	runnerBroken                     // 執行後沒有有效結果，等待 Abandon
)

// BetRunner 在一台本機 Machine 上逐段執行一筆下注（從第 0 段到局結束），並以 Assembler
// 組出完整局。它是正式逐段操作在本機的有狀態封裝：同一個實例保存局內狀態，呼叫端只送
// choice；正式服務仍使用 Machine.Spin，由後端每次送入恢復所需的資料。
//
// 回傳單位固定：Start 與 Choose 一律回傳本段（包括最後一段），完整局只由 Result 取得，
// 所以逐次累計段的贏分等於整局。
//
// 使用契約：
//   - 一個 BetRunner 同一時間只由一個執行者使用；局進行中不得直接操作它持有的 Machine。
//   - 回傳的段結果與完整局結果都是借用的唯讀結果，有效到 Machine 的下一次執行，或下一次
//     Start、Choose、Abandon；需要保留時自行複製或轉成 DTO。
//   - 不接受取消：呼叫端在每次呼叫前後自行檢查，需要時 Abandon。
//   - 只用於 deterministic 的本機 Machine（例如 NewMachine）；不執行 production reseed。
//   - checkpoint 依遊戲的穩定契約持有參照（交出後不得再修改），不做深拷貝。
//   - 遊戲或 RNG 的內部 panic 照常向外傳遞，沿用棄用 Machine 的契約；Abandon 不代表可以恢復。
type BetRunner struct {
	m           *Machine
	asm         *buf.SpinAssembler
	maxSegments int

	betMode, betMult int
	capture          bool

	next     []int // 上一段 ChooseNext 的獨立副本
	cp       any   // 上一段輸出的 checkpoint
	cycle    int
	segments int
	state    runnerState
	result   *buf.SpinResult
}

// NewBetRunner 建立綁定 m 的 BetRunner。maxSegments 是一局的段數上限（含第 0 段），
// 必須大於 0。
func NewBetRunner(m *Machine, maxSegments int) (*BetRunner, error) {
	if m == nil {
		return nil, errors.New("bet runner: nil machine")
	}
	if maxSegments <= 0 {
		return nil, fmt.Errorf("bet runner: max segments must be positive, got %d", maxSegments)
	}
	return &BetRunner{m: m, asm: buf.NewSpinAssembler(), maxSegments: maxSegments}, nil
}

// Start 開始新的一局並執行第 0 段，回傳本段。in 只使用 BetMode、BetMult、
// CaptureSnapshots 與 StartSnap；Cycle、Choice、ChooseNext、Checkpoint 必須為零值。
//
// 操作狀態、輸入與投注的檢查失敗時保留原狀態（例如上一局仍可由 Result 取得）。通過檢查
// 後上一局即失效；之後若指定的快照被拒絕，狀態回到沒有進行中的局。
func (r *BetRunner) Start(in SpinInput) (*buf.SpinResult, error) {
	switch r.state {
	case runnerPending:
		return nil, ErrBetInProgress
	case runnerBroken:
		return nil, fmt.Errorf("%w: %w", ErrBetInProgress, ErrBetBroken)
	}
	if in.Cycle != 0 || in.Choice != 0 || len(in.ChooseNext) != 0 || in.Checkpoint != nil {
		return nil, fmt.Errorf("bet runner: start input must not carry continuation fields: %w", ErrSegmentInput)
	}
	if err := r.validBet(in.BetMode, in.BetMult); err != nil {
		return nil, err
	}
	r.asm.Reset()
	r.next, r.cp, r.result = r.next[:0], nil, nil
	r.cycle, r.segments, r.state = 0, 0, runnerIdle
	r.betMode, r.betMult, r.capture = in.BetMode, in.BetMult, in.CaptureSnapshots
	return r.run(0, 0, nil, nil, in.StartSnap)
}

// Choose 以上一段 ChooseNext 的位置索引 choice 執行下一段，回傳本段。
func (r *BetRunner) Choose(choice int) (*buf.SpinResult, error) {
	switch r.state {
	case runnerPending:
	case runnerBroken:
		return nil, fmt.Errorf("%w: %w", ErrNoPendingChoice, ErrBetBroken)
	default:
		return nil, ErrNoPendingChoice
	}
	if choice < 0 || choice >= len(r.next) {
		return nil, fmt.Errorf("%w: %d not in [0, %d)", ErrChoiceOutOfRange, choice, len(r.next))
	}
	if r.segments >= r.maxSegments {
		return nil, fmt.Errorf("%w: %d", ErrSegmentLimit, r.maxSegments)
	}
	return r.run(r.cycle+1, choice, r.next, r.cp, nil)
}

// Result 在本局完成後回傳完整局；讀取不清除，可重複讀取，直到下一局開始或借用期限結束。
func (r *BetRunner) Result() (*buf.SpinResult, error) {
	switch r.state {
	case runnerDone:
		return r.result, nil
	case runnerBroken:
		return nil, fmt.Errorf("%w: %w", ErrBetNotFinished, ErrBetBroken)
	default:
		return nil, ErrBetNotFinished
	}
}

// Abandon 放棄目前的局，清除進度與組裝資料並保留容量。不還原已消耗的 RNG，也不代表退款
// 或交易回滾。
func (r *BetRunner) Abandon() {
	r.asm.Reset()
	r.next, r.cp, r.result = r.next[:0], nil, nil
	r.cycle, r.segments, r.state = 0, 0, runnerIdle
}

func (r *BetRunner) validBet(betMode, betMult int) error {
	if betMode < 0 || betMode >= len(r.m.BetUnits) {
		return fmt.Errorf("bet runner: bet mode %d out of range", betMode)
	}
	unit := r.m.BetUnits[betMode]
	if betMult < 1 || unit <= 0 || unit > int(^uint(0)>>1)/betMult {
		return fmt.Errorf("bet runner: invalid or overflowing wager (unit %d, mult %d)", unit, betMult)
	}
	return nil
}

// run 執行一段並更新局內狀態。執行前的拒絕不改變狀態；執行後沒有有效結果時，本局不可繼續。
func (r *BetRunner) run(cycle, choice int, next []int, cp any, snap []byte) (*buf.SpinResult, error) {
	m := r.m
	m.fillSegment(r.betMode, r.betMult, cycle, choice, next, cp, snap)
	sr, err := m.execSegment(segmentOpts{external: len(snap) != 0, capture: r.capture, checkOutput: true})
	if err != nil {
		if errors.Is(err, ErrGameRejected) {
			r.state = runnerBroken
		}
		return nil, err
	}
	r.segments++
	res, err := r.asm.Add(sr)
	if err != nil {
		r.state = runnerBroken
		return nil, fmt.Errorf("bet runner: assemble segment %d: %w", sr.Cycle, err)
	}
	if res != nil {
		r.state, r.result = runnerDone, res
		return sr, nil
	}
	r.state = runnerPending
	r.next = append(r.next[:0], sr.ChooseNext...)
	r.cp = nil
	if sr.State != nil {
		r.cp = sr.State.Checkpoint
	}
	r.cycle = sr.Cycle
	return sr, nil
}

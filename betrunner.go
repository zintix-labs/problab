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
	// ErrBetInProgress 表示一局尚未結束（或已不可繼續、尚未 Reset）時又呼叫 Bet。
	ErrBetInProgress = errors.New("bet runner: a bet is in progress")
	// ErrNoPendingChoice 表示目前沒有停在選擇點、等待 Next 的局。
	ErrNoPendingChoice = errors.New("bet runner: no pending choice")
	// ErrChoiceOutOfRange 表示 choice 不在 [0, len(ChooseNext)) 內。
	ErrChoiceOutOfRange = errors.New("bet runner: choice out of range")
	// ErrSegmentLimit 表示下一段會超過段數上限。上限是執行限制，不代表遊戲故障。
	ErrSegmentLimit = errors.New("bet runner: segment limit reached")
	// ErrBetBroken 表示本局執行後沒有有效結果，或自動選擇的續段失敗，本局不可繼續，必須 Reset。
	ErrBetBroken = errors.New("bet runner: the bet cannot continue; call Reset")
	// ErrInvalidChooseMode 表示選擇方式不是已定義的值。
	ErrInvalidChooseMode = errors.New("bet runner: invalid choose mode")
)

// ChooseMode 是一筆下注的選擇方式。零值 ChoosePlayer 停在選擇點，交還呼叫端。
//
// ChooseFirst、ChooseLast 是兩種固定政策：每個選擇點都選 ChooseNext 的第一個或最後一個
// 位置（不是固定的入口值）。結果只代表該政策，不代表所有選法；其他政策以 ChoosePlayer
// 在外層逐次決定。
type ChooseMode uint8

const (
	ChoosePlayer ChooseMode = iota // 停在選擇點，由呼叫端以 Next 選擇
	ChooseFirst                    // 每個選擇點選 ChooseNext 的第一個位置，跑到局結束
	ChooseLast                     // 每個選擇點選 ChooseNext 的最後一個位置，跑到局結束
)

func (c ChooseMode) valid() bool { return c <= ChooseLast }

func (c ChooseMode) String() string {
	switch c {
	case ChoosePlayer:
		return "player"
	case ChooseFirst:
		return "first"
	case ChooseLast:
		return "last"
	}
	return fmt.Sprintf("ChooseMode(%d)", uint8(c))
}

// ParseChooseMode 解析外部參數（server、CLI）的選擇方式：空字串為 ChoosePlayer（不指定），
// "first"、"last" 為對應的模式。其他值（包括 "player" 與大小寫不同的寫法）回傳包住
// ErrInvalidChooseMode 的 error。
func ParseChooseMode(s string) (ChooseMode, error) {
	switch s {
	case "":
		return ChoosePlayer, nil
	case "first":
		return ChooseFirst, nil
	case "last":
		return ChooseLast, nil
	}
	return ChoosePlayer, fmt.Errorf("%w: %q (want first or last)", ErrInvalidChooseMode, s)
}

// DefaultMaxSegments 是 BetRunner 與消費端預設的段數上限。它是防止遊戲永不結束的保守
// 執行限制，不保證所有正常的遊戲都能在這個段數內完成。
const DefaultMaxSegments = 10

// BetRunnerOption 設定 NewBetRunner。
type BetRunnerOption func(*betRunnerOptions)

type betRunnerOptions struct{ maxSegments int }

// WithMaxSegments 設定一局的段數上限（含第 0 段），必須大於 0。
func WithMaxSegments(n int) BetRunnerOption {
	return func(o *betRunnerOptions) { o.maxSegments = n }
}

type runnerState uint8

const (
	runnerIdle    runnerState = iota // 沒有進行中的局
	runnerPending                    // 局進行中，停在選擇點等待 Next
	runnerDone                       // 本局完成
	runnerBroken                     // 本局不可繼續，等待 Reset
)

// BetRunner 在一台本機 Machine 上執行一筆下注（從第 0 段到局結束），並以 Assembler 組出
// 完整局。它是正式逐段操作在本機的有狀態封裝：同一個實例保存局內狀態，呼叫端只送
// choice；正式服務仍使用 Machine.Spin，由後端每次送入恢復所需的資料。
//
// 回傳的結果以 IsGameEnd 判斷單位：未結束是本段，結束是完整局（單段局的本段即完整局）。
// ChooseFirst、ChooseLast 的 Bet 一律回傳完整局；ChoosePlayer 的 Bet 與 Next 在局結束前
// 回傳本段，最後一次回傳完整局。計分時，局結束以完整局的 TotalWin 覆蓋，不累加先前
// 回傳的段：
//
//	Bet(in, ChoosePlayer) → 未結束，TotalWin = 10（第 0 段）
//	Next(0)               → 未結束，TotalWin = 20（第 1 段）
//	Next(1)               → 已結束，TotalWin = 60（完整局：10 + 20 + 第 2 段的 30）
//
// 完整局不保留段邊界：最後一段的起始快照與輸入 checkpoint 無法從完整局還原。
//
// 使用契約：
//   - 一個 BetRunner 同一時間只由一個執行者使用；局進行中不得直接操作它持有的 Machine。
//   - 回傳的段結果與完整局結果都是借用的唯讀結果，有效到 Machine 的下一次執行，或下一次
//     Bet、Next、Reset；需要保留時自行複製或轉成 DTO。
//   - 不接受取消：呼叫端在每次呼叫前後自行檢查，需要時 Reset。ChooseFirst、ChooseLast
//     執行到局結束才返回；段數上限只限制執行次數，不保證完成時間。
//   - 只用於 deterministic 的本機 Machine（例如 NewMachine）；不執行 production reseed。
//   - checkpoint 依遊戲的穩定契約持有參照（交出後不得再修改），不做深拷貝。
//   - 遊戲或 RNG 的內部 panic 照常向外傳遞，沿用棄用 Machine 的契約；Reset 不代表可以恢復。
type BetRunner struct {
	m           *Machine
	asm         *buf.SpinAssembler
	maxSegments int

	mode             ChooseMode
	betMode, betMult int
	capture          bool

	next     []int // 上一段 ChooseNext 的獨立副本
	cp       any   // 上一段輸出的 checkpoint
	cycle    int
	segments int
	state    runnerState
	result   *buf.SpinResult
}

// NewBetRunner 建立綁定 m 的 BetRunner。段數上限預設為 DefaultMaxSegments，可以
// WithMaxSegments 覆寫；m 為 nil 或上限不大於 0 時回傳 error。
func NewBetRunner(m *Machine, opts ...BetRunnerOption) (*BetRunner, error) {
	if m == nil {
		return nil, errors.New("bet runner: nil machine")
	}
	o := betRunnerOptions{maxSegments: DefaultMaxSegments}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if o.maxSegments <= 0 {
		return nil, fmt.Errorf("bet runner: max segments must be positive, got %d", o.maxSegments)
	}
	return &BetRunner{m: m, asm: buf.NewSpinAssembler(), maxSegments: o.maxSegments}, nil
}

// Bet 開始一筆下注並執行到停止點：ChooseFirst、ChooseLast 跑到局結束；ChoosePlayer 跑到
// 第一個選擇點，或第 0 段就結束。in 只使用 BetMode、BetMult、CaptureSnapshots 與
// StartSnap（只作用於第 0 段）；Cycle、Choice、ChooseNext、Checkpoint 必須為零值。
//
// 選擇方式、操作狀態、輸入與投注的檢查失敗時保留原狀態（例如上一局的完整局仍然有效）。
// 通過檢查後上一局即失效；之後若指定的快照被拒絕，狀態回到沒有進行中的局。
func (r *BetRunner) Bet(in SpinInput, mode ChooseMode) (*buf.SpinResult, error) {
	if !mode.valid() {
		return nil, fmt.Errorf("%w: %d", ErrInvalidChooseMode, uint8(mode))
	}
	switch r.state {
	case runnerPending:
		return nil, ErrBetInProgress
	case runnerBroken:
		return nil, fmt.Errorf("%w: %w", ErrBetInProgress, ErrBetBroken)
	}
	if in.Cycle != 0 || in.Choice != 0 || len(in.ChooseNext) != 0 || in.Checkpoint != nil {
		return nil, fmt.Errorf("bet runner: bet input must not carry continuation fields: %w", ErrSegmentInput)
	}
	if err := r.validBet(in.BetMode, in.BetMult); err != nil {
		return nil, err
	}
	r.clear()
	r.mode = mode
	r.betMode, r.betMult, r.capture = in.BetMode, in.BetMult, in.CaptureSnapshots
	return r.drive(r.run(0, 0, nil, nil, in.StartSnap))
}

// Next 在 ChoosePlayer 的局停在選擇點時，以上一段 ChooseNext 的位置索引 choice 執行下一段。
// 局尚未結束時回傳本段，結束時回傳完整局。
func (r *BetRunner) Next(choice int) (*buf.SpinResult, error) {
	switch {
	case r.state == runnerBroken:
		return nil, fmt.Errorf("%w: %w", ErrNoPendingChoice, ErrBetBroken)
	case r.state != runnerPending || r.mode != ChoosePlayer:
		return nil, ErrNoPendingChoice
	}
	if choice < 0 || choice >= len(r.next) {
		return nil, fmt.Errorf("%w: %d not in [0, %d)", ErrChoiceOutOfRange, choice, len(r.next))
	}
	if r.segments >= r.maxSegments {
		return nil, fmt.Errorf("%w: %d", ErrSegmentLimit, r.maxSegments)
	}
	return r.drive(r.run(r.cycle+1, choice, r.next, r.cp, nil))
}

// Reset 放棄目前的局，清除進度與組裝資料並保留容量。不還原已消耗的 RNG，也不代表退款
// 或交易回滾。
func (r *BetRunner) Reset() {
	r.clear()
	r.mode = ChoosePlayer
}

func (r *BetRunner) clear() {
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

// drive 依本局的選擇方式處理剛執行的段：局結束回傳完整局；ChoosePlayer 回傳本段；
// ChooseFirst、ChooseLast 自動選擇並繼續執行，直到局結束或發生錯誤。自動選擇的局沒有
// 呼叫端能接手停在中途的局，所以續段的錯誤與段數上限都使本局不可繼續。
func (r *BetRunner) drive(sr *buf.SpinResult, err error) (*buf.SpinResult, error) {
	for {
		if err != nil {
			if r.mode != ChoosePlayer && r.state == runnerPending {
				r.state = runnerBroken
			}
			return nil, err
		}
		if r.state == runnerDone {
			return r.result, nil
		}
		if r.mode == ChoosePlayer {
			return sr, nil
		}
		if r.segments >= r.maxSegments {
			r.state = runnerBroken
			return nil, fmt.Errorf("%w: %d", ErrSegmentLimit, r.maxSegments)
		}
		choice := 0
		if r.mode == ChooseLast {
			choice = len(r.next) - 1
		}
		sr, err = r.run(r.cycle+1, choice, r.next, r.cp, nil)
	}
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

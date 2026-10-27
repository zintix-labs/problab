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

	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/buf"
)

// 執行核心：Machine 的執行入口（Spin、SpinUnsafe、BetRunner）共用這裡的
// 私有請求、單段執行、RNG 起點準備與隔離機制，以及 spin()。
//
// 職責：
//   - 包裝層（各入口）：鎖、外部請求驗證、錯誤表達、結果轉換。
//   - 單段執行（fillSegment、execSegment）：填入請求、結構不變量、選擇起點與路由、
//     production reseed、抽庫、在恢復守衛的保護範圍內呼叫 spin()、檢查結果、擷取快照。
//   - 起點機制（enterStart 與 rngGuard）：記錄恢復點、Restore 起點、首次 Restore
//     失敗的清理、收尾還原、雙重故障的原因保留。
//   - spin()：以 Machine 持有的請求呼叫 GetResult，不做其他事。

// errMachineFaulted 表示 Machine 的 RNG 恢復失敗，不可再使用。
var errMachineFaulted = errors.New("machine is unusable: restoring its RNG recovery point failed")

// startKind 是本次執行的 RNG 起點來源。
type startKind uint8

const (
	startNone     startKind = iota // 不指定：延續機台目前的 RNG，不記錄恢復點
	startExternal                  // 外部快照（回放）
	startSeed                      // 從 optimal artifact 抽出的 seed
)

// rngGuard 是恢復守衛。armed 為 false（startNone）時 finish 不做任何事。
type rngGuard struct {
	m     *Machine
	armed bool
}

// resetSpinRequest 把 Machine 持有的請求恢復為基底：保留 GameId、GameName，重設
// 本次請求值，保留可重用緩衝區的容量。只碰請求，不碰 RNG、結果與遊戲狀態。
func (m *Machine) resetSpinRequest() {
	m.spinRequest = buf.SpinRequest{GameId: m.gameId, GameName: m.gameName}
	m.reqSnap = m.reqSnap[:0]
	m.reqChooseNext = m.reqChooseNext[:0]
}

// fillSegment 一次寫入整份私有請求。ChooseNext 複製進 reqChooseNext，snap（本段的指定
// 起始快照，可為空）複製進 reqSnap，之後修改輸入不影響遊戲讀到的值。Bet 由 BetUnits
// 推導；betMode 必須在範圍內，由呼叫端保證。
//
// 逐欄寫入私有請求的每一個欄位（不經暫存的複合字面值，避免熱路徑上的清零與複製）；
// buf.SpinRequest 新增欄位時必須同步，TestFillSegmentWritesEveryField 會檢查。
func (m *Machine) fillSegment(betMode, betMult, cycle, choice int, chooseNext []int, cp any, snap []byte) {
	m.reqChooseNext = m.reqChooseNext[:0]
	if len(chooseNext) != 0 {
		m.reqChooseNext = append(m.reqChooseNext, chooseNext...)
	}
	m.reqSnap = m.reqSnap[:0]
	if len(snap) != 0 {
		m.reqSnap = append(m.reqSnap, snap...)
	}
	q := &m.spinRequest
	q.GameName, q.GameId = m.gameName, m.gameId
	q.Bet, q.BetMode, q.BetMult = m.BetUnits[betMode]*betMult, betMode, betMult
	q.Cycle, q.Choice, q.ChooseNext, q.Checkpoint = cycle, choice, m.reqChooseNext, cp
}

// validSegment 檢查結構不變量：Cycle >= 0；Cycle 為 0 時沒有選項、Choice 為 0；
// Cycle > 0 時有選項且 Choice 在範圍內。
func validSegment(r *buf.SpinRequest) bool {
	if r.Cycle < 0 {
		return false
	}
	if r.Cycle == 0 {
		return len(r.ChooseNext) == 0 && r.Choice == 0
	}
	return len(r.ChooseNext) > 0 && r.Choice >= 0 && r.Choice < len(r.ChooseNext)
}

// segmentOpts 是各入口在單段執行上的差異。
type segmentOpts struct {
	external    bool // reqSnap 是本段的指定起始快照
	reseed      bool // 未指定快照時先 reseed（只有 production 的 Spin）
	capture     bool // 把起始／結束 RNG 快照寫進結果的 State
	checkOutput bool // 檢查 IsGameEnd 與 ChooseNext 是否一致（Spin、BetRunner）
}

// startRejected 是指定的起始快照被 PRNG 拒絕、且已防禦性還原的錯誤；保留 PRNG 的原因，
// 讓 Spin 維持原本的錯誤訊息。
type startRejected struct{ err error }

func (e *startRejected) Error() string {
	return ErrStartSnapshotRejected.Error() + ": " + e.err.Error()
}
func (e *startRejected) Is(target error) bool { return target == ErrStartSnapshotRejected }
func (e *startRejected) Unwrap() error        { return e.err }

// execSegment 是 Spin、SpinUnsafe、BetRunner 共用的單段執行。呼叫前，入口已以
// fillSegment 寫入本段輸入。
//
// 結構不變量與起點路由都在 production reseed、抽庫之前判斷，拒絕時不改變 RNG。
// optimal 開啟時，依這個 bet mode 的設定決定本段抽庫或照常使用機台的 RNG；續段的入口
// 沒有配置時回傳 ErrContinuationUnsupported。遊戲回傳 nil 時回傳 ErrGameRejected，恢復
// 守衛照常收尾。結果違反輸出契約，或 optimal 開啟時交付了無法接續的段，panic（內部故障）。
func (m *Machine) execSegment(o segmentOpts) (*buf.SpinResult, error) {
	q := &m.spinRequest
	if !validSegment(q) {
		return nil, ErrSegmentInput
	}
	kind, start := startNone, []byte(nil)
	var startSnap []byte
	switch {
	case o.external:
		kind, start = startExternal, m.reqSnap
	default:
		seeded := false
		if m.optimal != nil {
			var configured bool
			if seeded, configured = m.route(q); !configured {
				return nil, ErrContinuationUnsupported
			}
		}
		if o.reseed {
			if err := m.core.Reseed(); err != nil {
				return nil, errs.NewFatal("reseed PRNG before new game cycle: " + err.Error())
			}
		}
		if seeded {
			kind = startSeed
			start = m.pickSeed(q, &m.seedBuf)
		} else if o.capture {
			startSnap = m.snapshotInto(&m.startBuf, "start state")
		}
	}
	if kind == startNone {
		// 不指定起點：沒有恢復點，不需要恢復守衛。
		return m.finishSegment(m.spin(), o, kind, startSnap)
	}
	return m.execGuarded(o, kind, start)
}

// execGuarded 在恢復守衛的保護範圍內執行指定起點的一段。與 execSegment 分開，讓不指定起點的
// 熱路徑不帶 defer。
func (m *Machine) execGuarded(o segmentOpts, kind startKind, start []byte) (*buf.SpinResult, error) {
	g, formatErr := m.enterStart(kind, start)
	if formatErr != nil {
		return nil, &startRejected{err: formatErr}
	}
	defer g.finish()
	return m.finishSegment(m.spin(), o, kind, start)
}

// finishSegment 檢查結果、以請求的 Cycle 覆寫段號，並依 capture 擷取或清除結果的 RNG
// 快照；必須在恢復守衛還原之前執行。
func (m *Machine) finishSegment(sr *buf.SpinResult, o segmentOpts, kind startKind, startSnap []byte) (*buf.SpinResult, error) {
	if sr == nil {
		return nil, ErrGameRejected
	}
	if o.checkOutput && sr.IsGameEnd != (len(sr.ChooseNext) == 0) {
		panic(fmt.Errorf("game returned an inconsistent segment: IsGameEnd=%v, len(ChooseNext)=%d", sr.IsGameEnd, len(sr.ChooseNext)))
	}
	if m.optimal != nil && kind != startExternal && !sr.IsGameEnd && !m.optimal.Offers(m.spinRequest.BetMode, sr.ChooseNext) {
		// 不放在 checkOutput 之下：SpinUnsafe 也不得交付下一段無法接續的結果。
		if len(sr.ChooseNext) == 0 {
			panic(fmt.Errorf("optimal bet mode %d: the game returned an unfinished segment without ChooseNext", m.spinRequest.BetMode))
		}
		panic(fmt.Errorf("optimal bet mode %d: ChooseNext %v has an entry without an after slot", m.spinRequest.BetMode, sr.ChooseNext))
	}
	sr.Cycle = m.spinRequest.Cycle // 段號由引擎填入，遊戲寫入的值不保留
	if !o.capture {
		if sr.State != nil {
			sr.State.StartCoreSnap = nil
			sr.State.AfterCoreSnap = nil
		}
		return sr, nil
	}
	afterSnap := m.snapshotInto(&m.afterBuf, "after state")
	if sr.State == nil {
		m.fallbackState = buf.SpinState{}
		sr.State = &m.fallbackState
	}
	sr.State.StartCoreSnap = startSnap
	sr.State.AfterCoreSnap = afterSnap
	return sr, nil
}

// checkUsable 在 RNG 恢復失敗過的 Machine 上 panic；只有 Spin 呼叫它。
func (m *Machine) checkUsable() {
	if m.faulted {
		panic(errMachineFaulted)
	}
}

// spin 以 Machine 持有的請求，從目前的 RNG 狀態執行遊戲。它是各執行入口共用的
// 遊戲呼叫節點：不抽庫、不 Restore、不擷取快照，也不做任何驗證或結果檢查。
func (m *Machine) spin() *buf.SpinResult {
	return m.gh.GetResult(&m.spinRequest)
}

// route 回報 optimal 開啟時本段是否抽庫，以及續段的入口是否配置：Cycle 0 看 start，
// 續段看 after[ChooseNext[Choice]]。
func (m *Machine) route(q *buf.SpinRequest) (seeded, configured bool) {
	if q.Cycle == 0 {
		return m.optimal.StartSeeded(q.BetMode), true
	}
	return m.optimal.Entry(q.BetMode, q.ChooseNext[q.Choice])
}

// pickSeed 從本段的庫抽 seed 並複製到 Machine 的緩衝區 dst。失敗屬於內部執行故障。
func (m *Machine) pickSeed(q *buf.SpinRequest, dst *[]byte) []byte {
	var seed []byte
	var err error
	if q.Cycle == 0 {
		seed, err = m.optimal.PickStart(q.BetMode, m.core)
	} else {
		seed, err = m.optimal.PickEntry(q.BetMode, q.ChooseNext[q.Choice], m.core)
	}
	if err != nil {
		panic(fmt.Errorf("pick optimal seed: %w", err))
	}
	*dst = append((*dst)[:0], seed...)
	return *dst
}

// snapshotInto 擷取機台 RNG 的快照並複製到 Machine 的緩衝區 dst；失敗時 panic。
// 複製讓快照不與 PRNG 自己重用的緩衝區共用底層陣列。
func (m *Machine) snapshotInto(dst *[]byte, what string) []byte {
	snap, err := m.core.Snapshot()
	if err != nil {
		panic(fmt.Errorf("snapshot %s: %w", what, err))
	}
	*dst = append((*dst)[:0], snap...)
	return *dst
}

// enterStart 準備 RNG 起點並回傳恢復守衛。
//   - startNone：不記錄恢復點，回傳未啟用的守衛。
//   - 其他：記錄恢復點後 Restore(start)。Restore 回傳錯誤或直接 panic 時，由這裡
//     立即還原恢復點，不交給包裝層。
//
// 只有 startExternal 的 Restore 回傳錯誤、且恢復成功時，回傳 formatErr（呼叫端回
// Warn，不執行遊戲）。其他失敗一律 panic。成功時，包裝層必須緊接著 defer g.finish()。
func (m *Machine) enterStart(kind startKind, start []byte) (g rngGuard, formatErr error) {
	if kind == startNone {
		return rngGuard{}, nil
	}
	m.snapshotInto(&m.restoreBuf, "RNG recovery point")
	if err := m.restoreStart(start); err != nil {
		m.recoverTo(err)
		if kind == startExternal {
			return rngGuard{}, err
		}
		panic(fmt.Errorf("restore optimal seed: %w", err))
	}
	return rngGuard{m: m, armed: true}, nil
}

// restoreStart 在保護範圍內 Restore 起點：Restore 直接 panic 時，先還原恢復點，再原樣
// 傳遞原本的 panic 值。PRNG 可能在部分改寫狀態後才失敗。
func (m *Machine) restoreStart(start []byte) error {
	defer func() {
		if p := recover(); p != nil {
			m.recoverTo(p) // 恢復失敗時不會返回
			panic(p)
		}
	}()
	return m.core.Restore(start)
}

// recoverTo 把機台 RNG 還原到恢復點，成功時返回。恢復本身失敗（Restore 回傳錯誤或
// 直接 panic）時，標記 Machine 失效，並以 errors.Join 同時保留 cause（先前的故障；
// nil 表示沒有）與恢復故障後 panic，不會返回。
func (m *Machine) recoverTo(cause any) {
	failure := m.restoreRecoveryPoint()
	if failure == nil {
		return
	}
	m.faulted = true
	if cause == nil {
		panic(failure)
	}
	panic(errors.Join(asError(cause), failure))
}

func (m *Machine) restoreRecoveryPoint() (failure error) {
	defer func() {
		if p := recover(); p != nil {
			failure = fmt.Errorf("restore RNG recovery point panicked: %w", asError(p))
		}
	}()
	if err := m.core.Restore(m.restoreBuf); err != nil {
		return fmt.Errorf("restore RNG recovery point: %w", err)
	}
	return nil
}

// finish 是恢復守衛的收尾，必須由包裝層以 `defer g.finish()` 直接延遲呼叫，recover
// 才會生效。它在正常返回與 panic 時都還原恢復點；恢復成功時原樣傳遞原本的 panic 值。
func (g rngGuard) finish() {
	if !g.armed {
		return
	}
	p := recover() // 保留原始值，不轉換
	g.m.recoverTo(p)
	if p != nil {
		panic(p)
	}
}

// asError 只在需要合併原因時使用：error 保留原值（errors.Is 仍可判斷），其他值包成 error。
func asError(v any) error {
	if err, ok := v.(error); ok {
		return err
	}
	return fmt.Errorf("panic: %v", v)
}

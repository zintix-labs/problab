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

// 執行核心：Machine 的執行入口（Spin、SpinUnsafe）共用這裡的
// 私有請求、RNG 起點準備與隔離機制，以及 spin()。
//
// 職責：
//   - 包裝層（各入口）：鎖、重設與填入請求、選擇起點來源、production reseed、
//     抽庫、在恢復守衛的保護範圍內呼叫 spin()、擷取快照、轉換結果。
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
// 本次請求值，清除 StartState 參照，保留可重用緩衝區的容量。只碰請求，不碰 RNG、
// 結果與遊戲狀態。
func (m *Machine) resetSpinRequest() {
	m.spinRequest = buf.SpinRequest{GameId: m.gameId, GameName: m.gameName}
	m.reqStart = buf.StartState{}
	m.reqSnap = m.reqSnap[:0]
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

// pickSeed 從 artifact 抽 seed 並複製到 Machine 的緩衝區 dst。失敗屬於內部執行故障。
func (m *Machine) pickSeed(betMode int, dst *[]byte) []byte {
	seed, err := m.optimal.PickSeed(betMode, m.core)
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

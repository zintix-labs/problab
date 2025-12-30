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

package problab

import (
	"crypto/rand"
	"math"
	"math/big"
	"sync"

	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/spec"
)

// Machine 封裝一台「可對外提供 Spin」的遊戲機台。
//
// 你可以把 Machine 視為 Game 的「外殼（shell）」：
//   - 對外：提供 Spin 入口（HTTP/模擬器通常只操作 Machine）。
//   - 對內：持有 RNG（Core）與真正執行遊戲邏輯的核心（sdk/slot.Game）。
//
// 並發語意：
//   - Machine 預設不是 lock-free 結構；它內含可重用的 request/result buffer（熱路徑），因此同一台 Machine 不應被多 goroutine 同時 Spin。
//   - 若要併發模擬，由更高層建立多台 Machine 分散到不同 worker 並管理其生命週期。
//
// Buffer 語意（非常重要，影響 DX 與正確性）：
//   - SpinRequest / SpinResult 會被重用（避免 GC），每次 Spin 會覆寫內容。
//   - 你若需要在 Spin 後保留結果，請在離開臨界區前轉成 DTO（或自行 copy 你需要的欄位）。
//
// initseed 用於記錄出生時的 seed（追溯/重現的基礎資訊）；完整審計仍以 Core 的 Snapshot/Restore 為準。
type Machine struct {
	gameName    string           // 遊戲名稱（來自 GameSetting.GameName，主要用於觀測/日誌）
	gameId      spec.GID         // 遊戲 ID（Catalog 內唯一；用於路由與查表）
	core        *core.Core       // RNG 核心（PRNG + Snapshot/Restore 合約；熱路徑會頻繁取樣）
	gh          *slot.Game       // 遊戲執行核心（Slot 邏輯入口；由 LogicRegistry + GameSetting 組裝）
	BetUnits    []int            // 押注單位（由遊戲設定衍生；通常給外部列舉 UI/測試）
	SpinRequest *buf.SpinRequest // 可重用的請求 buffer（每次 Spin 會覆寫/填充）
	SpinResult  *buf.SpinResult  // 可重用的結果 buffer（熱路徑；每次 Spin 會覆寫）
	mu          sync.Mutex       // 防併發鎖：保護可重用 buffers 與核心狀態一致性
	initseed    int64            // 出生 seed（便於追溯；完整重現請用 Snapshot/Restore）
}

// newMachine 以「隨機 seed」建立 Machine。
//
// 這裡使用 crypto/rand 產生 seed 是為了：
//   - 在對外服務情境避免可預測 RNG
//   - 同時保留可追溯性（seed 會被記錄在 Machine.initseed）
//
// seed 只保證了新建的Machine起點，如果需要在任意局後將機台"重設"到任意Core節點，請利用Snapshot Restore來操作
func newMachine(gs *spec.GameSetting, reg *slot.LogicRegistry, cf core.PRNGFactory, isSim bool) (*Machine, error) {
	seed, err := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
	if err != nil {
		return nil, errs.Wrap(err, "new crypto seed error in go std lib")
	}
	return newMachineWithSeed(gs, reg, cf, seed.Int64(), isSim)
}

// newMachineWithSeed 以指定 seed 建立 Machine。
//
// 這是最常用的「可重現」入口：同一份 GameSetting + 同一個 seed，應能得到一致的隨機序列（取決於 Core 實作）。
//
// 建立流程（概念）：
//  1. core.New(cf.NewWithSeed(seed)) 建出 RNG 核心
//  2. slot.NewGame(gs, reg, core, isSim) 依設定 + registry 建出 Slot 遊戲執行核心
//  3. 初始化 Machine 需要的 buffers（SpinRequest/SpinResult）
func newMachineWithSeed(gs *spec.GameSetting, reg *slot.LogicRegistry, cf core.PRNGFactory, seed int64, isSim bool) (*Machine, error) {
	m := &Machine{
		gameName:    gs.GameName,
		gameId:      spec.GID(gs.GameID),
		core:        core.New(cf.New(seed)),
		gh:          nil,
		BetUnits:    nil,
		SpinRequest: nil,
		SpinResult:  nil,
		initseed:    seed,
	}
	var err error
	m.gh, err = slot.NewGame(gs, reg, m.core, isSim)
	if err != nil {
		return nil, err
	}
	m.BetUnits = m.gh.BetUnits
	m.SpinRequest = &buf.SpinRequest{}
	m.SpinResult = m.gh.SpinResult
	return m, nil
}

// Spin 為主要公開入口，會驗證投注請求，執行遊戲並回傳Spin結果。
func (m *Machine) Spin(r *buf.SpinRequest) (dto.SpinResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. 校驗請求合法性
	if err := m.valid(r); err != nil {
		// 實作err代碼
		return dto.SpinResult{}, err
	}

	// 2. 取得spinResult
	sr := m.gh.GetResult(r)

	// 3. dto
	d := dto.NewSpinResultDTO(sr)
	return d, nil
}

// SpinInternal 直接取得內部 SpinResult；常用於模擬器或測試
//
// 此行為跳過所有檢查，並只使用預設1單位下注
func (m *Machine) SpinInternal(betMode int) *buf.SpinResult {
	m.SpinRequest.BetMode = betMode
	m.SpinRequest.BetMult = 1
	m.SpinRequest.Bet = m.BetUnits[betMode]
	return m.gh.GetResult(m.SpinRequest)
}

func (m *Machine) valid(req *buf.SpinRequest) error {
	if m.gameId != req.GameId {
		return errs.NewWarn("game id is not matched")
	}
	if m.gameName != req.GameName {
		return errs.NewWarn("game name is not matched")
	}
	if req.BetMode < 0 || req.BetMode >= len(m.BetUnits) {
		return errs.NewWarn("bet mode out of range")
	}
	// 要第一次下注才判斷，第二次以後的選擇請求Bet要帶0
	if req.BetMult*m.BetUnits[req.BetMode] != req.Bet {
		return errs.NewWarn("error bet value")
	}
	return nil
}

// SnapshotCore 取得Core狀態暫存 當前僅提供取得Core狀態
//
// 之後要實作斷手重連時候提供checkpoint加入必要恢復資訊時實作
// SnapShot() <- 保留語意
func (m *Machine) SnapshotCore() ([]byte, error) {
	return m.core.Snapshot()
}

// RestoreCore 恢復Core狀態暫存 當前僅提供恢復Core狀態
//
// 之後要實作斷手重連時候提供checkpoint加入必要恢復資訊時實作
// Restore() <- 保留語意
func (m *Machine) RestoreCore(src []byte) error {
	return m.core.Restore(src)
}

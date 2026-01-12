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
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"math/big"
	"sync"

	"github.com/klauspost/compress/zstd"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/sampler"
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
// Gacha 表示優化後的抽樣結構（對應 optimizer.Gacha）。
// 為了避免循環導入，這裡定義一個簡化版本。
type Gacha struct {
	Picker  *sampler.AliasTable `json:"picker"`   // 抽樣表
	SeedLen int                 `json:"seed_len"` // 抽到對應第幾個種子，就要 * SeedLen 取[n*SeedLen:(n+1)*SeedLen]
}

// Pick 從 Gacha 中抽取一個索引範圍。
func (g *Gacha) Pick(c *core.Core) (start int, end int) {
	s := g.Picker.Pick(c)
	start = s * g.SeedLen
	end = start + g.SeedLen
	return
}

// OptimalRuntime 存儲優化運行時數據（Gacha 和 SeedBank）。
// 每個 betmode 對應一個 Gacha 和一個 SeedBank。
type OptimalRuntime struct {
	Gachas []*Gacha // 對應每個 betmode，len(Gachas) == len(BetUnits)
	Bank   [][]byte // 對應每個 betmode，每個 []byte 是完整的 SeedBank
}

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
	optimal     *OptimalRuntime  // 優化運行時數據（nil 表示未啟用優化）
}

// newMachine 以「隨機 seed」建立 Machine。
//
// 這裡使用 crypto/rand 產生 seed 是為了：
//   - 在對外服務情境避免可預測 RNG
//   - 同時保留可追溯性（seed 會被記錄在 Machine.initseed）
//
// seed 只保證了新建的Machine起點，如果需要在任意局後將機台"重設"到任意Core節點，請利用Snapshot Restore來操作
func newMachine(gs *spec.GameSetting, reg *slot.LogicRegistry, cf core.PRNGFactory, isSim bool, optimalFS fs.FS) (*Machine, error) {
	seed, err := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
	if err != nil {
		return nil, errs.Wrap(err, "new crypto seed error in go std lib")
	}
	return newMachineWithSeed(gs, reg, cf, seed.Int64(), isSim, optimalFS)
}

// newMachineWithSeed 以指定 seed 建立 Machine。
//
// 這是最常用的「可重現」入口：同一份 GameSetting + 同一個 seed，應能得到一致的隨機序列（取決於 Core 實作）。
//
// 建立流程（概念）：
//  1. core.New(cf.NewWithSeed(seed)) 建出 RNG 核心
//  2. slot.NewGame(gs, reg, core, isSim) 依設定 + registry 建出 Slot 遊戲執行核心
//  3. 初始化 Machine 需要的 buffers（SpinRequest/SpinResult）
//  4. 如果啟用優化（UseOptimal = true），從 optimalFS 加載 Gacha 和 SeedBank
func newMachineWithSeed(gs *spec.GameSetting, reg *slot.LogicRegistry, cf core.PRNGFactory, seed int64, isSim bool, optimalFS fs.FS) (*Machine, error) {
	m := &Machine{
		gameName:    gs.GameName,
		gameId:      spec.GID(gs.GameID),
		core:        core.New(cf.New(seed)),
		gh:          nil,
		BetUnits:    nil,
		SpinRequest: nil,
		SpinResult:  nil,
		initseed:    seed,
		optimal:     nil,
	}
	var err error
	m.gh, err = slot.NewGame(gs, reg, m.core, isSim)
	if err != nil {
		return nil, err
	}
	m.BetUnits = m.gh.BetUnits
	m.SpinRequest = &buf.SpinRequest{}
	m.SpinResult = m.gh.SpinResult

	// 如果啟用優化，加載 Gacha 和 SeedBank
	if gs.OptimalSetting.UseOptimal && optimalFS != nil {
		optimal, err := loadOptimalRuntime(gs, optimalFS)
		if err != nil {
			return nil, err
		}
		m.optimal = optimal
	}

	return m, nil
}

// Spin 為主要公開入口，會驗證投注請求，執行遊戲並回傳Spin結果。
func (m *Machine) Spin(r *dto.SpinRequest) (dto.SpinResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. 校驗請求合法性
	if err := m.valid(r); err != nil {
		// 實作err代碼
		return dto.SpinResult{}, err
	}
	// 2. parse dto to inner spin request
	req, err := r.Parse(m.gh.GameSetting.LogicKey)
	if err != nil {
		return dto.SpinResult{}, err
	}

	// 2.5. 優化邏輯：如果新局且啟用優化，從 Gacha 中 Pick 並設置 StartCoreSnap
	if req.StartState == nil || len(req.StartState.StartCoreSnap) == 0 {
		// 新局，且外部沒有指定 StartCoreSnap
		if m.optimal != nil {
			// 有開啟優化
			betMode := r.BetMode
			if betMode < 0 || betMode >= len(m.optimal.Gachas) {
				return dto.SpinResult{}, errs.NewWarn(fmt.Sprintf("bet_mode %d out of range for optimal (max: %d)", betMode, len(m.optimal.Gachas)-1))
			}

			gacha := m.optimal.Gachas[betMode]
			bank := m.optimal.Bank[betMode]

			// Pick 出 start 和 end
			start, end := gacha.Pick(m.core)

			// 邊界檢查
			if start < 0 || end > len(bank) || start >= end {
				return dto.SpinResult{}, errs.NewWarn(fmt.Sprintf("invalid gacha pick range: start=%d, end=%d, bank_len=%d", start, end, len(bank)))
			}

			// 設置到 StartState
			if req.StartState == nil {
				req.StartState = &buf.StartState{}
			}
			req.StartState.StartCoreSnap = bank[start:end]
		}
	}

	// 3. get start snapshot
	startsnap, err := m.SnapshotCore()
	if err != nil {
		return dto.SpinResult{}, errs.NewFatal("before snapshot error " + err.Error())
	}
	rem := startsnap
	if req.StartState != nil && len(req.StartState.StartCoreSnap) != 0 {
		startsnap = req.StartState.StartCoreSnap
		if err := m.RestoreCore(req.StartState.StartCoreSnap); err != nil {
			return dto.SpinResult{}, errs.NewWarn("restore core err " + err.Error())
		}
	}

	// 4. get inner spinResult
	sr := m.gh.GetResult(req)

	// 5. get after snapshot
	aftersnap, err := m.SnapshotCore()
	if err != nil {
		if e := m.RestoreCore(rem); e != nil {
			return dto.SpinResult{}, errs.NewFatal("fall back err " + e.Error())
		}
		return dto.SpinResult{}, errs.NewWarn("after snapshot error " + err.Error())
	}
	state := sr.State
	state.StartCoreSnap = startsnap
	state.AfterCoreSnap = aftersnap

	// 6. restore if needed
	if req.StartState != nil && len(req.StartState.StartCoreSnap) != 0 {
		if err := m.RestoreCore(rem); err != nil {
			return dto.SpinResult{}, errs.NewFatal("restore core back err " + err.Error())
		}
	}

	// 7. dto
	return dto.NewSpinResultDTO(sr)
}

// SpinInternal 直接取得內部 SpinResult；常用於模擬器或測試
//
// 請勿在正式環境使用
//
// 此行為跳過所有檢查，並只使用預設1單位下注
// 如果啟用優化，會從 Gacha 中 Pick 種子並先設置 Core 狀態
func (m *Machine) SpinInternal(betMode int) *buf.SpinResult {
	// 優化邏輯：如果啟用優化，從 Gacha 中 Pick 並設置 Core
	if m.optimal != nil {
		if betMode >= 0 && betMode < len(m.optimal.Gachas) {
			gacha := m.optimal.Gachas[betMode]
			bank := m.optimal.Bank[betMode]

			// Pick 出 start 和 end
			start, end := gacha.Pick(m.core)
			snap, _ := m.SnapshotCore()
			defer func() { _ = m.RestoreCore(snap) }()

			// 邊界檢查
			if start >= 0 && end <= len(bank) && start < end {

				// 設置 Core 狀態
				_ = m.RestoreCore(bank[start:end])
			}
		}
	}

	m.SpinRequest.BetMode = betMode
	m.SpinRequest.BetMult = 1
	m.SpinRequest.Bet = m.BetUnits[betMode]
	return m.gh.GetResult(m.SpinRequest)
}

func (m *Machine) valid(req *dto.SpinRequest) error {
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

// loadGacha 從 optimalFS 加載 Gacha 文件（.json.zst 格式）。
func loadGacha(optimalFS fs.FS, path string) (*Gacha, error) {
	if optimalFS == nil {
		return nil, errs.NewWarn("optimalFS is nil")
	}
	if path == "" {
		return nil, errs.NewWarn("gacha path is empty")
	}

	// 讀取壓縮文件
	compressed, err := fs.ReadFile(optimalFS, path)
	if err != nil {
		return nil, errs.Wrap(err, "read gacha file failed")
	}

	// 解壓 zstd
	zr, err := zstd.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, errs.Wrap(err, "create zstd reader failed")
	}
	defer zr.Close()

	// 讀取解壓後的 JSON
	jsonBytes, err := io.ReadAll(zr)
	if err != nil {
		return nil, errs.Wrap(err, "read decompressed data failed")
	}

	// 解析 JSON
	var gacha Gacha
	if err := json.Unmarshal(jsonBytes, &gacha); err != nil {
		return nil, errs.Wrap(err, "unmarshal gacha json failed")
	}

	// 驗證
	if gacha.Picker == nil {
		return nil, errs.Warnf("gacha: Picker is required")
	}
	if gacha.SeedLen <= 0 {
		return nil, errs.Warnf("gacha: SeedLen must be > 0")
	}

	return &gacha, nil
}

// loadSeedBank 從 optimalFS 加載 SeedBank 文件（.bin 格式，純 []byte）。
func loadSeedBank(optimalFS fs.FS, path string) ([]byte, error) {
	if optimalFS == nil {
		return nil, errs.NewWarn("optimalFS is nil")
	}
	if path == "" {
		return nil, errs.NewWarn("seed_bank path is empty")
	}

	bank, err := fs.ReadFile(optimalFS, path)
	if err != nil {
		return nil, errs.Wrap(err, "read seed_bank file failed")
	}

	return bank, nil
}

// loadOptimalRuntime 從 optimalFS 加載優化運行時數據。
func loadOptimalRuntime(gs *spec.GameSetting, optimalFS fs.FS) (*OptimalRuntime, error) {
	opt := gs.OptimalSetting

	// 校驗：gachas 和 seed_bank 數量必須等於 BetUnits 數量
	if len(opt.Gachas) != len(gs.BetUnits) {
		return nil, errs.NewFatal(fmt.Sprintf("gachas count (%d) must match bet_units count (%d)", len(opt.Gachas), len(gs.BetUnits)))
	}
	if len(opt.SeedBank) != len(gs.BetUnits) {
		return nil, errs.NewFatal(fmt.Sprintf("seed_bank count (%d) must match bet_units count (%d)", len(opt.SeedBank), len(gs.BetUnits)))
	}

	optimal := &OptimalRuntime{
		Gachas: make([]*Gacha, len(gs.BetUnits)),
		Bank:   make([][]byte, len(gs.BetUnits)),
	}

	// 加載每個 betmode 的 Gacha 和 SeedBank
	for i := range gs.BetUnits {
		// 加載 Gacha
		gacha, err := loadGacha(optimalFS, opt.Gachas[i])
		if err != nil {
			return nil, errs.Wrap(err, fmt.Sprintf("load gacha[%d] (%s) failed", i, opt.Gachas[i]))
		}
		optimal.Gachas[i] = gacha

		// 加載 SeedBank
		bank, err := loadSeedBank(optimalFS, opt.SeedBank[i])
		if err != nil {
			return nil, errs.Wrap(err, fmt.Sprintf("load seed_bank[%d] (%s) failed", i, opt.SeedBank[i]))
		}
		optimal.Bank[i] = bank
	}

	return optimal, nil
}

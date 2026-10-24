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
	"sync"

	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/internal/optimalrt"
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
//   - Machine 預設不是 lock-free 結構；它持有可重用的請求、結果與快照緩衝區（熱路徑），因此同一台 Machine 不應被多 goroutine 同時執行。
//   - Spin 會上鎖；SpinUnsafe 不上鎖，呼叫端必須保證獨占同一台 Machine。
//   - 若要併發模擬，由更高層建立多台 Machine 分散到不同 worker 並管理其生命週期。
//
// Buffer 語意（非常重要，影響 DX 與正確性）：
//   - Machine 持有交給遊戲的請求，每次執行前重設；遊戲不得保留請求的參照。
//   - 結果與快照緩衝區會被重用（避免 GC），每次執行會覆寫內容。
//   - 你若需要在執行後保留結果，請在下一次呼叫前轉成 DTO（或自行 copy 你需要的欄位）。
//
// 錯誤語意：可預期的輸入錯誤以 error 回傳；內部執行故障以 panic 呈現，由最外層執行邊界
// （MachinePool，或離線工具自己的工作迴圈）處理。RNG 恢復失敗後 Machine 不可再用；遊戲
// panic 後，即使 RNG 已還原，Machine 也不保證可以繼續使用。
//
// Gacha is the legacy JSON compatibility shape for optimizer.Gacha.
// Deprecated: runtime Machines use the immutable internal Artifact instead.
type Gacha struct {
	Picker  *sampler.AliasTableF64 `json:"picker"`   // 抽樣表
	SeedLen int                    `json:"seed_len"` // 抽到對應第幾個種子，就要 * SeedLen 取[n*SeedLen:(n+1)*SeedLen]
}

// Pick 從 Gacha 中抽取一個索引範圍。
func (g *Gacha) Pick(c *core.Core) (start int, end int) {
	s := g.Picker.Pick(c)
	start = s * g.SeedLen
	end = start + g.SeedLen
	return
}

// OptimalRuntime is retained for source compatibility with the legacy public
// shape. Deprecated: Problab now owns one immutable shared Artifact per bundle.
type OptimalRuntime struct {
	Gachas []*Gacha // 對應每個 betmode，len(Gachas) == len(BetUnits)
	Bank   [][]byte // 對應每個 betmode，每個 []byte 是完整的 SeedBank
}

type Machine struct {
	// 請求與快照緩衝區由 Machine 持有，借用到下一次呼叫為止。
	// restoreBuf、reqSnap、seedBuf、startBuf、afterBuf 必須是彼此獨立的底層陣列：
	// 第三方 PRNG 的 snapshot 可能重用自己的緩衝區，artifact 的 bank 則是唯讀的共用資料。
	spinRequest   buf.SpinRequest // 交給遊戲的請求；每次執行前以 resetSpinRequest 重設
	reqStart      buf.StartState  // spinRequest.StartState 有內容時指向它
	reqSnap       []byte          // Spin 交給遊戲、也作為結果起始快照的起點（外部快照或抽出的 seed）
	seedBuf       []byte          // SpinUnsafe 抽出的 seed
	restoreBuf    []byte          // 恢復點
	startBuf      []byte          // 不指定起點時擷取的起始快照
	afterBuf      []byte          // 結束快照
	fallbackState buf.SpinState   // 遊戲回傳的結果 State 為 nil 時使用
	faulted       bool            // RNG 恢復失敗後為 true；Spin 會主動檢查並 panic，SpinUnsafe 不檢查（由呼叫端棄用機台）

	gameName string              // 遊戲名稱（來自 GameSetting.GameName，主要用於觀測/日誌）
	gameId   spec.GID            // 遊戲 ID（Catalog 內唯一；用於路由與查表）
	core     *core.Core          // RNG 核心（PRNG + Snapshot/Restore 合約；熱路徑會頻繁取樣）
	gh       *slot.Game          // 遊戲執行核心（Slot 邏輯入口；由 LogicRegistry + GameSetting 組裝）
	BetUnits []int               // 押注單位（由遊戲設定衍生；通常給外部列舉 UI/測試）
	mu       sync.Mutex          // 防併發鎖：保護可重用 buffers 與核心狀態一致性
	rngMode  rngMode             // deterministic 或 production new-cycle reseed lifecycle
	poolSlot uint64              // MachinePool 的穩定 slot identity；非 pool Machine 為 0
	poolGen  uint64              // 此 slot 的重建 generation；非 pool Machine 為 0
	optimal  *optimalrt.Artifact // Problab instance 共用的不可變優化資料
}

type rngMode uint8

const (
	rngDeterministic rngMode = iota
	rngProduction
)

// newMachine 是 Machine 的私有建構核心：所有建構入口（公開入口、
// Simulator worker、MachinePool、測試輔助函式）最後都經由它建立 Machine。
//
// 它不推導政策：RNG 政策（mode）與 Extend 省略政策（omitExtend）由呼叫者決定，
// 呼叫者也負責維持取設定、resolveOptimal、產生 seed 的先後順序。
//
// 建立流程：
//  1. 檢查不變條件：production 必須保留 Extend（正式回應需要完整 Extend）
//  2. cf.New(seed) + core.New(...) 建出 RNG 核心
//  3. slot.NewGame(gs, reg, core, omitExtend) 依設定 + registry 建出 Slot 遊戲執行核心
//  4. 填好請求基底（GameId、GameName）
//  5. 如果啟用優化（UseOptimal = true），使用 Problab 預先解析的共用 Artifact
func newMachine(gs *spec.GameSetting, reg *slot.LogicRegistry, cf core.PRNGFactory, seed []byte, omitExtend bool, optimal *optimalrt.Artifact, mode rngMode) (*Machine, error) {
	if mode == rngProduction && omitExtend {
		return nil, errs.NewFatal("production machine must not omit Extend")
	}
	seed = append([]byte(nil), seed...)
	rng, err := cf.New(seed)
	if err != nil {
		return nil, errs.Wrap(err, "create PRNG")
	}
	if rng == nil {
		return nil, errs.NewFatal("core factory returned nil PRNG")
	}
	m := &Machine{
		gameName: gs.GameName,
		gameId:   spec.GID(gs.GameID),
		core:     core.New(rng),
		rngMode:  mode,
		optimal:  optimal,
	}
	m.gh, err = slot.NewGame(gs, reg, m.core, omitExtend)
	if err != nil {
		return nil, err
	}
	m.BetUnits = m.gh.BetUnits
	m.resetSpinRequest()

	if gs.OptimalSetting.UseOptimal && optimal == nil {
		return nil, errs.NewFatal("resolved optimal artifact is required when use_optimal=true")
	}

	return m, nil
}

// Spin 為主要公開入口，會驗證投注請求，執行遊戲並回傳Spin結果。
//
// 可預期的輸入錯誤（請求驗證、DTO 解析、被 PRNG 拒絕的外部起始快照）回傳 errs.Warn；
// production reseed 失敗回傳 errs.Fatal。其他內部執行故障以 panic 呈現，由 MachinePool
// 汰換機台；RNG 恢復失敗後 Machine 不可再用。
func (m *Machine) Spin(r *dto.SpinRequest) (dto.SpinResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkUsable()
	m.resetSpinRequest()

	// 1. 校驗請求合法性
	if err := m.valid(r); err != nil {
		return dto.SpinResult{}, err
	}
	// 2. 解析 DTO，填入 Machine 持有的請求（GameId、GameName 保留機台值）
	req, err := r.Parse(m.gh.GameSetting.LogicKey)
	if err != nil {
		return dto.SpinResult{}, err
	}
	q := &m.spinRequest
	q.UID, q.Bet, q.BetMode, q.BetMult = req.UID, req.Bet, req.BetMode, req.BetMult
	q.Cycle, q.Choice, q.HasChoice = req.Cycle, req.Choice, req.HasChoice
	if req.StartState != nil {
		m.reqSnap = append(m.reqSnap[:0], req.StartState.StartCoreSnap...)
		m.reqStart = buf.StartState{StartCoreSnap: m.reqSnap, Checkpoint: req.StartState.Checkpoint}
		q.StartState = &m.reqStart
	}

	// 3. 選擇起點：外部快照（回放）；否則 production 先 reseed，optimal 開啟時抽庫
	kind, start := startNone, []byte(nil)
	var startSnap []byte
	if req.StartState != nil && len(req.StartState.StartCoreSnap) != 0 {
		kind, start = startExternal, m.reqSnap
	} else {
		if m.rngMode == rngProduction {
			if err := m.core.Reseed(); err != nil {
				return dto.SpinResult{}, errs.NewFatal("reseed PRNG before new game cycle: " + err.Error())
			}
		}
		if m.optimal != nil {
			// 先把 seed 填進請求，再記錄恢復點（與既有順序相同）。
			m.reqSnap = m.pickSeed(q.BetMode, &m.reqSnap)
			m.reqStart.StartCoreSnap = m.reqSnap
			q.StartState = &m.reqStart
			kind, start = startSeed, m.reqSnap
		} else {
			startSnap = m.snapshotInto(&m.startBuf, "start state")
		}
	}

	// 4. 準備起點並執行
	g, formatErr := m.enterStart(kind, start)
	if formatErr != nil {
		return dto.SpinResult{}, errs.NewWarn("restore core err " + formatErr.Error())
	}
	defer g.finish()
	if kind != startNone {
		startSnap = start
	}
	sr := m.spin()
	sr.Cycle = m.spinRequest.Cycle // 段號由引擎填入，遊戲寫入的值不保留
	afterSnap := m.snapshotInto(&m.afterBuf, "after state")
	state := sr.State
	state.StartCoreSnap = startSnap
	state.AfterCoreSnap = afterSnap

	// 5. 轉成 DTO（複製），在恢復守衛還原 RNG 之前完成
	return dto.NewSpinResultDTO(sr)
}

// UsesOptimal reports the loaded runtime source, not merely a config flag.
func (m *Machine) UsesOptimal() bool { return m.optimal != nil }

// OmitExtend 回傳此 Machine 的 Extend 省略政策（true 表示結果不含 Extend）。
func (m *Machine) OmitExtend() bool { return m.gh.OmitExtend() }

// SpinInput 是 SpinUnsafe 的輸入。呼叫端應使用帶欄位名的 literal，之後新增欄位時
// 不必修改；新增欄位的零值等於新增前的行為。
type SpinInput struct {
	BetMode          int  // 必須在 Machine 的 bet mode 範圍內
	BetMult          int  // 必須 >= 1；零值不合法。下注單位 × BetMult 不得溢位
	CaptureSnapshots bool // 是否把起始／結束 RNG 快照寫進結果的 State
}

// SpinUnsafe 是本機執行入口：一次執行一段（目前所有遊戲一段就是一局），回傳借用的
// 結果，到同一台 Machine 的下一次呼叫為止；需要保留的呼叫端自行複製（轉 DTO 即是複製）。
//
// Unsafe 指的是呼叫端責任，不是記憶體安全：
//   - 不驗證輸入。BetMode 在範圍內、BetMult >= 1、投注額乘法不溢位由呼叫端保證；
//     不合法的輸入行為未定義（例如 bet mode 越界時 panic）。
//   - 不上鎖。呼叫期間由呼叫端獨占 Machine，不得與 Spin、RestoreCore、SnapshotCore
//     等任何操作並行；同步完成後交給另一個 goroutine 是允許的。
//   - 不執行 production reseed，只供呼叫端已掌握 RNG 生命週期的本機執行使用；
//     正式服務使用 Spin。
//
// 起點：optimal 開啟時抽庫並在執行後還原選取狀態；未啟用時從機台目前的 RNG 執行。
// 因此只有在不會抽庫的機台上，RestoreCore 之後呼叫才等於從指定快照回放。
//
// CaptureSnapshots 為 true 時，把起始快照（抽庫時為抽出的 seed，否則為執行前的機台
// 狀態）與結束快照寫進結果的 State，快照借用 Machine 的緩衝區；為 false 時不擷取，
// 並清除結果 State 中前一次留下的 RNG 快照（不碰遊戲輸出的 Checkpoint）。
//
// 遊戲回傳 nil 時原樣回傳，不觸碰 State；RNG 照常還原。
//
// 內部執行故障以 panic 呈現。發生任何內部執行 panic 後，該 Machine 必須棄用：
// SpinUnsafe 不檢查 Machine 是否曾經 RNG 恢復失敗，接住 panic 後繼續使用屬於違反契約。
func (m *Machine) SpinUnsafe(in SpinInput) *buf.SpinResult {
	// 一次寫入整份請求。reqStart、reqSnap 只經由 StartState 交給遊戲，這裡的
	// StartState 為 nil，所以不必重設；Spin 每次開始時會自行重設它們。
	m.spinRequest = buf.SpinRequest{
		GameId: m.gameId, GameName: m.gameName,
		Bet: m.BetUnits[in.BetMode] * in.BetMult, BetMode: in.BetMode, BetMult: in.BetMult,
	}
	if m.optimal != nil {
		return m.spinUnsafeSeeded(in)
	}
	// 不指定起點：沒有恢復點，不需要恢復守衛。
	var startSnap []byte
	if in.CaptureSnapshots {
		startSnap = m.snapshotInto(&m.startBuf, "start state")
	}
	return m.finishUnsafe(m.spin(), in.CaptureSnapshots, startSnap)
}

// spinUnsafeSeeded 是 SpinUnsafe 的抽庫路徑：抽出的 seed 即起始快照，執行後由恢復守衛
// 還原選取狀態。
func (m *Machine) spinUnsafeSeeded(in SpinInput) *buf.SpinResult {
	start := m.pickSeed(in.BetMode, &m.seedBuf)
	g, _ := m.enterStart(startSeed, start) // startSeed 不會回傳格式錯誤
	defer g.finish()
	return m.finishUnsafe(m.spin(), in.CaptureSnapshots, start)
}

// finishUnsafe 以請求的 Cycle 覆寫結果的段號，並依 capture 擷取或清除結果的 RNG 快照；
// 必須在恢復守衛還原之前執行。
func (m *Machine) finishUnsafe(result *buf.SpinResult, capture bool, startSnap []byte) *buf.SpinResult {
	if result == nil {
		return nil
	}
	result.Cycle = m.spinRequest.Cycle // 段號由引擎填入，遊戲寫入的值不保留
	if !capture {
		if result.State != nil {
			result.State.StartCoreSnap = nil
			result.State.AfterCoreSnap = nil
		}
		return result
	}
	afterSnap := m.snapshotInto(&m.afterBuf, "after state")
	if result.State == nil {
		m.fallbackState = buf.SpinState{}
		result.State = &m.fallbackState
	}
	result.State.StartCoreSnap = startSnap
	result.State.AfterCoreSnap = afterSnap
	return result
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

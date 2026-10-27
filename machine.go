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
	"errors"
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
	spinRequest   buf.SpinRequest // 交給遊戲的請求；每次執行前以 fillSegment 整份寫入
	reqChooseNext []int           // spinRequest.ChooseNext 的底層陣列（輸入的複製）
	reqSnap       []byte          // 本段的指定起始快照（外部快照或 SpinInput.StartSnap），不交給遊戲
	seedBuf       []byte          // 抽出的 seed
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

// Spin 為主要公開入口，會驗證投注請求，執行一段遊戲並回傳該段的結果。
//
// 可預期的輸入錯誤回傳 errs.Warn：請求驗證、DTO 解析、被 PRNG 拒絕的外部起始快照、
// 違反結構不變量的續段請求（ErrSegmentInput）、續段的入口沒有配置在 optimal 設定中
// （ErrContinuationUnsupported），以及遊戲拒絕請求（ErrGameRejected）。後三者可以
// errors.Is 判斷。production reseed 失敗回傳 errs.Fatal。其他內部執行故障以 panic 呈現，
// 由 MachinePool 汰換機台；RNG 恢復失敗後 Machine 不可再用。
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
	p, err := r.Parse(m.gh.GameSetting.LogicKey)
	if err != nil {
		return dto.SpinResult{}, err
	}
	m.fillSegment(r.BetMode, r.BetMult, p.Cycle, p.Choice, p.ChooseNext, p.Checkpoint, p.StartSnap)

	// 3. 執行一段：外部快照（回放）；否則 production 先 reseed，optimal 開啟時抽庫
	sr, err := m.execSegment(segmentOpts{
		external:    len(p.StartSnap) != 0,
		reseed:      m.rngMode == rngProduction,
		capture:     true,
		checkOutput: true,
	})
	if err != nil {
		return dto.SpinResult{}, spinError(err)
	}

	// 4. 轉成 DTO（複製），補上入口回填的欄位
	res, err := dto.NewSpinResultDTO(sr)
	if err != nil {
		return res, err
	}
	res.BetID = p.BetID
	if p.HasChoice {
		choice := p.Choice
		res.Choice = &choice
	}
	return res, nil
}

// spinError 把單段執行的錯誤轉成 Spin 的錯誤。既有的錯誤維持原本的訊息與等級；新增的
// 拒絕以 errs.Warn 承載，Cause 為可以 errors.Is 判斷的 sentinel。
func spinError(err error) error {
	var rejected *startRejected
	switch {
	case errors.As(err, &rejected):
		return errs.NewWarn("restore core err " + rejected.err.Error())
	case errors.Is(err, ErrSegmentInput), errors.Is(err, ErrContinuationUnsupported), errors.Is(err, ErrGameRejected):
		return &errs.E{ErrLv: errs.Warn, Message: "spin request rejected", Cause: err}
	default:
		return err
	}
}

// UsesOptimal reports whether optimal routing is on for this Machine, from the
// loaded runtime source rather than the config flag. Some segments may still
// run on the machine RNG when their slot has no gacha.
func (m *Machine) UsesOptimal() bool { return m.optimal != nil }

// OmitExtend 回傳此 Machine 的 Extend 省略政策（true 表示結果不含 Extend）。
func (m *Machine) OmitExtend() bool { return m.gh.OmitExtend() }

// 單段執行的拒絕原因。執行前的拒絕（ErrSegmentInput、ErrStartSnapshotRejected、
// ErrContinuationUnsupported）不改變 RNG 與 Machine 狀態；ErrGameRejected 發生在遊戲執行
// 之後，RNG 可能已消耗（指定快照或抽庫時由恢復守衛還原）。
var (
	// ErrSegmentInput 表示輸入違反結構不變量：Cycle < 0；Cycle 為 0 卻帶選項或 Choice；
	// Cycle > 0 卻沒有選項，或 Choice 不在 [0, len(ChooseNext)) 內。
	ErrSegmentInput = errors.New("invalid segment input")
	// ErrStartSnapshotRejected 表示指定的起始快照被 PRNG 拒絕，且已還原到執行前。
	ErrStartSnapshotRejected = errors.New("start snapshot rejected")
	// ErrContinuationUnsupported 表示機台開啟 optimal、未指定快照而請求續段，但入口
	// ChooseNext[Choice] 沒有配置在這個 bet mode 的 after 中（含沒有 after 的設定）。
	ErrContinuationUnsupported = errors.New("continuation is not supported by the optimal setting")
	// ErrGameRejected 表示遊戲回傳 nil，拒絕了這次請求（例如不合法的入口或 checkpoint）。
	ErrGameRejected = errors.New("game rejected the request")
)

// SpinInput 是 SpinUnsafe 的輸入。呼叫端應使用帶欄位名的 literal，之後新增欄位時
// 不必修改；新增欄位的零值等於新增前的行為。
type SpinInput struct {
	BetMode          int  // 必須在 Machine 的 bet mode 範圍內
	BetMult          int  // 必須 >= 1；零值不合法。下注單位 × BetMult 不得溢位
	CaptureSnapshots bool // 是否把起始／結束 RNG 快照寫進結果的 State

	Cycle      int    // 第幾段，0 為新局
	Choice     int    // ChooseNext 的位置索引；Cycle 為 0 時必須為 0
	ChooseNext []int  // 上一段結果的 ChooseNext；Cycle 為 0 時必須為空。Machine 會複製
	Checkpoint any    // 上一段結果的 checkpoint；遊戲註冊的型別
	StartSnap  []byte // 本段的指定起始快照，空表示不指定。Machine 會複製
}

// SpinUnsafe 是本機執行入口：一次執行一段，回傳借用的結果，到同一台 Machine 的下一次
// 呼叫為止；需要保留的呼叫端自行複製（轉 DTO 即是複製）。
//
// Unsafe 指的是呼叫端責任，不是記憶體安全：
//   - 跳過對外入口的完整請求驗證。BetMode 在範圍內、BetMult >= 1、投注額乘法不溢位由
//     呼叫端保證；不合法時行為未定義（例如 bet mode 越界時 panic）。
//   - 仍維護 Machine 執行所需的前置條件：違反結構不變量、指定的起始快照被拒絕、或目前的
//     optimal 設定不支援續段時回傳 nil，不執行遊戲、不消耗 RNG。
//   - 不上鎖。呼叫期間由呼叫端獨占 Machine，不得與 Spin、RestoreCore、SnapshotCore
//     等任何操作並行；同步完成後交給另一個 goroutine 是允許的。
//   - 不執行 production reseed，只供呼叫端已掌握 RNG 生命週期的本機執行使用；
//     正式服務使用 Spin。
//
// 起點：StartSnap 非空時從它執行本段（跳過抽庫），回傳後機台恢復到執行前，只作用於
// 這一次呼叫；否則 optimal 開啟時抽庫並在執行後還原選取狀態，未啟用時從機台目前的 RNG
// 執行。
//
// CaptureSnapshots 為 true 時，把起始快照（指定快照、抽出的 seed，或執行前的機台狀態）
// 與結束快照寫進結果的 State，快照借用 Machine 的緩衝區；為 false 時不擷取，並清除結果
// State 中前一次留下的 RNG 快照（不碰遊戲輸出的 Checkpoint）。
//
// 遊戲回傳 nil 時原樣回傳，不觸碰 State；RNG 照常還原。nil 一律表示這次呼叫沒有有效結果。
//
// 內部執行故障以 panic 呈現。發生任何內部執行 panic 後，該 Machine 必須棄用：
// SpinUnsafe 不檢查 Machine 是否曾經 RNG 恢復失敗，接住 panic 後繼續使用屬於違反契約。
func (m *Machine) SpinUnsafe(in SpinInput) *buf.SpinResult {
	m.fillSegment(in.BetMode, in.BetMult, in.Cycle, in.Choice, in.ChooseNext, in.Checkpoint, in.StartSnap)
	sr, err := m.execSegment(segmentOpts{external: len(in.StartSnap) != 0, capture: in.CaptureSnapshots})
	if err != nil {
		return nil
	}
	return sr
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
	// 續段沿用第 0 段的投注；只有第 0 段扣注，由呼叫端負責
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

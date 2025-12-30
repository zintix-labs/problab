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

package slot

import (
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/calc"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/gen"
	"github.com/zintix-labs/problab/spec"
)

// poolSize 是每個 GameModeHandler 預先配置的結果物件數量。
//
// 目的：避免每一局 spin 都 new 一個 *GameModeResult 造成 GC 壓力。
// 取捨：poolSize 越大，常態記憶體占用越高；越小，當連續 Yield 次數超過 poolSize
// 時就會進入「慢路徑」擴容（但仍然是分批擴容，而不是每次都分配）。
//
// 建議：
// - 純 Base Game 通常 1~2 次 Yield（或甚至 0 次），poolSize 可小。
// - 會在一次 spin 內多次產出 mode result（例如 base + free + bonus）時可提高。
const poolSize int = 3

// GameMode 是「單一 GameMode」在執行期的工作站 (runtime workstation)。
//
// 你可以把它理解成：
// - **ScreenGenerator**：負責「產生盤面」(Gen Screen)
// - **ScreenCalculator**：負責「算分 / 找中獎」(Calc)
// - **GameModeResult**：本局可寫的結果 buffer（可重用、零配置熱路徑）
// - **pool/pid**：結果物件池，支援在一次 spin 內多次 Yield 結果（例如 base + free + bonus）
//
// ### 使用者（數學家）應該怎麼用？
// 典型流程會長這樣：
//  1. 使用 gm.ScreenGenerator 產生盤面到 gm.GameModeResult
//  2. 使用 gm.ScreenCalculator 對 gm.GameModeResult 進行算分 / 填充命中資訊
//  3. 呼叫 gm.YieldResult() 取得「本次 mode 的最終結果指標」，交給上層組裝 SpinResult 或轉 DTO
//
// ### 合約（非常重要）
// - **非並行安全 (NOT thread-safe)**：一個 GameMode 只能在單一 goroutine/單一 spin 流程中使用。
// - **GameModeSetting 視為唯讀**：Init 完成後，不要在遊戲邏輯中修改 setting（特別是 slice/map）。
// - **GameModeResult 是可重用 buffer**：
//   - gm.GameModeResult 永遠指向「下一個要寫的 buffer」。
//   - 你呼叫 YieldResult() 取走的那個指標，在下一次 Yield/Reset 之後仍然有效（不會被 free），
//     但請把它當成「已完成、不可再由 gm 修改」的結果。
//   - **不要把 gm.GameModeResult 指標存起來跨越 Yield/Reset**，如果要保留就保留 YieldResult() 的回傳值。
type GameMode struct {
	// core 提供整個 engine 的核心依賴（例如 PRNG、共享 LUT、共用工具）。
	// 由上層建立並注入；GameModeHandler 不擁有其生命週期。
	core *core.Core

	// GameModeSetting 是此 mode 的規格設定（由 YAML/JSON 解析而來並 Init 完成）。
	// 合約：Init 完成後視為唯讀；不要在遊戲邏輯中修改其內容（尤其是 slice/map）。
	GameModeSetting *spec.GameModeSetting

	// ScreenGenerator 負責依照 GameModeSetting 生成盤面。
	// 合約：
	// - 由 handler 初始化，不要自行替換指標。
	// - 可能包含快取 / LUT / 內部狀態，僅在單一 goroutine 下使用。
	ScreenGenerator *gen.ScreenGenerator

	// ScreenCalculator 負責依照 GameModeSetting 對盤面算分、找中獎、填充結果。
	// 合約：同 ScreenGenerator（可能含內部 buffer），僅在單一 goroutine 下使用。
	ScreenCalculator *calc.ScreenCalculator

	// GameModeResult 指向「目前可寫」的結果 buffer。
	// - 每次 YieldResult() 會把當前 buffer 交出去，並把此指標切到 pool 的下一個 buffer 並 Reset。
	// - 遊戲邏輯應該把它當成「工作中 buffer」，不要長期持有；若要保留，請保存 YieldResult() 的回傳值。
	GameModeResult *buf.GameModeResult

	// modeId 是此 handler 對應的 mode 索引/識別，用於結果標記與除錯。
	modeId int

	// pool 是結果物件池：在一次 spin 內可能多次 Yield（例如 base + free + bonus），因此需要多個 result buffer。
	// 這些 *GameModeResult 會被重複使用（Reset 後再寫入）。
	pool []*buf.GameModeResult

	// pid 是 pool 指標，指向目前正在使用的 pool 索引。
	pid int
}

// newGameMode 建立一個 GameModeHandler 並完成初始化（generator / calculator / result pool）。
//
// 注意：不需要由遊戲邏輯實作者直接呼叫，因為上層 GameHandler 會負責建立並持有。
// 你會在遊戲邏輯裡拿到已經初始化完成的 *GameModeHandler 來使用。
func newGameMode(core *core.Core, gameModeSetting *spec.GameModeSetting, modeId int) *GameMode {
	gm := &GameMode{
		core:            core,
		GameModeSetting: gameModeSetting,
		modeId:          modeId,
	}
	gm.init()
	return gm
}

// YieldResult 提交（Yield）當前 GameModeResult，並切換到下一個可寫 buffer。
//
// ### 行為
// - 回傳值：本次 mode 的「最終結果指標」（由 pool 管理，caller 可安全持有並向上回傳）。
// - 內部：
//  1. 將當前 gm.GameModeResult 標記為 mode end（IsModeEnd = true）
//  2. pool 指標前進到下一格
//  3. 如 pool 用完則分批擴容（一次增加 poolSize）
//  4. gm.GameModeResult 切換到新 buffer，並 Reset，供下一次 mode 寫入
//
// ### 合約
// - Yield 之後：不要再透過 gm 去修改剛剛 Yield 出去的那個結果（應視為完成品）。
// - 需要保留結果：請保留 YieldResult() 的回傳值，而不是保留 gm.GameModeResult（因為它會被切換/Reset）。
// - 非並行安全：不可跨 goroutine 同時呼叫。
//
// ### 範例
//
//	res := gm.YieldResult()
//	// res 可向上回傳、轉 DTO、統計累加等
//	// gm.GameModeResult 已經切換到下一個乾淨 buffer，可繼續生成下一個 mode。
func (gm *GameMode) YieldResult() *buf.GameModeResult {
	// 1) 取出要提交的結果 並
	gmr := gm.GameModeResult
	gmr.IsModeEnd = true

	// 2) 推進池指標
	gm.pid++

	// 3) 池已用完 → 一次性長到 len+poolSize（只做一次拷貝）
	if gm.pid == len(gm.pool) {
		old := gm.pool
		// 新切片容量 = 舊長度 + poolSize（不做倍增）
		tmp := make([]*buf.GameModeResult, len(old), len(old)+poolSize)
		copy(tmp, old)
		gm.pool = tmp

		// 懶建立新 result 物件
		buffer, hitsInit := gm.getInitCap()
		for i := 0; i < poolSize; i++ {
			gm.pool = append(gm.pool, buf.NewGameModeResult(gm.modeId, gm.GameModeSetting, buffer, hitsInit))
		}
	}

	// 4) 切換當前 result 並 Reset 供下一個 mode 使用
	gm.GameModeResult = gm.pool[gm.pid]
	gm.GameModeResult.Reset()
	return gmr
}

// ResetGameModeResult 將 pool 指標重置到第 0 格，並把目前 buffer Reset 成乾淨狀態。
//
// 用途：
// - 通常在「一次新的 spin 開始」時呼叫，確保本次流程從 pool[0] 開始使用。
// - 不會重置 core；core 的 PRNG/共享狀態由更上層管理。
func (gm *GameMode) ResetGameModeResult() {
	// 不重置core
	gm.pid = 0
	gm.GameModeResult = gm.pool[0]
	gm.GameModeResult.Reset()
}

// ============================================================
// ** 以下內部方法 **
// ============================================================

// init 初始化 generator / calculator / pool。
// 僅由建構流程呼叫；不建議在遊戲邏輯中手動呼叫。
//
// 設計重點：
// - generator / calculator 以 mode setting 建立一次後重用
// - pool 預先配置 poolSize 個 *GameModeResult，並根據 BetType 估計初始容量以避免熱路徑擴容
func (gm *GameMode) init() {
	// 建立盤面生成器
	gm.ScreenGenerator = gen.NewScreenGenerator(gm.core, &gm.GameModeSetting.ScreenSetting, &gm.GameModeSetting.GenScreenSetting)
	// 建立盤面計算器
	gm.ScreenCalculator = calc.NewScreenCalculator(gm.GameModeSetting)

	// 建立結果池
	gm.pid = 0
	gm.pool = make([]*buf.GameModeResult, 0, poolSize)
	buffer, hitsInitSize := gm.getInitCap()
	for i := 0; i < poolSize; i++ {
		gm.pool = append(gm.pool, buf.NewGameModeResult(gm.modeId, gm.GameModeSetting, buffer, hitsInitSize))
	}
	gm.GameModeResult = gm.pool[gm.pid]
}

// getInitCap 回傳 (bufferCap, hitsFlatInitSize)，用於初始化 GameModeResult 的內部切片容量。
//
// 目標：
// - 讓大多數遊戲在熱路徑上不需要 slice grow（避免進入慢路徑與額外 allocations）
// - 這裡是「保守但不過分」的估計：寧可略大一點吃常態記憶體，也不要每局反覆擴容
//
// 注意：這個估計和 BetType 強相關（Line/Way/Cluster/Count 等）。
func (gm *GameMode) getInitCap() (int, int) {
	// 根據押注型態決定 : buffer & hitsFlat 大小 以及計算函數
	buffer := 0           // 宣告初始最大細項結果數量：例如每一條線、每個way 保守但不過分的估計 目的是不要觸發擴容 否則會進入慢路徑
	hitsFlatInitSize := 0 // 宣告初始最大中獎圖稀疏矩陣容量：每一條線idx總和、每個way總和

	gms := gm.GameModeSetting

	switch gms.HitSetting.BetType {
	case spec.BetTypeLineLTR:
		buffer = len(gms.HitSetting.LineTable) + 1            // 線獎數量直接走線表+1
		hitsFlatInitSize = gms.ScreenSetting.Columns * buffer // 走線表數 * 軸數 = 裝下所有走線表index 長度
	case spec.BetTypeLineRTL:
		buffer = len(gms.HitSetting.LineTable) + 1            // 線獎數量直接走線表+1
		hitsFlatInitSize = gms.ScreenSetting.Columns * buffer // 走線表數 * 軸數 = 裝下所有走線表index 長度
	case spec.BetTypeLineBoth:
		buffer = (len(gms.HitSetting.LineTable) + 1) * 2      // 左右走線數量直接 * 2
		hitsFlatInitSize = gms.ScreenSetting.Columns * buffer // 走線表數 * 軸數 = 裝下所有走線表index 長度
	case spec.BetTypeWayLTR:
		buffer = (gms.SymbolSetting.SymbolCount + gms.ScreenSetting.Rows) * 2     // 把所有圖標數當作可能得分計數 + 第一軸的格子數(可能的起始數)
		hitsFlatInitSize = gms.ScreenSetting.Columns * gms.ScreenSetting.Rows * 2 // 兩倍盤面大小
	case spec.BetTypeWayRTL:
		buffer = (gms.SymbolSetting.SymbolCount + gms.ScreenSetting.Rows) * 2     // 把所有圖標數當作可能得分計數 + 第一軸的格子數(可能的起始數)
		hitsFlatInitSize = gms.ScreenSetting.Columns * gms.ScreenSetting.Rows * 2 // 兩倍盤面大小
	case spec.BetTypeWayBoth:
		buffer = (gms.SymbolSetting.SymbolCount + gms.ScreenSetting.Rows) * 4     // 單向Way * 2
		hitsFlatInitSize = gms.ScreenSetting.Columns * gms.ScreenSetting.Rows * 4 // 單向Way * 2
	case spec.BetTypeCount:
		buffer = gms.SymbolSetting.SymbolCount + 1                                // 直接把所有圖標都當作得分計數 + 1
		hitsFlatInitSize = gms.ScreenSetting.Columns * gms.ScreenSetting.Rows * 2 // 兩倍盤面大小
	case spec.BetTypeCluster:
		buffer = (gms.ScreenSetting.Columns * gms.ScreenSetting.Rows) + gms.SymbolSetting.SymbolCount // 盤面大小(每個格子都算一次得分)+圖標數量
		hitsFlatInitSize = gms.ScreenSetting.Columns * gms.ScreenSetting.Rows * 2                     // 兩倍盤面大小
	default:
		buffer = ((gms.ScreenSetting.Columns * gms.ScreenSetting.Rows) + gms.SymbolSetting.SymbolCount) * 4
		hitsFlatInitSize = gms.ScreenSetting.Columns * gms.ScreenSetting.Rows * 2
	}

	return buffer, hitsFlatInitSize
}

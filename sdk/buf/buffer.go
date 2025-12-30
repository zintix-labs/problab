package buf

import (
	"github.com/zintix-labs/problab/spec"
)

const capSpinGrow int = 20

// SpinResult 保存一次完整 Spin 的結果。
type SpinResult struct {
	TotalWin      int               // 總贏分
	GameName      string            // 遊戲名稱
	GameID        spec.GID          // 遊戲Id
	Logic         spec.LogicKey     // 對應遊戲邏輯
	Bet           int               // 當次押注
	BetUnits      []int             // 押注單位
	BetMode       int               // 押注類型
	BetMult       int               // 押注倍數
	GameModeCount int               // 經過幾個GameMode
	GameModeList  []*GameModeResult // 每個遊戲模式的完整結構
	IsGameEnd     bool              // 遊戲結束旗標
}

// NewSpinResult 建立指定機台的 SpinResult 實體，並預先配置基本容量。
func NewSpinResult(gs *spec.GameSetting) *SpinResult {
	sr := &SpinResult{
		TotalWin:      0,
		GameName:      gs.GameName,
		GameID:        spec.GID(gs.GameID),
		Logic:         gs.LogicKey,
		Bet:           0,
		BetUnits:      gs.BetUnits,
		BetMode:       0,
		BetMult:       0,
		GameModeCount: 0,
		GameModeList:  make([]*GameModeResult, 0, capSpinGrow),
		IsGameEnd:     false,
	}
	return sr
}

// AppendModeResult 將單一 GameMode 的結果累積到 SpinResult。
func (s *SpinResult) AppendModeResult(gmr *GameModeResult) {
	if s.IsGameEnd {
		panic("spin request is already end, but still send new result")
	}
	s.TotalWin += gmr.TotalWin
	s.GameModeCount++
	s.GameModeList = append(s.GameModeList, gmr)
}

// End : 結束Spin
func (s *SpinResult) End() {
	s.IsGameEnd = true
}

// Reset 重置累積資料，保留已配置的內部切片容量。
func (s *SpinResult) Reset() {
	s.TotalWin = 0
	s.Bet = 0
	s.BetMult = 0
	s.BetMode = 0
	s.GameModeCount = 0
	s.GameModeList = s.GameModeList[:0]
	s.IsGameEnd = false
}

// Game Mode

const capModeGrow int = 1024 // 容量大小

type FinishType uint8

const (
	FinishAct FinishType = iota
	FinishStep
	FinishRound
)

// GameModeResult 保存單一 GameMode 的所有累積資訊（Round/Step/Act 索引、盤面快照、細項、觸發狀態等）。
type GameModeResult struct {
	TotalWin   int  // 整個GameMode贏分
	GameModeId int  // 設定檔中的第幾個Mode狀態
	IsModeEnd  bool // 模式是否完整結束
	Trigger    int  // 觸發遊戲

	ActResults []ActResult

	BetType    spec.BetType
	ScreenSize int                // 盤面大小
	Screens    []int16            // 盤面存儲
	Details    []CalcScreenDetail // 細項紀錄列表
	HitsFlat   []int16            // 中獎圖稀疏矩陣 (只有中獎的格子idx會紀錄)

	TmpAct *TmpAct // 狀態暫存點
}

type ActResult struct {
	ActType string // enum/int

	RoundId int
	StepId  int
	ActId   int

	IsRoundEnd bool
	IsStepEnd  bool

	NowTotalWin int
	RoundAccWin int
	StepAccWin  int
	ActWin      int

	DetailsStart int
	DetailsEnd   int
	ScreenStart  int // -1: 無；否則指到 ScreenBuf 起點

	ExtendResult any // cfg.Turbo模式下存struct指標 正式外發使用情況下轉 map[string]any(Json) 或 []byte
}

// CalcScreenDetail 盤面算分細項
type CalcScreenDetail struct {
	Win           int   // 本DetailCalcResult輸贏
	SymbolID      int16 // 圖標ID
	LineID        int   // Line專用 : 線表ID
	Count         int   // 計算數量 直接對應PayTable (Line: 連線長度, Cluster: 集群圖標數量, Collect: 收集數量)
	Combinations  int   // Way專用 : 組合數量
	Direction     uint8 // 方向，0: 左到右，1: 右到左 (Way Line用)
	HitsFlatStart int   // HitsFlat的起始位置
	HitsFlatLen   int   // HitsFlat的長度
}

// 狀態暫存點
type TmpAct struct {
	CurrRound   int // 當下局數
	CurrStep    int // 當下段數
	CurrAct     int // 當下Act
	ScreenStart int // 本次Screen起點
	DetailStart int // 這次記錄的Detail開始位置
	HitsStart   int // 這次紀錄的中獎圖
	CurrDetail  int // 細項數量 len(Details)
	Acctotalwin int // 當下累計總贏分
	RoundAccWin int // 當下Round累計贏分
	StepAccWin  int // 當下Step累計贏分
	Win         int // 當下贏分
}

// ============================================================
// ** 以下公開方法 **
// ============================================================

// NewGameModeResult 依照設定建立 GameModeResult，並預配快照與細項緩衝。
func NewGameModeResult(modeId int, ganeModeSetting *spec.GameModeSetting, buffer int, hitsInitSize int) *GameModeResult {
	gms := ganeModeSetting
	gmr := &GameModeResult{
		TotalWin:   0,
		GameModeId: modeId,
		IsModeEnd:  false,
		Trigger:    0,

		ActResults: make([]ActResult, 0, capModeGrow),

		BetType:    gms.HitSetting.BetType,
		ScreenSize: gms.ScreenSetting.ScreenSize,
		Screens:    make([]int16, 0, gms.ScreenSetting.ScreenSize*capModeGrow),
		Details:    make([]CalcScreenDetail, buffer+capModeGrow),
		HitsFlat:   make([]int16, 0, hitsInitSize+capModeGrow),

		TmpAct: &TmpAct{},
	}
	return gmr
}

// Reset 重置狀態，清空累積結果但保留已配置容量。
func (gmr *GameModeResult) Reset() {
	gmr.TotalWin = 0
	gmr.IsModeEnd = false
	gmr.Trigger = 0
	// GameModeId 不變

	gmr.ActResults = gmr.ActResults[:0]

	// BetType 不變
	// ScreenSize 不變
	gmr.Screens = gmr.Screens[:0]
	// Details 內容不清空，RecordDetail 時會自動清空並覆蓋
	gmr.HitsFlat = gmr.HitsFlat[:0]

	gmr.TmpAct.reset()
}

// RecordDetail 紀錄一筆算分細項，並同步追加中獎格資訊與暫存贏分。
func (gmr *GameModeResult) RecordDetail(win int, symbolID int16, lineID int, count int, combine int, direction uint8, hitsFlat []int16) {
	// 紀錄細部資料
	d := gmr.nextDetail()
	d.SymbolID = symbolID
	d.LineID = lineID
	d.Count = count
	d.Combinations = combine
	d.Win = win
	d.Direction = direction
	d.HitsFlatLen = len(hitsFlat)

	// 紀錄頂部資料 (還沒紀錄Act)
	gmr.HitsFlat = append(gmr.HitsFlat, hitsFlat...)

	gmr.TmpAct.addwin(win)
}

// RecordDetailSegments 紀錄一筆以兩段命中資料構成的細項（例如雙段命中線），並累積暫存贏分。
func (gmr *GameModeResult) RecordDetailSegments(win int, symbolID int16, lineID int, count int, combine int, direction uint8, seg1 []int16, seg2 []int16) {
	// 紀錄細部資料
	d := gmr.nextDetail()
	d.SymbolID = symbolID
	d.LineID = lineID
	d.Count = count
	d.Combinations = combine
	d.Win = win
	d.Direction = direction
	d.HitsFlatLen = len(seg1) + len(seg2)

	// 紀錄頂部資料
	gmr.HitsFlat = append(gmr.HitsFlat, seg1...)
	gmr.HitsFlat = append(gmr.HitsFlat, seg2...)

	gmr.TmpAct.addwin(win)
}

// GetDetails 取得目前累計的盤面細項紀錄。
func (gmr *GameModeResult) GetDetails() []CalcScreenDetail {
	return gmr.Details[:gmr.TmpAct.CurrDetail]
}

// View 回傳當前盤面快照，若不存在則回傳 nil。
func (gmr *GameModeResult) View() []int16 {
	sz := gmr.ScreenSize
	end := gmr.TmpAct.ScreenStart // 永遠指向 Screens 的最新尾端
	start := end - sz
	// 邊界保護（uint 折疊負數，避免多餘分支成本）
	if uint(end) > uint(len(gmr.Screens)) || start < 0 {
		return nil
	}
	return gmr.Screens[start:end]
}

// HitMapLastAct 取得最近一次落地 Act 的中獎位置切片（避免複製，請勿修改返回值）。
// 性能考量取到切片，請勿改動
func (gmr *GameModeResult) HitMapLastAct() []int16 {
	if len(gmr.ActResults) == 0 {
		return nil
	}
	a := gmr.ActResults[len(gmr.ActResults)-1]

	// 必須檢查是否有 Detail，否則下面取陣列會爆
	if a.DetailsEnd <= a.DetailsStart {
		return nil
	}

	// 注意：這裡假設 Details 的順序是連續的，且 HitsFlat 也是連續寫入的
	// 取出第一筆 Detail 的 Start
	start := gmr.Details[a.DetailsStart].HitsFlatStart
	// 取出最後一筆 Detail 的 End
	lastDetail := gmr.Details[a.DetailsEnd-1]
	end := lastDetail.HitsFlatStart + lastDetail.HitsFlatLen

	// 邊界防禦 (Optional but safe)
	if end > len(gmr.HitsFlat) {
		return nil
	}

	return gmr.HitsFlat[start:end]
}

// HitMapTmp 取得當前暫存（未落地）的中獎位置切片；Discard 後內容會被回收。
// 性能考量取到切片，請勿改動(一旦Discard會消失)
func (gmr *GameModeResult) HitMapTmp() []int16 {
	if len(gmr.HitsFlat) <= gmr.TmpAct.HitsStart {
		return nil
	}
	return gmr.HitsFlat[gmr.TmpAct.HitsStart:]
}

// GetTmpWin 取得當下暫存贏分。
func (gmr *GameModeResult) GetTmpWin() int {
	return gmr.TmpAct.Win
}

// UpdateTmpWin 直接覆寫暫存贏分，並同步調整累積軌跡。
func (gmr *GameModeResult) UpdateTmpWin(win int) {
	t := gmr.TmpAct
	diff := win - t.Win
	t.Win = win
	t.Acctotalwin += diff
	t.RoundAccWin += diff
	t.StepAccWin += diff
}

// Discard 拋棄本次動作得分 資料不落地 環境更美麗
func (gmr *GameModeResult) Discard() {
	t := gmr.TmpAct

	gmr.Screens = gmr.Screens[:t.ScreenStart]
	t.CurrDetail = t.DetailStart // 邏輯長度回卷
	gmr.HitsFlat = gmr.HitsFlat[:t.HitsStart]
	t.addwin(-t.Win) // 分數回退
}

// AddAct 是「線性遊戲流程」的核心紀錄入口：
//   - 一次只接受一個行為（Act），依序落地到 ActResults，維持 Round/Step/Act 三層索引。
//   - 呼叫者先累積暫存分數/細項/盤面，最後用 AddAct 落地；之後才會呼叫 FinishStep / FinishRound 標記邊界。
//   - screen 參數：可選的盤面快照，長度必須符合 ScreenSize；若不需要快照可傳空 slice。
//   - ext 參數：遊戲的 ExtendResult 介面。AddAct 內部只負責呼叫 Snapshot()，不涉入 isSim 判斷；
//     是否回傳 nil (Sim 模式) 或深拷貝副本 (Server 模式) 完全由遊戲實作端決定。
//   - at 參數：指定本次行為是否同時結束 Step / Round（FinishAct 代表僅結束 Act）。
//
// 整體流程：
//  1. 驗證並 snapshot 盤面（若提供）。
//  2. 將暫存的分數/細項範圍/盤面位置封裝為 ActResult，並 append 至 ActResults。
//  3. TotalWin 累加暫存分數，TmpAct 的 Act 索引 +1，並重置暫存邊界(nextAct)。
//  4. 若 at==FinishStep / FinishRound，立即推進對應層級的索引，方便下一次寫入。
func (gmr *GameModeResult) AddAct(at FinishType, actType string, screen []int16, ext ExtendResult) {
	if len(screen) != 0 && len(screen) != gmr.ScreenSize { // 能否直接限制screen [ScreenSize]int16 避免檢查
		panic("screen size not match")
	}
	t := gmr.TmpAct
	screenStart := -1
	if len(screen) > 0 {
		screenStart = t.ScreenStart
		gmr.snapshot(screen)
	}
	// 1. 落地Act
	a := ActResult{
		ActType:      actType,
		RoundId:      t.CurrRound,
		StepId:       t.CurrStep,
		ActId:        t.CurrAct,
		IsRoundEnd:   (at == FinishRound),
		IsStepEnd:    (at != FinishAct),
		ActWin:       t.Win,
		StepAccWin:   t.StepAccWin,
		RoundAccWin:  t.RoundAccWin,
		NowTotalWin:  t.Acctotalwin,
		DetailsStart: t.DetailStart,
		DetailsEnd:   t.CurrDetail,
		ScreenStart:  screenStart,
	}
	if ext != nil {
		a.ExtendResult = ext.Snapshot()
	}
	gmr.ActResults = append(gmr.ActResults, a)

	// 2. 更新總分 步進act
	gmr.TotalWin += t.Win
	t.CurrAct++

	// 這裡會把 detailStart=currDetail、hitsStart/ screenStart 移到最新邊界，並把 t.win 清 0
	gmr.nextAct()

	if at == FinishRound {
		t.nextRound()
		return
	}
	if at == FinishStep {
		t.nextStep()
		return
	}
}

// FinishStep 手動結束當前 Step (段落)：
//   - 通常用於具有分支的邏輯斷點:先提交Act行為，接著判斷cond1 FinishStep結束本次 或者繼續進行...
//   - 若 AddAct 已經傳入 FinishStep，則無需再次呼叫此方法。
//   - 此方法會推進 Step 索引 (CurrStep++) 並重置 Act 索引 (CurrAct=0)。
//   - 推進FinishRound的時候也會重置Step
//   - 防呆：若最後一個 Act 已經是 StepEnd 或 RoundEnd，此呼叫會被忽略。
func (gmr *GameModeResult) FinishStep() {
	count := len(gmr.ActResults)
	if count == 0 {
		panic("no actions can finish the step")
	}
	act := &gmr.ActResults[count-1]
	if act.IsStepEnd || act.IsRoundEnd {
		return
	}
	act.IsStepEnd = true
	gmr.TmpAct.nextStep()
}

// FinishRound 手動結束當前 Round (局)：
// - 通常用於「BaseGame 結束」或「FreeSpin 的完整一局結束」。
// - 會同時標記 StepEnd 與 RoundEnd (因為 Round 結束隱含 Step 結束)。
// - 此方法會推進 Round 索引 (CurrRound++) 並重置所有內層索引。
func (gmr *GameModeResult) FinishRound() {
	count := len(gmr.ActResults)
	if count == 0 {
		panic("no actions can finish the round")
	}
	act := &gmr.ActResults[count-1]
	if act.IsRoundEnd {
		return
	}
	act.IsStepEnd = true
	act.IsRoundEnd = true

	gmr.TmpAct.nextRound()
}

// ============================================================
// ** 以下內部方法 **
// ============================================================

// nextDetail 取得下一個Detail準備寫入(只在確定要紀錄時呼叫)
// NOTE: nextDetail 只應由 RecordDetail 呼叫，以保證完整寫入流程不被中斷。
func (gmr *GameModeResult) nextDetail() *CalcScreenDetail {
	if gmr.TmpAct.CurrDetail < len(gmr.Details) {
		d := &gmr.Details[gmr.TmpAct.CurrDetail]
		*d = CalcScreenDetail{}             // 清空內容 : 通用手法，如果BetType中只會用到少數值 可以考慮特化處理
		d.HitsFlatStart = len(gmr.HitsFlat) // 紀錄HitsFlat起始位置
		gmr.TmpAct.CurrDetail++
		return d
	}
	// 觸發擴容 比較慢 如果觸發 建議後續調整更大的buffer 避免進入
	gmr.Details = append(gmr.Details, make([]CalcScreenDetail, capModeGrow)...)
	return gmr.nextDetail()
}

func (gmr *GameModeResult) nextAct() {
	t := gmr.TmpAct

	t.ScreenStart = len(gmr.Screens)
	t.DetailStart = t.CurrDetail // 基準線使用邏輯長度 而非總長
	t.HitsStart = len(gmr.HitsFlat)

	t.Win = 0 // 重新累計
}

func (t *TmpAct) nextStep() {
	t.CurrStep++
	t.CurrAct = 0
	t.StepAccWin = 0
	t.Win = 0
}

func (t *TmpAct) nextRound() {
	// 內部狀態更新
	t.CurrRound++
	t.CurrStep = 0
	t.CurrAct = 0
	t.RoundAccWin = 0
	t.StepAccWin = 0
	t.Win = 0
}

// 生成 screen 快照
func (gmr *GameModeResult) snapshot(screen []int16) {
	if len(screen) == 0 {
		return
	}
	sz := gmr.ScreenSize
	if len(screen) != gmr.ScreenSize {
		panic("screen size not match")
	}
	t := gmr.TmpAct.ScreenStart
	s := gmr.Screens
	need := t + sz
	if cap(s) < need {
		ns := make([]int16, t, 2*(cap(s)+sz))
		copy(ns, s[:t])
		gmr.Screens = ns
	}
	gmr.Screens = append(gmr.Screens, screen...)
}

func (ta *TmpAct) reset() {
	ta.CurrRound = 0
	ta.CurrStep = 0
	ta.CurrAct = 0
	ta.ScreenStart = 0
	ta.DetailStart = 0
	ta.HitsStart = 0
	ta.CurrDetail = 0
	ta.Acctotalwin = 0
	ta.RoundAccWin = 0
	ta.StepAccWin = 0
	ta.Win = 0
}

func (ta *TmpAct) addwin(win int) {
	ta.Win += win
	ta.Acctotalwin += win
	ta.RoundAccWin += win
	ta.StepAccWin += win
}

// ExtendResult 定義了所有遊戲擴充資訊必須具備的行為
//
// 這強制規範開發者實作 Reset 和 Snapshot 機制，確保 Sim/Server 模式正確運作。
type ExtendResult interface {
	// Reset 需要做到「完全清空到初始狀態」：
	//	- 由遊戲自行決定要不要重用記憶體，以避免 GC 負擔。
	//	- 只在遊戲內被呼叫，不經過 GameModeResult，避免污染共用流程。
	//	- 保證下一次 Snapshot 不會帶著上一局遺留狀態。
	//	- 是否依據 isSim 做額外優化（例如跳過清空內容），完全由遊戲實作者自行決定。
	//	- 一般而言Spin(BaseGame) 一開始會先Reset一次，進入FG也會先Reset一次
	Reset()
	// Snapshot 建立快照
	//  - 呼叫端（GameModeResult/Act/DTO）一律只呼叫 Snapshot，不需要知道 isSim 的存在。
	//  - 遊戲實作者可以在內部判斷 isSim 以回傳 nil (觸發 JSON omitempty)，省去深拷貝 CPU 成本與流量。
	//  - 回傳型別使用 any 是為了相容 JSON 序列化，避免強轉型。
	//  - 建議：總是先考量並實作深拷貝方式以確保併發安全；Sim 模式可依需求回傳 nil 或輕量資料。
	Snapshot() any
}

// NoExtend 是「無附加資料」的佔位型別：
// - 允許遊戲以最小成本完成 ExtendResult 註冊，避免到處 nil 判斷。
// - Reset/Snapshot 皆為空操作；行為可預期且 thread-safe。
// - 透過指標型別滿足泛型註冊約束，與有實際資料的 extend 接口一致。
// - 保持結構簡單，讓只有盤面與分數的遊戲不用額外負擔。
type NoExtend struct{}

// Reset 是 NoExtend 的空實作：
//   - 不需要任何狀態回收，因為 NoExtend 不持有資料。
//   - 保留方法是為了滿足介面契約，讓呼叫端不用分支處理。
//   - 提醒實作者：若將來加入欄位，這裡需要對應清空。
//   - 儘管是空函數，也保持存在以方便閱讀與一致性。
func (e *NoExtend) Reset() {}

// Snapshot 是 NoExtend 的空實作：
//   - 永遠回傳 nil，這樣 JSON 輸出時該欄位會被完全省略 (omitempty)。
//   - 呼叫端永遠只呼叫 Snapshot；是否有 isSim 優化由具體遊戲 extend 決定，這裡保持單純。
//   - GameModeResult.AddAct 內會直接呼叫此方法，減少分支與 nil 判斷。
//   - 若未來需要占位資料或想顯式傳遞空物件，可在此改為回傳 new(NoExtend)。
func (e *NoExtend) Snapshot() any {
	// if e.isSim {
	// 	return nil
	// }
	// return new(NoExtend)
	return nil
}

package demo_logic

import (
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/ops"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/spec"
)

// ============================================================
// ** 註冊 **
// ============================================================

func init() {
	slot.GameRegister[*buf.NoExtend](
		"demo_cascade",
		buildGame0001,
		Reg,
	)
}

// ============================================================
// ** 遊戲介面 **
// ============================================================

type game0001 struct {
	fixed *fixed0001
}

func buildGame0001(g *slot.Game) (slot.GameLogic, error) {
	g1 := &game0001{
		fixed: &fixed0001{
			baseMaxStep:       1000,
			freeMaxStep:       1000,
			fillScreenIdx:     []int{0, 0, 0, 0, 0},
			nowfillReelSetIdx: []int{0, 0, 0, 0, 0},
			symbolTypes:       g.GameSetting.GameModeSettings[0].SymbolSetting.SymbolTypes,
		},
	}
	return g1, nil
}

// ============================================================
// ** 此遊戲需要的額外結構宣告: Fixed設定宣告 **
// ============================================================

type fixed0001 struct {
	baseMaxStep       int
	freeMaxStep       int
	fillScreenIdx     []int
	nowfillReelSetIdx []int
	symbolTypes       []spec.SymbolType
}

// ============================================================
// ** 遊戲需要的額外結構宣告: 需要實作Reset以及SnapShot **
// ============================================================

// ============================================================
// ** 遊戲主邏輯入口 **
// ============================================================

// getResult 主要介面函數 回傳遊戲結果 *res.SpinResult
func (g *game0001) GetResult(r *buf.SpinRequest, gh *slot.Game) *buf.SpinResult {
	sr := gh.StartNewSpin(r)
	base := g.getBaseResult(r, gh)
	sr.AppendModeResult(base)

	if base.Trigger != 0 {
		free := g.getFreeResult(r, gh)
		sr.AppendModeResult(free)
	}
	sr.End()
	return sr
}

// ============================================================
// ** 遊戲中各模式內部邏輯實作 **
// ============================================================

func (g *game0001) getBaseResult(r *buf.SpinRequest, gh *slot.Game) *buf.GameModeResult {
	mode := gh.GameModeHandlerList[0]
	sg := mode.ScreenGenerator
	sc := mode.ScreenCalculator
	gmr := mode.GameModeResult
	maxStep := g.fixed.baseMaxStep
	fillReelSet := &mode.GameModeSetting.GenScreenSetting.ReelSetGroup[1]

	betMult := r.BetMult

	for i := 0; i < 1; i++ {
		// 1. 生成該round開局盤面
		screen := sg.GenScreen()
		g.resetIdx()
		for range maxStep {
			// 2. 算分
			sc.CalcScreen(betMult, screen, gmr)

			// 3. Act完成
			win := gmr.GetTmpWin() // 先記當下贏分避免提交後要重找
			gmr.AddAct(buf.FinishAct, "GenAndCalcScreen", screen, nil)

			// 4. 如果沒贏分退出
			if win == 0 {
				gmr.FinishStep()
				break
			}

			// 5. 消除掉落
			// 5.1 消除
			ops.Clear(screen, gmr.HitMapLastAct())
			// 5.2 掉落
			ops.Gravity(screen, sg.Cols, sg.Rows, g.fixed.fillScreenIdx)

			// 6. 提交 Step結果
			gmr.AddAct(buf.FinishStep, "Gravity", screen, nil) // 消除掉落盤面

			// 7. 補滿盤面
			ops.FillScreen(screen, fillReelSet, g.fixed.fillScreenIdx, g.fixed.nowfillReelSetIdx, sg.Cols)
		}

		gmr.Trigger = g.trigger(screen)
		gmr.FinishRound()
	}
	return mode.YieldResult()
}

func (g *game0001) getFreeResult(r *buf.SpinRequest, gh *slot.Game) *buf.GameModeResult {
	mode := gh.GameModeHandlerList[1]
	sg := mode.ScreenGenerator
	sc := mode.ScreenCalculator
	gmr := mode.GameModeResult
	maxStep := g.fixed.freeMaxStep
	fillReelSet := &mode.GameModeSetting.GenScreenSetting.ReelSetGroup[1]

	betMult := r.BetMult

	for i := 0; i < 10; i++ {
		// 1. 生成該round開局盤面
		screen := sg.GenScreen()
		g.resetIdx()
		for range maxStep {
			// 2. 算分
			sc.CalcScreen(betMult, screen, gmr)

			// 3. Act完成
			win := gmr.GetTmpWin() // 先記當下贏分避免提交後要重找
			gmr.AddAct(buf.FinishAct, "GenAndCalcScreen", screen, nil)

			// 4. 如果沒贏分退出
			if win == 0 {
				gmr.FinishStep()
				break
			}

			// 5. 消除掉落
			// 5.1 消除
			ops.Clear(screen, gmr.HitMapLastAct())
			// 5.2 掉落
			ops.Gravity(screen, sg.Cols, sg.Rows, g.fixed.fillScreenIdx)

			// 6. 提交step結果
			gmr.AddAct(buf.FinishAct, "Gravity", screen, nil) // 消除掉落盤面

			// 7. 補滿盤面
			ops.FillScreen(screen, fillReelSet, g.fixed.fillScreenIdx, g.fixed.nowfillReelSetIdx, sg.Cols)
		}

		gmr.FinishRound()
	}

	return mode.YieldResult()
}

// ============================================================
// ** 遊戲內部輔助函數實作 **
// ============================================================

// 0 代表不觸發 > 0 各自觸發
func (g *game0001) trigger(screen []int16) int {
	symTypes := g.fixed.symbolTypes
	count := 0
	for _, v := range screen {
		if symTypes[v] == spec.SymbolTypeScatter {
			count++
		}
	}
	if count > 2 {
		return 1
	}
	return 0
}

func (g *game0001) resetIdx() {
	for i := 0; i < len(g.fixed.fillScreenIdx); i++ {
		g.fixed.fillScreenIdx[i] = 0
		g.fixed.nowfillReelSetIdx[i] = 0
	}
}

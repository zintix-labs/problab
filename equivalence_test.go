package problab

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/spec"
)

// 本機與正式路徑的等價：同一組 choice 分別送進 BetRunner（checkpoint 傳指標）與
// Spin(dto)（checkpoint 經 JSON 往返），逐段比對遊戲結果、段快照與呼叫後的機台位置，
// 最後比對完整局。BetRunner 最後一次回傳完整局，所以本機端也以探針取得最後一段。

// lastResultLogic 包住遊戲邏輯，保留遊戲最後回傳的段結果，讓測試取得 Spin 實際產生的 buf 結果。
type lastResultLogic struct {
	inner slot.GameLogic
	last  *buf.SpinResult
}

func (l *lastResultLogic) GetResult(r *buf.SpinRequest, g *slot.Game) *buf.SpinResult {
	l.last = l.inner.GetResult(r, g)
	return l.last
}

// wrapLastResult 以 lastResultLogic 包住 m 目前的遊戲邏輯（從 reg 依 logic key 重建）。
func wrapLastResult(t testing.TB, m *Machine, reg *slot.LogicRegistry) *lastResultLogic {
	t.Helper()
	key := m.gh.GameSetting.LogicKey
	logic := &lastResultLogic{}
	wrapped := slot.NewLogicRegistry()
	if err := wrapped.Register(key, func(g *slot.Game) (slot.GameLogic, error) {
		inner, err := reg.Build(key, g)
		logic.inner = inner
		return logic, err
	}); err != nil {
		t.Fatal(err)
	}
	gh, err := slot.NewGame(m.gh.GameSetting, wrapped, m.core, m.gh.OmitExtend())
	if err != nil {
		t.Fatal(err)
	}
	m.gh = gh
	return logic
}

// gameJSON 是比對用的遊戲結果：DTO 化後清除只由 Spin 入口回填的 bet_id 與 choice。
func gameJSON(t testing.TB, d dto.SpinResult) string {
	t.Helper()
	d.BetID, d.Choice = "", nil
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func segmentJSON(t testing.TB, sr *buf.SpinResult) string {
	t.Helper()
	d, err := dto.NewSpinResultDTO(sr)
	if err != nil {
		t.Fatal(err)
	}
	return gameJSON(t, d)
}

// compareLocalAndDTO 以兩台相同 seed 的機台各執行 bets 局，回傳所有差異。choose 依局號與
// 段號決定 choice。
func compareLocalAndDTO(t testing.TB, local, remote *Machine, reg *slot.LogicRegistry, bets int, choose func(bet, cycle int) int) []string {
	t.Helper()
	var diffs []string
	diff := func(format string, args ...any) { diffs = append(diffs, fmt.Sprintf(format, args...)) }
	runner, err := NewBetRunner(local, WithMaxSegments(8))
	if err != nil {
		t.Fatal(err)
	}
	localProbe := wrapLastResult(t, local, reg)
	probe := wrapLastResult(t, remote, reg)
	asm := buf.NewSpinAssembler()
	triggered := 0
	for bet := range bets {
		out, err := runner.Bet(SpinInput{BetMode: 0, BetMult: 1, CaptureSnapshots: true}, ChoosePlayer)
		if err != nil {
			t.Fatal(err)
		}
		req := &dto.SpinRequest{BetID: fmt.Sprint("b", bet), GameName: remote.gameName, GameId: remote.gameId,
			Bet: remote.BetUnits[0], BetMode: 0, BetMult: 1}
		asm.Reset()
		for cycle := 0; ; cycle++ {
			res, err := remote.Spin(req)
			if err != nil {
				t.Fatal(err)
			}
			if res.BetID != req.BetID || (req.Choice == nil) != (res.Choice == nil) || (req.Choice != nil && *req.Choice != *res.Choice) {
				diff("bet %d segment %d: entry fields bet_id=%q choice=%v", bet, cycle, res.BetID, res.Choice)
			}
			// 本局未結束時回傳的是本段；結束時是完整局，本段改由探針取得。
			seg := out
			if out.IsGameEnd {
				seg = localProbe.last
			}
			if got, want := segmentJSON(t, seg), gameJSON(t, res); got != want {
				diff("bet %d segment %d: results differ\n local=%s\nremote=%s", bet, cycle, got, want)
			}
			if string(coreState(t, local)) != string(coreState(t, remote)) {
				diff("bet %d segment %d: machine positions differ", bet, cycle)
			}
			full, err := asm.Add(probe.last)
			if err != nil {
				t.Fatal(err)
			}
			if full != nil {
				if !out.IsGameEnd {
					diff("bet %d: local game not finished", bet)
				} else if segmentJSON(t, out) != segmentJSON(t, full) {
					diff("bet %d: whole games differ", bet)
				}
				break
			}
			if cycle == 0 {
				triggered++
			}
			choice := choose(bet, cycle)
			if out, err = runner.Next(choice); err != nil {
				diff("bet %d segment %d: local choose: %v", bet, cycle+1, err)
				return diffs
			}
			req.Cycle, req.Choice, req.ChooseNext = cycle+1, intp(choice), res.ChooseNext
			req.StartState = &dto.StartState{Checkpoint: res.State.Checkpoint}
		}
	}
	if triggered == 0 {
		t.Fatal("no bet reached a choice point")
	}
	return diffs
}

func TestLocalAndDTOPathsAreEquivalent(t *testing.T) {
	lab := demoChoiceLab(t)
	local, remote := demoChoiceMachine(t, lab, 31), demoChoiceMachine(t, lab, 31)
	diffs := compareLocalAndDTO(t, local, remote, demo_logic.Logics, 3000, func(bet, _ int) int { return bet % 2 })
	for _, d := range diffs {
		t.Error(d)
	}
}

// hiddenCP 的未匯出欄位無法經 JSON 往返：本機路徑傳指標時保留，正式路徑解碼後為 0。
type hiddenCP struct {
	Visible int `json:"visible"`
	hidden  int
}

// hiddenLogic 包住 demo_normal：第 0 段停在選擇點並輸出 hiddenCP；第 1 段的贏分加上 hidden。
type hiddenLogic struct{ inner slot.GameLogic }

func (l *hiddenLogic) GetResult(r *buf.SpinRequest, g *slot.Game) *buf.SpinResult {
	if r.Cycle == 0 {
		sr := l.inner.GetResult(r, g)
		sr.IsGameEnd = false
		sr.ChooseNext = append(sr.ChooseNext[:0], 1)
		sr.State.Checkpoint = &hiddenCP{Visible: 1, hidden: 2}
		return sr
	}
	cp, ok := r.Checkpoint.(*hiddenCP)
	if !ok {
		return nil
	}
	sr := l.inner.GetResult(r, g)
	sr.TotalWin += cp.hidden
	return sr
}

// 等價檢查必須能偵測無法往返的 checkpoint：這個測試斷言比較函式回報差異。
func TestLocalAndDTOComparisonDetectsLossyCheckpoint(t *testing.T) {
	const key spec.LogicKey = "equivalence_hidden_checkpoint"
	reg := slot.NewLogicRegistry()
	if err := reg.Register(key, func(g *slot.Game) (slot.GameLogic, error) {
		inner, err := demo_logic.Logics.Build("demo_normal", g)
		return &hiddenLogic{inner: inner}, err
	}); err != nil {
		t.Fatal(err)
	}
	if err := dto.RegisterCheckpoint[hiddenCP](key); err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(demo_configs.FS, "game_0_demonormal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "logic_key: demo_normal", "logic_key: "+string(key), 1))
	lab, err := NewAuto(core.Default(), Configs(fstest.MapFS{"game.yaml": &fstest.MapFile{Data: raw}}), Logics(reg))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	newMachine := func() *Machine {
		m, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(31)})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	diffs := compareLocalAndDTO(t, newMachine(), newMachine(), reg, 50, func(int, int) int { return 0 })
	if len(diffs) == 0 || !strings.Contains(diffs[0], "segment 1: results differ") {
		t.Fatalf("the comparison did not detect a checkpoint that cannot round-trip: %v", diffs)
	}
}

package problab

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/zintix-labs/problab/corefmt"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/spec"
)

// demo_choice（game 2）：BG 觸發後二選一的分段遊戲，設定在 StagedFS。

const demoChoiceID spec.GID = 2

func demoChoiceLab(t testing.TB) *Problab {
	t.Helper()
	lab, err := NewAuto(core.Default(), Configs(demo_configs.FS, demo_configs.StagedFS), Logics(demo_logic.Logics))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	return lab
}

func demoChoiceMachine(t testing.TB, lab *Problab, seed int64) *Machine {
	t.Helper()
	m, err := lab.NewMachine(demoChoiceID, MachineConfig{Seed: SeedInt64(seed)})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// 預設的 demo 設定不包含 game 2；明確載入 StagedFS 時才能建立並執行。
func TestDemoChoiceIsStaged(t *testing.T) {
	lab, err := NewAuto(core.Default(), Configs(demo_configs.FS), Logics(demo_logic.Logics))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	if slices.Contains(lab.IDs(), demoChoiceID) {
		t.Fatal("the default demo configs include the staged choice game")
	}
	staged := demoChoiceLab(t)
	if !slices.Contains(staged.IDs(), demoChoiceID) {
		t.Fatal("StagedFS does not provide the choice game")
	}
	if demoChoiceMachine(t, staged, 1).SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1}) == nil {
		t.Fatal("the staged game did not run")
	}
}

// startTriggered 以 BetRunner（player）反覆開局，直到第 0 段停在選擇點，回傳該段與觸發前的機台位置。
func startTriggered(t *testing.T, r *BetRunner) (*buf.SpinResult, []byte) {
	t.Helper()
	for range 5000 {
		before := coreState(t, r.m)
		sr, err := r.Bet(SpinInput{BetMode: 0, BetMult: 1, CaptureSnapshots: true}, ChoosePlayer)
		if err != nil {
			t.Fatal(err)
		}
		if !sr.IsGameEnd {
			return sr, before
		}
	}
	t.Fatal("the choice game never triggered")
	return nil, nil
}

func screenActs(gmr *buf.GameModeResult) int {
	n := 0
	for _, a := range gmr.ActResults {
		if a.ActType == "screen" {
			n++
		}
	}
	return n
}

// 兩個入口：FG-A 轉數為 8、FG-B 為 4，Scatter 每多一個多一轉；完整局的贏分等於兩段相加。
func TestDemoChoiceEntries(t *testing.T) {
	lab := demoChoiceLab(t)
	for choice, base := range []int{8, 4} {
		m := demoChoiceMachine(t, lab, 11)
		r, err := NewBetRunner(m, WithMaxSegments(2))
		if err != nil {
			t.Fatal(err)
		}
		seg0, _ := startTriggered(t, r)
		if !reflect.DeepEqual(seg0.ChooseNext, []int{demo_logic.EntryFGA, demo_logic.EntryFGB}) {
			t.Fatalf("choose next %v", seg0.ChooseNext)
		}
		raw, err := dto.EncodeCheckpoint(seg0.Logic, seg0.State.Checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		var cp struct{ Scatters int }
		if err := json.Unmarshal(raw, &cp); err != nil || cp.Scatters < 3 {
			t.Fatalf("checkpoint %s: %v", raw, err)
		}
		win0, modes0 := seg0.TotalWin, seg0.GameModeCount
		// 最後一段結束本局，回傳的是完整局：第 1 段的 GMR 接在第 0 段之後。
		res, err := r.Next(choice)
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsGameEnd || res.Cycle != 1 || len(res.ChooseNext) != 0 || res.GameModeCount != modes0+1 {
			t.Fatalf("game: end %v cycle %d next %v modes %d", res.IsGameEnd, res.Cycle, res.ChooseNext, res.GameModeCount)
		}
		fg := res.GameModeList[modes0]
		if got, want := screenActs(fg), base+cp.Scatters-3; got != want {
			t.Fatalf("choice %d: %d free rounds, want %d", choice, got, want)
		}
		if fg.GameModeId != choice+1 {
			t.Fatalf("choice %d entered mode %d", choice, fg.GameModeId)
		}
		if res.TotalWin != win0+fg.TotalWin {
			t.Fatalf("game win %d, want %d + %d", res.TotalWin, win0, fg.TotalWin)
		}
	}
}

// 遊戲以 nil 拒絕不接受的入口與 checkpoint。
func TestDemoChoiceRejects(t *testing.T) {
	lab := demoChoiceLab(t)
	m := demoChoiceMachine(t, lab, 11)
	r, err := NewBetRunner(m, WithMaxSegments(2))
	if err != nil {
		t.Fatal(err)
	}
	seg0, _ := startTriggered(t, r)
	cp := seg0.State.Checkpoint
	for name, in := range map[string]SpinInput{
		"unknown entry":    {Cycle: 1, ChooseNext: []int{9}, Checkpoint: cp},
		"wrong checkpoint": {Cycle: 1, ChooseNext: []int{demo_logic.EntryFGA}, Checkpoint: "x"},
		"no checkpoint":    {Cycle: 1, ChooseNext: []int{demo_logic.EntryFGA}},
	} {
		in.BetMode, in.BetMult = 0, 1
		if m.SpinUnsafe(in) != nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func choiceRequest(m *Machine, cycle int, choice *int, next []int, cp json.RawMessage, start string) *dto.SpinRequest {
	req := &dto.SpinRequest{BetID: "bet-1", GameName: m.gameName, GameId: m.gameId, Bet: m.BetUnits[0], BetMode: 0, BetMult: 1,
		Cycle: cycle, Choice: choice, ChooseNext: next}
	if len(cp) != 0 || start != "" {
		req.StartState = &dto.StartState{StartCoreSnapB64U: start, Checkpoint: cp}
	}
	return req
}

// spinTriggered 以 Spin 反覆開局，直到第 0 段停在選擇點。
func spinTriggered(t *testing.T, m *Machine) dto.SpinResult {
	t.Helper()
	for range 5000 {
		res, err := m.Spin(choiceRequest(m, 0, nil, nil, nil, ""))
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsGameEnd {
			return res
		}
	}
	t.Fatal("the choice game never triggered")
	return dto.SpinResult{}
}

func mustJSONString(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// 跨 Machine 接手：以第 0 段結果的 choose_next 與 cp 組出續段請求，送到另一台位置相同的
// Machine，結果與原機台繼續執行相同；結果回帶 bet_id、cycle 與 choice。
func TestDemoChoiceTakeoverAcrossMachines(t *testing.T) {
	lab := demoChoiceLab(t)
	a, b := demoChoiceMachine(t, lab, 21), demoChoiceMachine(t, lab, 99)
	seg0 := spinTriggered(t, a)
	if seg0.BetID != "bet-1" || seg0.Choice != nil || len(seg0.ChooseNext) != 2 || len(seg0.State.Checkpoint) == 0 {
		t.Fatalf("segment 0: %+v", seg0)
	}
	if err := b.RestoreCore(coreState(t, a)); err != nil {
		t.Fatal(err)
	}
	cont := choiceRequest(a, 1, intp(1), seg0.ChooseNext, seg0.State.Checkpoint, "")
	resA, err := a.Spin(cont)
	if err != nil {
		t.Fatal(err)
	}
	resB, err := b.Spin(cont)
	if err != nil {
		t.Fatal(err)
	}
	if mustJSONString(t, resA) != mustJSONString(t, resB) {
		t.Fatal("the continuation differs across machines")
	}
	if !resA.IsGameEnd || resA.Cycle != 1 || resA.Choice == nil || *resA.Choice != 1 || resA.BetID != "bet-1" {
		t.Fatalf("continuation: %+v", resA)
	}

	// 逐段回放：第 1 段以自己的起始快照、choice，以及第 0 段的 choose_next 與 cp 重送。
	c := demoChoiceMachine(t, lab, 5)
	replay := choiceRequest(c, 1, intp(1), seg0.ChooseNext, seg0.State.Checkpoint, resA.State.StartCoreSnapB64U)
	before := coreState(t, c)
	got, err := c.Spin(replay)
	if err != nil {
		t.Fatal(err)
	}
	if mustJSONString(t, got) != mustJSONString(t, resA) {
		t.Fatal("replaying segment 1 differs")
	}
	requireCore(t, c, before)
	// 第 0 段同樣可以回放。
	if got, err := c.Spin(choiceRequest(c, 0, nil, nil, nil, seg0.State.StartCoreSnapB64U)); err != nil || mustJSONString(t, got) != mustJSONString(t, seg0) {
		t.Fatalf("replaying segment 0 differs: %v", err)
	}
}

// optimal 開啟的機台：以起始快照重播第 0 段時跳過抽庫，得到相同的結果，機台恢復到執行前。
func TestOptimalSegmentReplaySkipsDraw(t *testing.T) {
	m := faultMachine(t, true)
	req := &dto.SpinRequest{GameName: m.gameName, GameId: m.gameId, Bet: m.BetUnits[0], BetMode: 0, BetMult: 1}
	first, err := m.Spin(req)
	if err != nil {
		t.Fatal(err)
	}
	before := coreState(t, m)
	req.StartState = &dto.StartState{StartCoreSnapB64U: first.State.StartCoreSnapB64U}
	again, err := m.Spin(req)
	if err != nil {
		t.Fatal(err)
	}
	if mustJSONString(t, again) != mustJSONString(t, first) {
		t.Fatal("replay differs")
	}
	requireCore(t, m, before)
	snap, _ := corefmt.DecodeBase64URL(first.State.StartCoreSnapB64U)
	if sr := m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1, StartSnap: snap, CaptureSnapshots: true}); sr == nil || sr.TotalWin != first.TotalWin {
		t.Fatal("SpinUnsafe replay with StartSnap differs")
	}
	requireCore(t, m, before)
}

package problab

import (
	"errors"
	"reflect"
	"testing"

	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/slot"
)

// chainCP 是 chainLogic 輸出的 checkpoint；每段新配置，交出後不再修改。
type chainCP struct{ N int }

// chainInput 是 chainLogic 收到的續段輸入。
type chainInput struct {
	Cycle      int
	Choice     int
	ChooseNext []int
	Checkpoint any
}

// chainLogic 包住 demo 邏輯，把一局延長為 depth 段：未到最後一段時結果未結束，
// ChooseNext 為 [100+cycle, 200+cycle]，checkpoint 為 &chainCP{cycle + 1}。
// nilAt >= 0 時，在該段回傳 nil。
type chainLogic struct {
	inner  slot.GameLogic
	depth  int
	nilAt  int
	inputs []chainInput
}

func (l *chainLogic) GetResult(r *buf.SpinRequest, g *slot.Game) *buf.SpinResult {
	l.inputs = append(l.inputs, chainInput{r.Cycle, r.Choice, append([]int(nil), r.ChooseNext...), r.Checkpoint})
	if r.Cycle == l.nilAt {
		return nil
	}
	sr := l.inner.GetResult(r, g)
	if r.Cycle < l.depth-1 {
		sr.IsGameEnd = false
		sr.ChooseNext = append(sr.ChooseNext[:0], 100+r.Cycle, 200+r.Cycle)
		sr.State.Checkpoint = &chainCP{N: r.Cycle + 1}
	}
	return sr
}

func chainRunner(t *testing.T, depth, maxSegments int) (*BetRunner, *chainLogic, *Machine) {
	t.Helper()
	m := faultMachine(t, false)
	key := m.gh.GameSetting.LogicKey
	logic := &chainLogic{depth: depth, nilAt: -1}
	reg := slot.NewLogicRegistry()
	if err := reg.Register(key, func(g *slot.Game) (slot.GameLogic, error) {
		inner, err := demo_logic.Logics.Build(key, g)
		logic.inner = inner
		return logic, err
	}); err != nil {
		t.Fatal(err)
	}
	gh, err := slot.NewGame(m.gh.GameSetting, reg, m.core, m.gh.OmitExtend())
	if err != nil {
		t.Fatal(err)
	}
	m.gh = gh
	r, err := NewBetRunner(m, maxSegments)
	if err != nil {
		t.Fatal(err)
	}
	return r, logic, m
}

func requireErrIs(t *testing.T, step string, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s: err %v, want %v", step, err, target)
	}
}

func TestNewBetRunnerValidates(t *testing.T) {
	if _, err := NewBetRunner(nil, 3); err == nil {
		t.Fatal("nil machine accepted")
	}
	if _, err := NewBetRunner(faultMachine(t, false), 0); err == nil {
		t.Fatal("non-positive segment limit accepted")
	}
}

// 四段的局：每段的 Cycle 遞增，送進遊戲的 ChooseNext 與 checkpoint 是上一段的輸出；
// 段結果逐次累計等於完整局，完整局包含所有段的 GMR。
func TestBetRunnerMultiSegment(t *testing.T) {
	r, logic, _ := chainRunner(t, 4, 10)
	sr, err := r.Start(SpinInput{BetMode: 0, BetMult: 2})
	if err != nil {
		t.Fatal(err)
	}
	total, modes := sr.TotalWin, sr.GameModeCount
	prevNext := append([]int(nil), sr.ChooseNext...)
	prevCP := sr.State.Checkpoint
	for cycle, choice := range []int{1, 0, 1} {
		sr, err = r.Choose(choice)
		if err != nil {
			t.Fatal(err)
		}
		in := logic.inputs[len(logic.inputs)-1]
		if in.Cycle != cycle+1 || in.Choice != choice || !reflect.DeepEqual(in.ChooseNext, prevNext) || in.Checkpoint != prevCP {
			t.Fatalf("segment %d received %+v, want next %v and checkpoint %p", cycle+1, in, prevNext, prevCP)
		}
		if sr.Cycle != cycle+1 {
			t.Fatalf("segment cycle %d, want %d", sr.Cycle, cycle+1)
		}
		total += sr.TotalWin
		modes += sr.GameModeCount
		prevNext = append(prevNext[:0], sr.ChooseNext...)
		prevCP = sr.State.Checkpoint
	}
	if !sr.IsGameEnd {
		t.Fatal("the last segment did not end the game")
	}
	res, err := r.Result()
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalWin != total || res.GameModeCount != modes || res.Cycle != 3 || res.Bet != 2*r.m.BetUnits[0] {
		t.Fatalf("game total %d modes %d cycle %d bet %d, want %d %d 3", res.TotalWin, res.GameModeCount, res.Cycle, res.Bet, total, modes)
	}
	if again, err := r.Result(); err != nil || again != res {
		t.Fatal("Result is not repeatable")
	}
	if _, err := r.Choose(0); !errors.Is(err, ErrNoPendingChoice) {
		t.Fatalf("Choose after the end: %v", err)
	}
}

// 段數上限以整局累計：上限 3 時第 4 段被拒絕，局維持進行中，Abandon 後可以重新開始。
func TestBetRunnerSegmentLimit(t *testing.T) {
	r, logic, m := chainRunner(t, 4, 3)
	if _, err := r.Start(SpinInput{BetMode: 0, BetMult: 1}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := r.Choose(0); err != nil {
			t.Fatal(err)
		}
	}
	calls, before := len(logic.inputs), coreState(t, m)
	_, err := r.Choose(0)
	requireErrIs(t, "4th segment", err, ErrSegmentLimit)
	if len(logic.inputs) != calls {
		t.Fatal("the game ran past the limit")
	}
	requireCore(t, m, before)
	_, err = r.Start(SpinInput{BetMode: 0, BetMult: 1})
	requireErrIs(t, "start while pending", err, ErrBetInProgress)
	r.Abandon()
	if _, err := r.Start(SpinInput{BetMode: 0, BetMult: 1}); err != nil {
		t.Fatalf("start after abandon: %v", err)
	}
}

func TestBetRunnerStateRules(t *testing.T) {
	r, logic, m := chainRunner(t, 2, 5)
	_, err := r.Choose(0)
	requireErrIs(t, "choose before start", err, ErrNoPendingChoice)
	_, err = r.Result()
	requireErrIs(t, "result before start", err, ErrBetNotFinished)
	for _, in := range []SpinInput{
		{BetMode: 0, BetMult: 1, Cycle: 1},
		{BetMode: 0, BetMult: 1, ChooseNext: []int{1}},
		{BetMode: 0, BetMult: 1, Checkpoint: &chainCP{}},
		{BetMode: 1, BetMult: 1},
		{BetMode: 0, BetMult: 0},
		{BetMode: 0, BetMult: int(^uint(0) >> 1)},
	} {
		if _, err := r.Start(in); err == nil {
			t.Fatalf("Start accepted %+v", in)
		}
	}
	if len(logic.inputs) != 0 {
		t.Fatal("rejected starts ran the game")
	}

	if _, err := r.Start(SpinInput{BetMode: 0, BetMult: 1}); err != nil {
		t.Fatal(err)
	}
	_, err = r.Result()
	requireErrIs(t, "result while pending", err, ErrBetNotFinished)
	before := coreState(t, m)
	for _, choice := range []int{-1, 2} {
		_, err := r.Choose(choice)
		requireErrIs(t, "choice out of range", err, ErrChoiceOutOfRange)
	}
	requireCore(t, m, before)
	if _, err := r.Choose(1); err != nil {
		t.Fatalf("a valid choice after an invalid one: %v", err)
	}
	done, err := r.Result()
	if err != nil {
		t.Fatal(err)
	}

	// 上一局完成後，Start 的投注檢查失敗保留原狀態；快照被拒絕時上一局已失效。
	if _, err := r.Start(SpinInput{BetMode: 0, BetMult: 0}); err == nil {
		t.Fatal("bad wager accepted")
	}
	if got, err := r.Result(); err != nil || got != done {
		t.Fatal("a rejected Start discarded the finished bet")
	}
	_, err = r.Start(SpinInput{BetMode: 0, BetMult: 1, StartSnap: []byte{1, 2, 3}})
	requireErrIs(t, "rejected snapshot", err, ErrStartSnapshotRejected)
	_, err = r.Result()
	requireErrIs(t, "result after a rejected snapshot", err, ErrBetNotFinished)
	if _, err := r.Start(SpinInput{BetMode: 0, BetMult: 1}); err != nil {
		t.Fatalf("start after a rejected snapshot: %v", err)
	}
}

// 遊戲回傳 nil 後本局不可繼續：Choose、Result、Start 都回報 ErrBetBroken，直到 Abandon。
func TestBetRunnerBrokenAfterGameRejection(t *testing.T) {
	r, logic, _ := chainRunner(t, 3, 5)
	logic.nilAt = 1
	if _, err := r.Start(SpinInput{BetMode: 0, BetMult: 1}); err != nil {
		t.Fatal(err)
	}
	_, err := r.Choose(0)
	requireErrIs(t, "game rejection", err, ErrGameRejected)
	for name, call := range map[string]func() error{
		"choose": func() error { _, err := r.Choose(0); return err },
		"result": func() error { _, err := r.Result(); return err },
		"start":  func() error { _, err := r.Start(SpinInput{BetMode: 0, BetMult: 1}); return err },
	} {
		requireErrIs(t, name, call(), ErrBetBroken)
	}
	logic.nilAt = -1
	r.Abandon()
	if _, err := r.Start(SpinInput{BetMode: 0, BetMult: 1}); err != nil {
		t.Fatalf("start after abandon: %v", err)
	}
}

// 單段遊戲：Start 即完成，Result 是放行的段結果本身。
func TestBetRunnerSingleSegment(t *testing.T) {
	m := faultMachine(t, false)
	r, err := NewBetRunner(m, 1)
	if err != nil {
		t.Fatal(err)
	}
	for range 50 {
		sr, err := r.Start(SpinInput{BetMode: 0, BetMult: 1})
		if err != nil {
			t.Fatal(err)
		}
		res, err := r.Result()
		if err != nil || res != sr || !sr.IsGameEnd {
			t.Fatalf("single segment: %v %v", res, err)
		}
	}
}

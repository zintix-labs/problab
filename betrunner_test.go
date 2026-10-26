package problab

import (
	"errors"
	"reflect"
	"strings"
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
	inner   slot.GameLogic
	depth   int
	nilAt   int
	inputs  []chainInput
	lastWin int // 最後一次回傳的段贏分
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
	l.lastWin = sr.TotalWin
	return sr
}

func chainRunner(t *testing.T, depth int, opts ...BetRunnerOption) (*BetRunner, *chainLogic, *Machine) {
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
	r, err := NewBetRunner(m, opts...)
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
	if _, err := NewBetRunner(nil); err == nil {
		t.Fatal("nil machine accepted")
	}
	for _, n := range []int{0, -1} {
		if _, err := NewBetRunner(faultMachine(t, false), WithMaxSegments(n)); err == nil {
			t.Fatalf("segment limit %d accepted", n)
		}
	}
	r, err := NewBetRunner(faultMachine(t, false), nil)
	if err != nil || r.maxSegments != DefaultMaxSegments || DefaultMaxSegments != 10 {
		t.Fatalf("default limit: %v %v", r, err)
	}
	if r, _ := NewBetRunner(faultMachine(t, false), WithMaxSegments(3)); r.maxSegments != 3 {
		t.Fatalf("WithMaxSegments(3) gave %d", r.maxSegments)
	}
}

func TestChooseModeNames(t *testing.T) {
	for _, c := range []struct {
		s    string
		mode ChooseMode
	}{{"", ChoosePlayer}, {"first", ChooseFirst}, {"last", ChooseLast}} {
		if got, err := ParseChooseMode(c.s); err != nil || got != c.mode {
			t.Fatalf("ParseChooseMode(%q) = %v, %v", c.s, got, err)
		}
	}
	for _, s := range []string{"player", "First", "LAST", " first", "auto"} {
		if _, err := ParseChooseMode(s); !errors.Is(err, ErrInvalidChooseMode) {
			t.Fatalf("ParseChooseMode(%q) error %v", s, err)
		}
	}
	for mode, want := range map[ChooseMode]string{ChoosePlayer: "player", ChooseFirst: "first", ChooseLast: "last", 7: "ChooseMode(7)"} {
		if mode.String() != want {
			t.Fatalf("%d.String() = %q, want %q", uint8(mode), mode.String(), want)
		}
	}
}

// 四段的局：每段的 Cycle 遞增，送進遊戲的 ChooseNext 與 checkpoint 是上一段的輸出；
// 未結束時回傳本段，最後一次回傳完整局，完整局等於各段相加。
func TestBetRunnerMultiSegment(t *testing.T) {
	r, logic, _ := chainRunner(t, 4)
	sr, err := r.Bet(SpinInput{BetMode: 0, BetMult: 2}, ChoosePlayer)
	if err != nil {
		t.Fatal(err)
	}
	total, modes := sr.TotalWin, sr.GameModeCount
	prevNext := append([]int(nil), sr.ChooseNext...)
	prevCP := sr.State.Checkpoint
	for cycle, choice := range []int{1, 0, 1} {
		sr, err = r.Next(choice)
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
		if sr.IsGameEnd {
			break
		}
		total += sr.TotalWin
		modes += sr.GameModeCount
		prevNext = append(prevNext[:0], sr.ChooseNext...)
		prevCP = sr.State.Checkpoint
	}
	if !sr.IsGameEnd {
		t.Fatal("the last call did not end the game")
	}
	// 最後一次回傳的是完整局：不能再加上先前的段。
	last := logic.lastWin
	if sr.TotalWin != total+last || sr.GameModeCount <= modes || sr.Cycle != 3 || sr.Bet != 2*r.m.BetUnits[0] {
		t.Fatalf("game total %d modes %d cycle %d bet %d, want %d > %d 3", sr.TotalWin, sr.GameModeCount, sr.Cycle, sr.Bet, total+last, modes)
	}
	if _, err := r.Next(0); !errors.Is(err, ErrNoPendingChoice) {
		t.Fatalf("Next after the end: %v", err)
	}
}

// player 依位置 0（或最後一個）逐段執行的完整局，與同一起點的 first（或 last）相同。
func TestBetRunnerAutoMatchesPlayer(t *testing.T) {
	for _, c := range []struct {
		mode ChooseMode
		pick func(n int) int
	}{{ChooseFirst, func(int) int { return 0 }}, {ChooseLast, func(n int) int { return n - 1 }}} {
		auto, autoLogic, am := chainRunner(t, 4)
		player, _, pm := chainRunner(t, 4)
		for range 20 {
			want, err := auto.Bet(SpinInput{BetMode: 0, BetMult: 1}, c.mode)
			if err != nil || !want.IsGameEnd {
				t.Fatalf("%v: %v %v", c.mode, want, err)
			}
			got, err := player.Bet(SpinInput{BetMode: 0, BetMult: 1}, ChoosePlayer)
			for err == nil && !got.IsGameEnd {
				got, err = player.Next(c.pick(len(got.ChooseNext)))
			}
			if err != nil {
				t.Fatal(err)
			}
			if segmentJSON(t, got) != segmentJSON(t, want) {
				t.Fatalf("%v: the automatic game differs from the player game", c.mode)
			}
			if string(coreState(t, am)) != string(coreState(t, pm)) {
				t.Fatalf("%v: machine positions differ", c.mode)
			}
		}
		// 每個選擇點選到的位置：chainLogic 的 ChooseNext 在每一段都不同。
		for _, in := range autoLogic.inputs {
			if in.Cycle > 0 && in.Choice != c.pick(len(in.ChooseNext)) {
				t.Fatalf("%v chose %d of %v", c.mode, in.Choice, in.ChooseNext)
			}
		}
	}
}

// 三種模式在單段遊戲上都回傳完整局，結果與機台位置相同。
func TestBetRunnerSingleSegment(t *testing.T) {
	var games []string
	var cores [][]byte
	for _, mode := range []ChooseMode{ChoosePlayer, ChooseFirst, ChooseLast} {
		m := faultMachine(t, false)
		r, err := NewBetRunner(m, WithMaxSegments(1))
		if err != nil {
			t.Fatal(err)
		}
		var out []byte
		for range 50 {
			sr, err := r.Bet(SpinInput{BetMode: 0, BetMult: 1}, mode)
			if err != nil || !sr.IsGameEnd {
				t.Fatalf("%v: %v %v", mode, sr, err)
			}
			out = append(out, segmentJSON(t, sr)...)
		}
		games, cores = append(games, string(out)), append(cores, coreState(t, m))
	}
	for i := 1; i < len(games); i++ {
		if games[i] != games[0] || string(cores[i]) != string(cores[0]) {
			t.Fatal("single-segment results depend on the choose mode")
		}
	}
}

// 段數上限以整局累計。player：上限 3 時第 4 段被拒絕，局維持進行中，Reset 後可以重新開始。
func TestBetRunnerSegmentLimit(t *testing.T) {
	r, logic, m := chainRunner(t, 4, WithMaxSegments(3))
	if _, err := r.Bet(SpinInput{BetMode: 0, BetMult: 1}, ChoosePlayer); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := r.Next(0); err != nil {
			t.Fatal(err)
		}
	}
	calls, before := len(logic.inputs), coreState(t, m)
	_, err := r.Next(0)
	requireErrIs(t, "4th segment", err, ErrSegmentLimit)
	if len(logic.inputs) != calls {
		t.Fatal("the game ran past the limit")
	}
	requireCore(t, m, before)
	if _, err := r.Next(0); !errors.Is(err, ErrSegmentLimit) {
		t.Fatalf("the pending bet changed after the limit: %v", err)
	}
	_, err = r.Bet(SpinInput{BetMode: 0, BetMult: 1}, ChoosePlayer)
	requireErrIs(t, "bet while pending", err, ErrBetInProgress)
	r.Reset()
	if _, err := r.Bet(SpinInput{BetMode: 0, BetMult: 1}, ChoosePlayer); err != nil {
		t.Fatalf("bet after reset: %v", err)
	}

	// first／last：超過上限時本局不可繼續，訊息帶上限。
	for _, mode := range []ChooseMode{ChooseFirst, ChooseLast} {
		r, logic, _ := chainRunner(t, 4, WithMaxSegments(3))
		_, err := r.Bet(SpinInput{BetMode: 0, BetMult: 1}, mode)
		requireErrIs(t, mode.String()+" limit", err, ErrSegmentLimit)
		if !strings.Contains(err.Error(), ": 3") || len(logic.inputs) != 3 {
			t.Fatalf("%v: %v after %d segments", mode, err, len(logic.inputs))
		}
		_, err = r.Next(0)
		requireErrIs(t, mode.String()+" next", err, ErrBetBroken)
		_, err = r.Bet(SpinInput{BetMode: 0, BetMult: 1}, mode)
		requireErrIs(t, mode.String()+" bet", err, ErrBetBroken)
		r.Reset()
		logic.depth = 2
		if _, err := r.Bet(SpinInput{BetMode: 0, BetMult: 1}, mode); err != nil {
			t.Fatalf("%v after reset: %v", mode, err)
		}
	}
}

func TestBetRunnerStateRules(t *testing.T) {
	r, logic, m := chainRunner(t, 2, WithMaxSegments(5))
	_, err := r.Next(0)
	requireErrIs(t, "next before bet", err, ErrNoPendingChoice)
	for _, in := range []SpinInput{
		{BetMode: 0, BetMult: 1, Cycle: 1},
		{BetMode: 0, BetMult: 1, ChooseNext: []int{1}},
		{BetMode: 0, BetMult: 1, Checkpoint: &chainCP{}},
		{BetMode: 1, BetMult: 1},
		{BetMode: 0, BetMult: 0},
		{BetMode: 0, BetMult: int(^uint(0) >> 1)},
	} {
		if _, err := r.Bet(in, ChoosePlayer); err == nil {
			t.Fatalf("Bet accepted %+v", in)
		}
	}
	if len(logic.inputs) != 0 {
		t.Fatal("rejected bets ran the game")
	}

	if _, err := r.Bet(SpinInput{BetMode: 0, BetMult: 1}, ChoosePlayer); err != nil {
		t.Fatal(err)
	}
	before := coreState(t, m)
	_, err = r.Bet(SpinInput{BetMode: 0, BetMult: 1}, ChoosePlayer)
	requireErrIs(t, "bet while pending", err, ErrBetInProgress)
	for _, choice := range []int{-1, 2} {
		_, err := r.Next(choice)
		requireErrIs(t, "choice out of range", err, ErrChoiceOutOfRange)
	}
	requireCore(t, m, before)
	done, err := r.Next(1)
	if err != nil || !done.IsGameEnd {
		t.Fatalf("a valid choice after an invalid one: %v", err)
	}
	doneJSON := segmentJSON(t, done)

	// 上一局完成後：未定義的模式與投注檢查失敗都保留原狀態，不執行、不消耗 RNG。
	before = coreState(t, m)
	calls := len(logic.inputs)
	for _, bad := range []func() error{
		func() error { _, err := r.Bet(SpinInput{BetMode: 0, BetMult: 1}, ChooseMode(3)); return err },
		func() error { _, err := r.Bet(SpinInput{BetMode: 0, BetMult: 0}, ChooseFirst); return err },
	} {
		if bad() == nil {
			t.Fatal("a bad bet was accepted")
		}
	}
	if _, err := r.Bet(SpinInput{BetMode: 0, BetMult: 1}, ChooseMode(9)); !errors.Is(err, ErrInvalidChooseMode) {
		t.Fatalf("invalid mode: %v", err)
	}
	requireCore(t, m, before)
	if len(logic.inputs) != calls || r.state != runnerDone || r.result != done || segmentJSON(t, done) != doneJSON {
		t.Fatal("a rejected Bet discarded the finished bet")
	}

	// 第 0 段的快照被拒絕：回到沒有進行中的局，可以直接開始下一局（各模式皆同）。
	for _, mode := range []ChooseMode{ChoosePlayer, ChooseFirst, ChooseLast} {
		_, err = r.Bet(SpinInput{BetMode: 0, BetMult: 1, StartSnap: []byte{1, 2, 3}}, mode)
		requireErrIs(t, "rejected snapshot", err, ErrStartSnapshotRejected)
		if r.state != runnerIdle {
			t.Fatalf("%v: state %d after a rejected snapshot", mode, r.state)
		}
		if _, err := r.Bet(SpinInput{BetMode: 0, BetMult: 1}, ChooseFirst); err != nil {
			t.Fatalf("%v: bet after a rejected snapshot: %v", mode, err)
		}
	}
	// 自動選擇的局沒有等待中的選擇。
	_, err = r.Next(0)
	requireErrIs(t, "next after an automatic bet", err, ErrNoPendingChoice)
}

// 遊戲回傳 nil 後本局不可繼續：Next、Bet 都回報 ErrBetBroken，直到 Reset。
func TestBetRunnerBrokenAfterGameRejection(t *testing.T) {
	for _, mode := range []ChooseMode{ChoosePlayer, ChooseFirst} {
		r, logic, _ := chainRunner(t, 3, WithMaxSegments(5))
		logic.nilAt = 1
		_, err := r.Bet(SpinInput{BetMode: 0, BetMult: 1}, mode)
		if mode == ChoosePlayer {
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.Next(0)
		}
		requireErrIs(t, mode.String()+" game rejection", err, ErrGameRejected)
		for name, call := range map[string]func() error{
			"next": func() error { _, err := r.Next(0); return err },
			"bet":  func() error { _, err := r.Bet(SpinInput{BetMode: 0, BetMult: 1}, mode); return err },
		} {
			requireErrIs(t, mode.String()+" "+name, call(), ErrBetBroken)
		}
		logic.nilAt = -1
		r.Reset()
		if _, err := r.Bet(SpinInput{BetMode: 0, BetMult: 1}, mode); err != nil {
			t.Fatalf("%v: bet after reset: %v", mode, err)
		}
	}
}

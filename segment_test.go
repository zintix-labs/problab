package problab

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/zintix-labs/problab/corefmt"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
)

// 單段執行的契約：結構不變量、起點路由、指定段快照、結果檢查，以及各入口的錯誤表達。

// segmentLogic 包住 demo 邏輯，記錄遊戲收到的 ChooseNext，並可把結果改為未結束（帶選項）
// 或不一致（未結束卻沒有選項）。
type segmentLogic struct {
	inner      slot.GameLogic
	calls      int
	seen       []int // 最後一次收到的 ChooseNext（未複製，用來檢查是否為 Machine 的副本）
	unfinished bool
	broken     bool
}

func (l *segmentLogic) GetResult(r *buf.SpinRequest, g *slot.Game) *buf.SpinResult {
	l.calls++
	l.seen = r.ChooseNext
	sr := l.inner.GetResult(r, g)
	switch {
	case l.unfinished:
		sr.IsGameEnd = false
		sr.ChooseNext = append(sr.ChooseNext[:0], 1, 2)
	case l.broken:
		sr.IsGameEnd = false
		sr.ChooseNext = sr.ChooseNext[:0]
	}
	return sr
}

func withSegmentLogic(t testing.TB, m *Machine) *segmentLogic {
	t.Helper()
	key := m.gh.GameSetting.LogicKey
	logic := &segmentLogic{}
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
	return logic
}

// reseedCounter 計數 production reseed 的次數。
type reseedCounter struct {
	core.PRNG
	n int
}

func (p *reseedCounter) Reseed() error {
	p.n++
	return p.PRNG.Reseed()
}

func segmentRequest(m *Machine, cycle int, choice *int, next []int) *dto.SpinRequest {
	return &dto.SpinRequest{GameName: m.gameName, GameId: m.gameId, Bet: m.BetUnits[0], BetMode: 0, BetMult: 1,
		Cycle: cycle, Choice: choice, ChooseNext: next}
}

func requireWarnIs(t testing.TB, step string, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s: err %v, want %v", step, err, target)
	}
	if e, ok := err.(*errs.E); !ok || e.ErrLv != errs.Warn {
		t.Fatalf("%s: err %T %v, want *errs.E at Warn", step, err, err)
	}
}

func intp(v int) *int { return &v }

// 違反結構不變量時，兩個入口都在執行前拒絕：遊戲沒被呼叫，RNG 沒有移動，之後照常可用。
func TestSegmentStructureIsRejectedBeforeExecution(t *testing.T) {
	cases := []struct {
		name   string
		cycle  int
		choice int
		next   []int
	}{
		{"negative cycle", -1, 0, nil},
		{"new game with choices", 0, 0, []int{1}},
		{"new game with choice", 0, 1, nil},
		{"continuation without choices", 1, 0, nil},
		{"choice below range", 1, -1, []int{1, 2}},
		{"choice above range", 1, 2, []int{1, 2}},
	}
	for _, optimal := range []bool{false, true} {
		for _, c := range cases {
			t.Run(fmt.Sprintf("optimal=%v/%s", optimal, c.name), func(t *testing.T) {
				m := faultMachine(t, optimal)
				logic := withSegmentLogic(t, m)
				before := coreState(t, m)
				in := SpinInput{BetMode: 0, BetMult: 1, Cycle: c.cycle, Choice: c.choice, ChooseNext: c.next}
				if sr := m.SpinUnsafe(in); sr != nil {
					t.Fatal("SpinUnsafe accepted an invalid segment")
				}
				// DTO 只能表達「有送 choice ⇔ 有 choose_next」，其餘由 Machine 檢查。
				var choice *int
				if len(c.next) > 0 {
					choice = intp(c.choice)
				}
				if c.name != "new game with choice" { // DTO 無法表達：沒有 choose_next 就不能送 choice
					_, err := m.Spin(segmentRequest(m, c.cycle, choice, c.next))
					requireWarnIs(t, "Spin", err, ErrSegmentInput)
				}
				if logic.calls != 0 {
					t.Fatalf("the game was called %d times", logic.calls)
				}
				requireCore(t, m, before)
				if m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1}) == nil {
					t.Fatal("the machine is not usable after a rejection")
				}
			})
		}
	}
}

// optimal 開啟、未指定快照的續段在 reseed 與抽庫之前拒絕；未開 optimal 時正常執行。
func TestContinuationRoutingWithoutSnapshot(t *testing.T) {
	next := []int{3}
	t.Run("optimal production", func(t *testing.T) {
		m, err := newProductionMachineForTest(faultLab(t, core.Default(), true), 0)
		if err != nil {
			t.Fatal(err)
		}
		counter := &reseedCounter{PRNG: m.core.PRNG}
		m.core.PRNG = counter
		logic := withSegmentLogic(t, m)
		before := coreState(t, m)
		_, err = m.Spin(segmentRequest(m, 1, intp(0), next))
		requireWarnIs(t, "Spin", err, ErrContinuationUnsupported)
		if counter.n != 0 || logic.calls != 0 {
			t.Fatalf("reseeds=%d calls=%d, want neither", counter.n, logic.calls)
		}
		requireCore(t, m, before)
		if _, err := m.Spin(segmentRequest(m, 0, nil, nil)); err != nil {
			t.Fatalf("the machine is not usable after a rejection: %v", err)
		}
	})
	t.Run("optimal unsafe", func(t *testing.T) {
		m := faultMachine(t, true)
		logic := withSegmentLogic(t, m)
		before := coreState(t, m)
		if m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1, Cycle: 1, ChooseNext: next}) != nil || logic.calls != 0 {
			t.Fatal("SpinUnsafe ran a continuation without a source")
		}
		requireCore(t, m, before)
	})
	t.Run("without optimal", func(t *testing.T) {
		m := faultMachine(t, false)
		logic := withSegmentLogic(t, m)
		sr := m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1, Cycle: 1, ChooseNext: next})
		if sr == nil || sr.Cycle != 1 || logic.calls != 1 {
			t.Fatalf("continuation without optimal did not run: %v", sr)
		}
		res, err := m.Spin(segmentRequest(m, 2, intp(0), next))
		if err != nil || res.Cycle != 2 || res.Choice == nil || *res.Choice != 0 {
			t.Fatalf("Spin continuation: %+v %v", res, err)
		}
	})
}

// 遊戲收到的 ChooseNext 是 Machine 的副本，不是輸入的 slice。
func TestChooseNextIsCopied(t *testing.T) {
	m := faultMachine(t, false)
	logic := withSegmentLogic(t, m)
	in := []int{4, 5}
	m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1, Cycle: 1, ChooseNext: in})
	if len(logic.seen) != 2 || &logic.seen[0] == &in[0] {
		t.Fatal("the game saw the caller's slice")
	}
	req := segmentRequest(m, 1, intp(1), []int{6, 7})
	if _, err := m.Spin(req); err != nil {
		t.Fatal(err)
	}
	if &logic.seen[0] == &req.ChooseNext[0] || logic.seen[1] != 7 {
		t.Fatal("Spin passed the request's slice to the game")
	}
}

// 指定段的起始快照：跳過抽庫、只作用於該段、回傳後恢復到執行前；被拒絕時不改變 RNG。
func TestStartSnapPerSegment(t *testing.T) {
	m := faultMachine(t, true)
	before := coreState(t, m)
	snap := testDefaultSnapshot(t, 999) // 不在庫中的快照
	sr := m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1, Cycle: 1, ChooseNext: []int{1}, StartSnap: snap, CaptureSnapshots: true})
	if sr == nil || !bytes.Equal(sr.State.StartCoreSnap, snap) {
		t.Fatal("the segment did not start from StartSnap")
	}
	requireCore(t, m, before)
	// 下一次不指定快照：照常抽庫，不沿用上一次的快照。
	sr = m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1, CaptureSnapshots: true})
	if sr == nil || bytes.Equal(sr.State.StartCoreSnap, snap) {
		t.Fatal("StartSnap leaked into the next call")
	}
	// 被拒絕且還原成功：SpinUnsafe 回傳 nil，Spin 維持原本的 Warn 訊息。
	m = faultMachine(t, true)
	before = coreState(t, m)
	if m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1, StartSnap: []byte{1, 2, 3}}) != nil {
		t.Fatal("SpinUnsafe ran a rejected snapshot")
	}
	requireCore(t, m, before)
	req := segmentRequest(m, 0, nil, nil)
	req.StartState = &dto.StartState{StartCoreSnapB64U: corefmt.EncodeBase64URL([]byte{1, 2, 3})}
	_, err := m.Spin(req)
	if e, ok := err.(*errs.E); !ok || e.ErrLv != errs.Warn || e.Cause != nil {
		t.Fatalf("rejected snapshot: %T %v, want the existing Warn without a cause", err, err)
	}
	requireCore(t, m, before)
}

// 結果違反輸出契約：Spin panic，SpinUnsafe 不檢查；由抽庫決定起點的結果未結束：所有入口 panic，
// RNG 停在抽庫後的位置，Machine 依棄用規則不再使用。
func TestSegmentOutputChecks(t *testing.T) {
	m := faultMachine(t, false)
	withSegmentLogic(t, m).broken = true
	if catchPanic(func() { _, _ = m.Spin(segmentRequest(m, 0, nil, nil)) }) == nil {
		t.Fatal("Spin accepted an inconsistent segment")
	}
	if catchPanic(func() {
		if m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1}) == nil {
			t.Error("SpinUnsafe dropped the result")
		}
	}) != nil {
		t.Fatal("SpinUnsafe checked the output")
	}

	recovery := pickedRecoveryPoint(t)
	for _, name := range []string{"Spin", "SpinUnsafe"} {
		m := faultMachine(t, true)
		withSegmentLogic(t, m).unfinished = true
		p := catchPanic(func() {
			if name == "Spin" {
				_, _ = m.Spin(segmentRequest(m, 0, nil, nil))
				return
			}
			m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1})
		})
		if p == nil {
			t.Fatalf("%s: an unfinished drawn segment did not panic", name)
		}
		requireCore(t, m, recovery)
	}
	// 未開 optimal 時，未結束的結果是合法的選擇點。
	m = faultMachine(t, false)
	withSegmentLogic(t, m).unfinished = true
	res, err := m.Spin(segmentRequest(m, 0, nil, nil))
	if err != nil || res.IsGameEnd || len(res.ChooseNext) != 2 {
		t.Fatalf("choice point without optimal: %+v %v", res, err)
	}
}

// fillSegment 逐欄寫入私有請求；buf.SpinRequest 新增欄位時，這裡提醒必須同步寫入。
func TestFillSegmentWritesEveryField(t *testing.T) {
	if n := reflect.TypeOf(buf.SpinRequest{}).NumField(); n != 9 {
		t.Fatalf("buf.SpinRequest has %d fields; update fillSegment and this test", n)
	}
	m := faultMachine(t, false)
	m.spinRequest = buf.SpinRequest{GameName: "x", GameId: 9, Bet: 1, BetMode: 1, BetMult: 1, Cycle: 1, Choice: 1, ChooseNext: []int{1}, Checkpoint: 1}
	m.fillSegment(0, 2, 0, 0, nil, nil, nil)
	want := buf.SpinRequest{GameName: m.gameName, GameId: m.gameId, Bet: 2 * m.BetUnits[0], BetMode: 0, BetMult: 2}
	got := m.spinRequest
	if len(got.ChooseNext) != 0 {
		t.Fatalf("stale ChooseNext %v", got.ChooseNext)
	}
	got.ChooseNext = nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fillSegment left stale fields:\n got %+v\nwant %+v", got, want)
	}
}

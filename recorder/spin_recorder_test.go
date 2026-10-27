// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package recorder

import (
	"math/rand/v2"
	"testing"

	"github.com/zintix-labs/problab/sdk/buf"
)

// An assembled whole game is recorded once: the wager and the round count only
// once, the first mode of cycle 0 as the base win, the rest as the free win,
// and a trigger from the game's total mode count.
func TestRecordAssembledGame(t *testing.T) {
	seg := func(cycle int, end bool, wins ...int) *buf.SpinResult {
		sr := &buf.SpinResult{Bet: 40, BetMult: 1, Cycle: cycle, IsGameEnd: end}
		for _, w := range wins {
			sr.TotalWin += w
			sr.GameModeCount++
			sr.GameModeList = append(sr.GameModeList, &buf.GameModeResult{TotalWin: w, TmpAct: &buf.TmpAct{}})
		}
		return sr
	}
	a := buf.NewSpinAssembler()
	var game *buf.SpinResult
	var err error
	for _, sr := range []*buf.SpinResult{seg(0, false, 10), seg(1, false, 20), seg(2, true, 30, 5)} {
		if game, err = a.Add(sr); err != nil {
			t.Fatal(err)
		}
	}
	r, err := NewSpinRecorder("g", 0, []int{40}, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	r.Record(game)
	r.Record(seg(0, true, 7)) // 單段、只有 BG 的一局
	b := r.Basic
	if b.Rounds != 2 || b.TotalBet != 80 {
		t.Fatalf("rounds=%d total bet=%d, want 2 and 80", b.Rounds, b.TotalBet)
	}
	if b.TotalWin != 72 || b.BaseWin != 17 || b.FreeWin != 55 {
		t.Fatalf("total=%d base=%d free=%d, want 72, 17, 55", b.TotalWin, b.BaseWin, b.FreeWin)
	}
	if b.Trigger != 1 {
		t.Fatalf("trigger=%d, want 1 (only the assembled game has more than one mode)", b.Trigger)
	}
}

// game 組出一局：BG 贏分 base；fg 為 nil 表示沒有觸發，否則每個值是一個 FG 的 GMR。
func game(bet, base int, fg ...int) *buf.SpinResult {
	sr := &buf.SpinResult{Bet: bet, BetMult: 1, IsGameEnd: true}
	for _, w := range append([]int{base}, fg...) {
		sr.TotalWin += w
		sr.GameModeCount++
		sr.GameModeList = append(sr.GameModeList, &buf.GameModeResult{TotalWin: w, TmpAct: &buf.TmpAct{}})
	}
	return sr
}

type recordedGame struct {
	base int64
	fg   []int64 // nil：沒有觸發
}

func recordedGames() []recordedGame {
	r := rand.New(rand.NewPCG(3, 5))
	games := make([]recordedGame, 30000)
	for i := range games {
		games[i].base = int64(r.IntN(120))
		if r.IntN(40) == 0 {
			games[i].fg = []int64{int64(r.IntN(3000))}
			if r.IntN(4) == 0 {
				games[i].fg = append(games[i].fg, 0, int64(r.IntN(800))) // 再觸發，含贏分為 0 的 FG
			}
		}
	}
	games[0].fg = []int64{0} // 觸發但 FG 贏分為 0，照樣計入 FreeCv
	return games
}

func newRecorder(t *testing.T, unit int) *SpinRecorder {
	t.Helper()
	r, err := NewSpinRecorder("g", 0, []int{unit}, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func recordAll(r *SpinRecorder, unit int, games []recordedGame) {
	for _, g := range games {
		fg := make([]int, len(g.fg))
		for i, w := range g.fg {
			fg[i] = int(w)
		}
		r.Record(game(unit, int(g.base), fg...))
	}
}

// expected 以精確值算出 Total 的 std、所有局 BG 的 CV、觸發局 FG 的 CV。
func expected(games []recordedGame, unit int64) (std, baseCv, freeCv float64) {
	var totals, bases, frees []int64
	for _, g := range games {
		total := g.base
		bases = append(bases, g.base)
		if g.fg != nil {
			free := int64(0)
			for _, w := range g.fg {
				free += w
			}
			frees = append(frees, free)
			total += free
		}
		totals = append(totals, total)
	}
	cv := func(wins []int64) float64 {
		s, m := exactStd(wins, unit)
		if len(wins) < 2 || m <= 0 {
			return 0
		}
		return s / m
	}
	std, _ = exactStd(totals, unit)
	return std, cv(bases), cv(frees)
}

func TestRecorderStdAndCv(t *testing.T) {
	games := recordedGames()
	r := newRecorder(t, 40)
	recordAll(r, 40, games)
	rep := r.Done()
	rep.Done()
	std, baseCv, freeCv := expected(games, 40)
	if !near(rep.Summary.Std, std) || !near(rep.Std(), std) || !near(rep.Summary.BaseCv, baseCv) || !near(rep.Summary.FreeCv, freeCv) {
		t.Fatalf("std %v base cv %v free cv %v, want %v %v %v", rep.Summary.Std, rep.Summary.BaseCv, rep.Summary.FreeCv, std, baseCv, freeCv)
	}
	if want := std / rep.Summary.RTP; !near(rep.Summary.Cv, want) {
		t.Fatalf("cv %v, want std / RTP = %v", rep.Summary.Cv, want)
	}
}

// 沒有觸發或只觸發一次時 FreeCv 為 0；全部為 0 時三個值都是 0。
func TestRecorderUndefinedCv(t *testing.T) {
	for name, c := range map[string]struct {
		games []recordedGame
		zero  func(*SpinRecorder) bool
	}{
		"no trigger":  {[]recordedGame{{base: 10}, {base: 30}}, func(r *SpinRecorder) bool { return r.Done().Summary.FreeCv == 0 }},
		"one trigger": {[]recordedGame{{base: 10, fg: []int64{50}}, {base: 30}}, func(r *SpinRecorder) bool { return r.Done().Summary.FreeCv == 0 }},
		"all zero": {[]recordedGame{{base: 0}, {base: 0, fg: []int64{0}}, {base: 0, fg: []int64{0}}}, func(r *SpinRecorder) bool {
			s := r.Done().Summary
			return s.Std == 0 && s.BaseCv == 0 && s.FreeCv == 0
		}},
	} {
		r := newRecorder(t, 40)
		recordAll(r, 40, c.games)
		if !c.zero(r) {
			t.Errorf("%s: %+v", name, r.Done().Summary)
		}
	}
}

// 依 worker 順序合併的結果與單一 Recorder 在容差內相同，同一順序重算逐位元相同；沒有觸發的
// worker 不影響 FreeCv。
func TestMergeSpinRecorderCombinesMoments(t *testing.T) {
	games := recordedGames()
	single := newRecorder(t, 40)
	recordAll(single, 40, games)
	want := single.Done().Summary
	merged := func() *SpinRecorder {
		parts := []*SpinRecorder{newRecorder(t, 40), newRecorder(t, 40), newRecorder(t, 40), newRecorder(t, 40)}
		for i, g := range games {
			recordAll(parts[i%3], 40, []recordedGame{g}) // parts[3] 保持為空
		}
		recordAll(parts[3], 40, []recordedGame{{base: 5}}) // 只有一局、沒有觸發
		m, err := MergeSpinRecorder(parts)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	all := append(append([]recordedGame(nil), games...), recordedGame{base: 5})
	std, baseCv, freeCv := expected(all, 40)
	got := merged().Done().Summary
	if !near(got.Std, std) || !near(got.BaseCv, baseCv) || !near(got.FreeCv, freeCv) {
		t.Fatalf("merged std %v base %v free %v, want %v %v %v", got.Std, got.BaseCv, got.FreeCv, std, baseCv, freeCv)
	}
	if again := merged().Done().Summary; again.Std != got.Std || again.BaseCv != got.BaseCv || again.FreeCv != got.FreeCv {
		t.Fatal("the same merge order gave a different result")
	}
	if want.Rounds != len(games) {
		t.Fatalf("rounds %d", want.Rounds)
	}
}

// 押注單位與贏分都很大：現行的 int 平方和會溢位，Welford 的 std 與 CV 仍與精確值相同。
func TestRecorderBeyondIntSquareSums(t *testing.T) {
	const unit = 1000
	games := make([]recordedGame, 100)
	for i := range games {
		games[i].base = int64(i%2) * 3_500_000_000
		if i%3 == 0 {
			games[i].fg = []int64{int64(i) * 40_000_000}
		}
	}
	r := newRecorder(t, unit)
	recordAll(r, unit, games)
	s := r.Done().Summary
	std, baseCv, freeCv := expected(games, unit)
	if !near(s.Std, std) || !near(s.BaseCv, baseCv) || !near(s.FreeCv, freeCv) {
		t.Fatalf("std %v base %v free %v, want %v %v %v", s.Std, s.BaseCv, s.FreeCv, std, baseCv, freeCv)
	}
}

// Copyright 2026 Zintix Labs
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

package dev

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"
	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/internal/optimalrt"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/sampler"
	"github.com/zintix-labs/problab/server/netsvr"
	"github.com/zintix-labs/problab/server/svrcfg"
	"github.com/zintix-labs/problab/spec"
)

// testRouter 以 chi 實作 NetRouter。
type testRouter struct{ r chi.Router }

func (s *testRouter) Use(mw func(http.Handler) http.Handler) { s.r.Use(mw) }
func (s *testRouter) Get(p string, h http.HandlerFunc)       { s.r.Get(p, h) }
func (s *testRouter) Post(p string, h http.HandlerFunc)      { s.r.Post(p, h) }
func (s *testRouter) Put(p string, h http.HandlerFunc)       { s.r.Put(p, h) }
func (s *testRouter) Delete(p string, h http.HandlerFunc)    { s.r.Delete(p, h) }
func (s *testRouter) Group(p string, fn func(netsvr.NetRouter)) {
	s.r.Route(p, func(r chi.Router) { fn(&testRouter{r: r}) })
}

func newServer(t *testing.T, lab *problab.Problab) *httptest.Server {
	t.Helper()
	rt := &testRouter{r: chi.NewRouter()}
	Register(rt, &svrcfg.SvrCfg{Problab: lab, Mode: svrcfg.ModeDev})
	ts := httptest.NewServer(rt.r)
	t.Cleanup(ts.Close)
	return ts
}

func demoLab(t *testing.T) *problab.Problab {
	t.Helper()
	lab, err := problab.NewAuto(core.Default(), problab.Configs(demo_configs.FS), problab.Logics(demo_logic.Logics))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	return lab
}

func post(t *testing.T, ts *httptest.Server, path string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	res, err := ts.Client().Post(ts.URL+path, "application/json", rd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, raw
}

func spin(t *testing.T, ts *httptest.Server, req map[string]any) spinResponse {
	t.Helper()
	code, raw := post(t, ts, "/dev/spin", req)
	if code != http.StatusOK {
		t.Fatalf("/dev/spin %v: status %d body %s", req, code, raw)
	}
	var resp spinResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func seedReq(gid int, choose, value string, rounds int) map[string]any {
	return map[string]any{"gid": gid, "betmode": 0, "choose": choose, "input": "seed", "value": value, "rounds": rounds}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// replayInput 依原局每一段的結果組出回放輸入：續段的 choose_next 與 cp 取自上一段。
func replayInput(b bet) []map[string]any {
	segs := make([]map[string]any, len(b.Segments))
	for i, s := range b.Segments {
		in := map[string]any{"start_b64u": s.State.StartCoreSnapB64U, "cycle": i}
		if i > 0 {
			prev := b.Segments[i-1]
			in["choice"] = *s.Choice
			in["choose_next"] = prev.ChooseNext
			if len(prev.State.Checkpoint) != 0 {
				in["cp"] = prev.State.Checkpoint
			}
		}
		segs[i] = in
	}
	return segs
}

// ---------- optimal fixtures ----------

// publishBank 以 raw 機台依序的快照建立一份均勻權重的庫；任何快照都是合法的 seed。
func publishBank(t *testing.T, root, name string, gid spec.GID, seed int64, n int) (dist, bank string) {
	t.Helper()
	raw := demoLab(t)
	m, err := raw.NewMachine(gid, problab.MachineConfig{Seed: problab.SeedInt64(seed)})
	if err != nil {
		t.Fatal(err)
	}
	var seeds []byte
	weights := make([]float64, n)
	for i := range n {
		snap, err := m.SnapshotCore()
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, snap...)
		weights[i] = 1
		m.SpinUnsafe(problab.SpinInput{BetMode: 0, BetMult: 1})
	}
	published, err := optimalrt.PublishGachaPair(filepath.Join(root, name), optimalrt.GachaPair{
		Picker: sampler.BuildAliasTableF64(weights), SeedLen: len(seeds) / n, SeedBank: seeds,
	})
	if err != nil {
		t.Fatal(err)
	}
	dist, _ = filepath.Rel(root, published.Gacha)
	bank, _ = filepath.Rel(root, published.SeedBank)
	return filepath.ToSlash(dist), filepath.ToSlash(bank)
}

var optimalBlock = regexp.MustCompile(`(?m)^optimal_setting:\n(?:  .*\n)*`)

func optimalLab(t *testing.T, config, block, root string) *problab.Problab {
	t.Helper()
	raw, err := fs.ReadFile(demo_configs.FS, config)
	if err != nil {
		t.Fatal(err)
	}
	if !optimalBlock.Match(raw) {
		t.Fatalf("%s has no optimal_setting block", config)
	}
	cfg := fstest.MapFS{"game.yaml": &fstest.MapFile{Data: optimalBlock.ReplaceAllLiteral(raw, []byte(block))}}
	lab, err := problab.NewAuto(core.Default(), problab.Configs(cfg), problab.Logics(demo_logic.Logics), problab.WithOptimalDir(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	return lab
}

// optimalNormalLab 是開啟 optimal 的 demo_0：start 庫有 1000 筆 seed。
func optimalNormalLab(t *testing.T) *problab.Problab {
	root := t.TempDir()
	dist, bank := publishBank(t, root, "start", 0, 7, 1000)
	block := fmt.Sprintf("optimal_setting:\n  use_optimal: true\n  gachas: [%s]\n  seed_bank: [%s]\n\n", dist, bank)
	return optimalLab(t, "game_0_demonormal.yaml", block, root)
}

// optimalEntryLab 是開啟 optimal 的 demo_3：start 與入口 3、7 各有一份庫，續段依入口抽庫。
func optimalEntryLab(t *testing.T) *problab.Problab {
	root := t.TempDir()
	slot := func(name string, seed int64) string {
		dist, bank := publishBank(t, root, name, 3, seed, 64)
		return fmt.Sprintf("{dist: %s, bank: %s}", dist, bank)
	}
	block := fmt.Sprintf("optimal_setting:\n  use_optimal: true\n  modes:\n    - start: %s\n      after:\n        3: %s\n        7: %s\n",
		slot("start", 100), slot("a", 101), slot("b", 102))
	return optimalLab(t, "game_3_demoentry.yaml", block, root)
}

// ---------- tests ----------

// 同一組輸入兩次結果完全相同；seed 留空時回傳的值貼回後也相同。
func TestSpinIsDeterministic(t *testing.T) {
	ts := newServer(t, demoLab(t))
	for gid := range 4 {
		for _, choose := range []string{"first", "last"} {
			first := spin(t, ts, seedReq(gid, choose, "hex:0a0b", 200))
			second := spin(t, ts, seedReq(gid, choose, "hex:0a0b", 200))
			if first.Seed != "hex:0a0b" || len(first.Bets) != 200 || mustJSON(t, first) != mustJSON(t, second) {
				t.Fatalf("gid %d %s: two runs differ (seed %q, %d bets)", gid, choose, first.Seed, len(first.Bets))
			}
			for _, b := range first.Bets {
				if !b.IsEnd || b.Segments[0].Cycle != 0 {
					t.Fatalf("gid %d %s: bet %d unfinished", gid, choose, b.Index)
				}
			}
		}
	}
	auto := spin(t, ts, seedReq(0, "first", "", 50))
	again := spin(t, ts, seedReq(0, "first", auto.Seed, 50))
	if auto.Seed == "" || mustJSON(t, auto.Bets) != mustJSON(t, again.Bets) {
		t.Fatalf("generated seed %q does not reproduce the batch", auto.Seed)
	}
}

// 一局的贏分是各段贏分的加總；總計只算已結束的局。
func TestSpinTotalsSumSegments(t *testing.T) {
	ts := newServer(t, demoLab(t))
	resp := spin(t, ts, seedReq(2, "first", "5", 500))
	multi, totalBet, totalWin := 0, 0, 0
	for _, b := range resp.Bets {
		win := 0
		for _, s := range b.Segments {
			win += s.TotalWin
		}
		if len(b.Segments) > 1 {
			multi++
		}
		if b.Win != win || b.Bet != 40 {
			t.Fatalf("bet %d: win %d segments %d bet %d", b.Index, b.Win, win, b.Bet)
		}
		totalBet += b.Bet
		totalWin += b.Win
	}
	if multi == 0 || resp.TotalBet != totalBet || resp.TotalWin != totalWin {
		t.Fatalf("multi %d totals %d/%d want %d/%d", multi, resp.TotalBet, resp.TotalWin, totalBet, totalWin)
	}
}

func TestSpinRoundsClamp(t *testing.T) {
	ts := newServer(t, demoLab(t))
	resp := spin(t, ts, seedReq(0, "first", "1", 1500))
	if resp.Rounds != maxRounds || len(resp.Bets) != maxRounds || !resp.Clamped || resp.RequestedRounds != 1500 {
		t.Fatalf("rounds %d bets %d clamped %v", resp.Rounds, len(resp.Bets), resp.Clamped)
	}
	resp = spin(t, ts, seedReq(0, "first", "1", 0))
	if resp.Rounds != 1 || resp.Clamped {
		t.Fatalf("rounds 0 => %d clamped %v", resp.Rounds, resp.Clamped)
	}
	resp = spin(t, ts, seedReq(2, "player", "1", 50))
	if resp.Rounds != 1 || len(resp.Bets) != 1 || resp.Clamped {
		t.Fatalf("player => rounds %d", resp.Rounds)
	}
	snap := resp.Bets[0].Segments[0].State.StartCoreSnapB64U
	resp = spin(t, ts, map[string]any{"gid": 2, "betmode": 0, "choose": "first", "input": "snap", "value": snap, "rounds": 50})
	if resp.Rounds != 1 || len(resp.Bets) != 1 || resp.Seed != "" {
		t.Fatalf("snap => rounds %d seed %q", resp.Rounds, resp.Seed)
	}
}

// player 停在選擇點；以 /dev/next 一路選到局結束，格式與正式環境的續段相同。
func TestPlayerStopsAndNextFinishes(t *testing.T) {
	ts := newServer(t, demoLab(t))
	var b bet
	for seed := range 2000 {
		b = spin(t, ts, seedReq(2, "player", fmt.Sprint(seed), 1)).Bets[0]
		if !b.IsEnd {
			break
		}
	}
	if b.IsEnd || len(b.Segments) != 1 || len(b.Segments[0].ChooseNext) == 0 {
		t.Fatal("no seed stopped at a choice point")
	}
	seg := b.Segments[0]
	for cycle := 1; !seg.IsGameEnd; cycle++ {
		if cycle > problab.DefaultMaxSegments {
			t.Fatal("never finished")
		}
		code, raw := post(t, ts, "/dev/next", map[string]any{
			"gid": 2, "betmode": 0, "cycle": cycle, "choice": len(seg.ChooseNext) - 1,
			"choose_next": seg.ChooseNext, "cp": seg.State.Checkpoint,
		})
		if code != http.StatusOK {
			t.Fatalf("/dev/next cycle %d: status %d body %s", cycle, code, raw)
		}
		var next nextResponse
		if err := json.Unmarshal(raw, &next); err != nil {
			t.Fatal(err)
		}
		if next.Segment.Cycle != cycle || next.Segment.Choice == nil || *next.Segment.Choice != len(seg.ChooseNext)-1 {
			t.Fatalf("cycle %d: got cycle %d choice %v", cycle, next.Segment.Cycle, next.Segment.Choice)
		}
		seg = next.Segment
	}
}

// snap 回放：第 0 段完全重現（含開啟 optimal 的機台）；多段局標示只保證第 0 段。
func TestSnapReplaysFirstSegment(t *testing.T) {
	for _, c := range []struct {
		name string
		lab  *problab.Problab
		gid  int
	}{
		{"demo_0", demoLab(t), 0},
		{"demo_0 optimal", optimalNormalLab(t), 0},
		{"demo_2", demoLab(t), 2},
	} {
		ts := newServer(t, c.lab)
		batch := spin(t, ts, seedReq(c.gid, "first", "11", 300))
		partial := 0
		for _, b := range batch.Bets {
			got := spin(t, ts, map[string]any{"gid": c.gid, "betmode": 0, "choose": "first", "input": "snap", "value": b.Segments[0].State.StartCoreSnapB64U})
			if mustJSON(t, got.Bets[0].Segments[0]) != mustJSON(t, b.Segments[0]) {
				t.Fatalf("%s bet %d: segment 0 not reproduced", c.name, b.Index)
			}
			if got.PartialReplay != (len(got.Bets[0].Segments) > 1) {
				t.Fatalf("%s bet %d: partial_replay %v with %d segments", c.name, b.Index, got.PartialReplay, len(got.Bets[0].Segments))
			}
			if got.PartialReplay {
				partial++
			}
		}
		if c.gid == 2 && partial == 0 {
			t.Fatalf("%s: no multi-segment bet exercised", c.name)
		}
	}
}

// 回放這局：每段從自己的快照開始，多段局完全重現，包含 optimal 依入口抽庫的續段。
func TestReplayReproducesWholeBet(t *testing.T) {
	for _, c := range []struct {
		name string
		lab  *problab.Problab
		gid  int
	}{
		{"demo_2", demoLab(t), 2},
		{"demo_3 optimal entries", optimalEntryLab(t), 3},
	} {
		ts := newServer(t, c.lab)
		for _, choose := range []string{"first", "last"} {
			batch := spin(t, ts, seedReq(c.gid, choose, "21", 300))
			multi := 0
			for _, b := range batch.Bets {
				if len(b.Segments) == 1 {
					continue
				}
				multi++
				code, raw := post(t, ts, "/dev/replay", map[string]any{"gid": c.gid, "betmode": 0, "segments": replayInput(b)})
				if code != http.StatusOK {
					t.Fatalf("%s %s bet %d: status %d body %s", c.name, choose, b.Index, code, raw)
				}
				var got replayResponse
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatal(err)
				}
				got.Bet.Index = b.Index
				if mustJSON(t, got.Bet) != mustJSON(t, b) {
					t.Fatalf("%s %s bet %d: replay differs", c.name, choose, b.Index)
				}
			}
			if multi == 0 {
				t.Fatalf("%s %s: no multi-segment bet exercised", c.name, choose)
			}
		}
	}
}

func TestInputErrors(t *testing.T) {
	ts := newServer(t, demoLab(t))
	snap := spin(t, ts, seedReq(2, "first", "3", 1)).Bets[0].Segments[0].State.StartCoreSnapB64U
	for _, c := range []struct {
		path string
		body any
		want string
	}{
		{"/dev/spin", map[string]any{"betmode": 0, "choose": "first", "input": "seed"}, "gid is required"},
		{"/dev/spin", map[string]any{"gid": 0, "choose": "first", "input": "seed"}, "betmode is required"},
		{"/dev/spin", map[string]any{"gid": 0, "betmode": 0, "input": "seed"}, "choose is required"},
		{"/dev/spin", map[string]any{"gid": 0, "betmode": 0, "choose": "auto", "input": "seed"}, "choose must be first, last or player"},
		{"/dev/spin", map[string]any{"gid": 0, "betmode": 0, "choose": "first"}, "input is required"},
		{"/dev/spin", map[string]any{"gid": 0, "betmode": 0, "choose": "first", "input": "hex"}, "input must be seed or snap"},
		{"/dev/spin", seedReq(0, "first", "abc", 1), "invalid seed"},
		{"/dev/spin", seedReq(0, "first", "9223372036854775808", 1), "invalid seed"},
		{"/dev/spin", map[string]any{"gid": 0, "betmode": 0, "choose": "first", "input": "snap", "value": " "}, "snap must not be empty"},
		{"/dev/spin", map[string]any{"gid": 0, "betmode": 0, "choose": "first", "input": "snap", "value": "not base64url!"}, "snap must be base64url"},
		{"/dev/spin", map[string]any{"gid": 0, "betmode": 9, "choose": "first", "input": "seed"}, "betmode 9 out of range"},
		{"/dev/spin", map[string]any{"gid": 99, "betmode": 0, "choose": "first", "input": "seed"}, "gid 99 not found"},
		{"/dev/spin", map[string]any{"gid": 0, "betmode": 0, "choose": "first", "input": "seed", "seed": "1"}, "unknown field"},
		{"/dev/spin", []byte(`{"gid": 0} {}`), "unexpected data"},
		{"/dev/next", map[string]any{"gid": 2, "betmode": 0, "cycle": 0, "choice": 0, "choose_next": []int{3}}, "cycle must be at least 1"},
		{"/dev/next", map[string]any{"gid": 2, "betmode": 0, "cycle": 1, "choice": 0}, "choose_next must not be empty"},
		{"/dev/next", map[string]any{"gid": 2, "betmode": 0, "cycle": 1, "choose_next": []int{3}}, "choice is required"},
		{"/dev/next", map[string]any{"gid": 2, "betmode": 0, "cycle": 1, "choice": 1, "choose_next": []int{3}}, "choice 1 out of range"},
		{"/dev/replay", map[string]any{"gid": 2, "betmode": 0, "segments": []any{}}, "segments must contain 1 to"},
		{"/dev/replay", map[string]any{"gid": 2, "betmode": 0, "segments": []any{map[string]any{"cycle": 0}}}, "segment 0 has no start_b64u"},
		{"/dev/replay", map[string]any{"gid": 2, "betmode": 0, "segments": []any{map[string]any{"start_b64u": snap, "cycle": 1}}}, "cycles must be 0, 1, 2"},
		{"/dev/replay", map[string]any{"gid": 2, "betmode": 0, "segments": []any{map[string]any{"start_b64u": snap, "cycle": 0, "choice": 0}}}, "segment 0 must not carry"},
		{"/dev/replay", map[string]any{"gid": 2, "betmode": 0, "segments": []any{
			map[string]any{"start_b64u": snap, "cycle": 0},
			map[string]any{"start_b64u": snap, "cycle": 1, "choose_next": []int{3}},
		}}, "choice is required"},
	} {
		code, raw := post(t, ts, c.path, c.body)
		if code != http.StatusBadRequest || !strings.Contains(string(raw), c.want) {
			t.Errorf("%s %v: status %d body %s; want 400 containing %q", c.path, c.body, code, raw, c.want)
		}
	}
}

func TestRequestLimits(t *testing.T) {
	ts := newServer(t, demoLab(t))
	huge := []byte(`{"gid": 0, "betmode": 0, "choose": "first", "input": "seed", "value": "utf8:` + strings.Repeat("x", maxBodySize) + `"}`)
	code, raw := post(t, ts, "/dev/spin", huge)
	if code != http.StatusBadRequest || !strings.Contains(string(raw), "request body exceeds") {
		t.Fatalf("huge body: status %d body %.200s", code, raw)
	}
	segs := make([]map[string]any, problab.DefaultMaxSegments+1)
	for i := range segs {
		segs[i] = map[string]any{"start_b64u": "AA", "cycle": i}
	}
	code, raw = post(t, ts, "/dev/replay", map[string]any{"gid": 2, "betmode": 0, "segments": segs})
	if code != http.StatusBadRequest || !strings.Contains(string(raw), "segments must contain 1 to 10 entries") {
		t.Fatalf("too many segments: status %d body %s", code, raw)
	}
}

// /dev/meta 只輸出畫面需要的欄位，不送出盤面產生、賠率與中獎判定的設定。
func TestMetaFieldWhitelist(t *testing.T) {
	ts := newServer(t, demoLab(t))
	res, err := ts.Client().Get(ts.URL + "/dev/meta")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var meta map[string]any
	if err := json.NewDecoder(res.Body).Decode(&meta); err != nil {
		t.Fatal(err)
	}
	keys := func(m map[string]any) string { return strings.Join(slices.Sorted(maps.Keys(m)), ",") }
	if got := keys(meta); got != "games,max_rounds,max_segments" {
		t.Fatalf("top-level keys %s", got)
	}
	games := meta["games"].([]any)
	if len(games) != 4 || meta["max_rounds"].(float64) != maxRounds || meta["max_segments"].(float64) != problab.DefaultMaxSegments {
		t.Fatalf("meta %v", meta)
	}
	for _, g := range games {
		game := g.(map[string]any)
		if got := keys(game); got != "bet_units,fixed,gid,logic,max_win_limit,modes,name" {
			t.Fatalf("game keys %s", got)
		}
		for _, m := range game["modes"].([]any) {
			mode := m.(map[string]any)
			if got := keys(mode); got != "columns,rows,symbols" {
				t.Fatalf("mode keys %s", got)
			}
			if len(mode["symbols"].([]any)) == 0 || mode["columns"].(float64) < 1 || mode["rows"].(float64) < 1 {
				t.Fatalf("mode %v", mode)
			}
		}
	}
}

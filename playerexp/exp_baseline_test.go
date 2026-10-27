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

package playerexp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/demo/demo_tags"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/playerexp"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/strategy"
	"github.com/zintix-labs/problab/sdk/strategy/builtin"
	"github.com/zintix-labs/problab/spec"
)

// 暫時基準 B1：單段遊戲（demo_0、demo_1）的報告、CSV，以及每次策略回呼收到的帳本、結果
// （DTO 與兩個快照）與回傳的決策。驗收後刪除。

const expBaselinePath = "testdata/exp_baseline.json"

func expHash(raw []byte) string {
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:12])
}

// recordingFactory 包裝另一個 factory，記錄每次回呼的輸入與決策。
type recordingFactory struct {
	inner strategy.Factory
	log   *[]string
}

func (f recordingFactory) NewStrategy(initial int) strategy.Strategy {
	*f.log = append(*f.log, fmt.Sprintf("new %d", initial))
	return &recordingStrategy{inner: f.inner.NewStrategy(initial), log: f.log}
}

type recordingStrategy struct {
	inner strategy.Strategy
	log   *[]string
}

func expDecision(d strategy.Decision) string {
	return fmt.Sprintf("keep=%v mode=%d mult=%d end=%d", d.KeepPlay, d.BetMode, d.BetMult, d.EndType)
}

func (s *recordingStrategy) FirstSpin(p strategy.PlayerState) strategy.Decision {
	d := s.inner.FirstSpin(p)
	*s.log = append(*s.log, fmt.Sprintf("first %+v -> %s", p, expDecision(d)))
	return d
}

func (s *recordingStrategy) Next(p strategy.PlayerState, r *buf.SpinResult) strategy.Decision {
	res, err := dto.NewSpinResultDTO(r)
	if err != nil {
		panic(err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		panic(err)
	}
	var start, after []byte
	if r.State != nil {
		start, after = r.State.StartCoreSnap, r.State.AfterCoreSnap
	}
	d := s.inner.Next(p, r)
	*s.log = append(*s.log, fmt.Sprintf("next %+v result=%s start=%s after=%s -> %s", p, expHash(raw), expHash(start), expHash(after), expDecision(d)))
	return d
}

// alternating 交替倍數 1、2，第 25 局後依餘額決定滿足或不滿足離場。
type alternating struct{}

func (alternating) NewStrategy(initial int) strategy.Strategy {
	return &alternatingStrategy{initial: initial}
}

type alternatingStrategy struct{ initial int }

func (s *alternatingStrategy) decide(p strategy.PlayerState) strategy.Decision {
	if p.Spins >= 25 {
		end := strategy.Dissatisfied
		if p.Balance >= s.initial {
			end = strategy.Satisfied
		}
		return strategy.Decision{EndType: end}
	}
	return strategy.Decision{KeepPlay: true, Bet: strategy.Bet{BetMode: 0, BetMult: 1 + p.Spins%2}}
}
func (s *alternatingStrategy) FirstSpin(p strategy.PlayerState) strategy.Decision { return s.decide(p) }
func (s *alternatingStrategy) Next(p strategy.PlayerState, _ *buf.SpinResult) strategy.Decision {
	return s.decide(p)
}

func expRecord(t *testing.T, gid spec.GID) map[string]any {
	t.Helper()
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	var log []string
	reg := strategy.NewRegistry()
	for name, f := range map[string]strategy.Factory{
		"monotone":    builtin.Monotone{BetMode: 0, BetMult: 1, MaxSpins: 60, SatisfiedRatio: 2},
		"higher_bet":  builtin.Monotone{BetMode: 0, BetMult: 2, MaxSpins: 60, SatisfiedRatio: 2},
		"alternating": alternating{},
	} {
		if err := reg.Register(gid, name, recordingFactory{f, &log}); err != nil {
			t.Fatal(err)
		}
	}
	var events []playerexp.EventConfig
	if gid == 0 {
		events = []playerexp.EventConfig{{Name: "fg", WinRange: playerexp.WinRange{Min: 0}, Tags: []string{"fg"}}, {Name: "any", WinRange: playerexp.WinRange{Min: 0}}}
	}
	group := func(name string) playerexp.GroupConfig {
		return playerexp.GroupConfig{Name: name, Player: playerexp.PlayerConfig{Players: 25, InitBet: 50, Strategy: name}, WinBuckets: []float64{0, 1, 5}, Events: events}
	}
	c := playerexp.Config{GameID: gid, Seed: playerexp.Int64Seed(77), Analysis: []playerexp.GroupConfig{group("monotone"), group("higher_bet"), group("alternating")}}
	r, err := playerexp.Run(context.Background(), c, playerexp.Options{Lab: lab, Strategies: reg, Tags: demo_tags.Catalog})
	if err != nil {
		t.Fatal(err)
	}
	report, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := playerexp.WriteCSV(t.TempDir(), r)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	csv := map[string]string{}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		csv[e.Name()] = expHash(raw)
	}
	names := make([]string, 0, len(csv))
	for n := range csv {
		names = append(names, n)
	}
	sort.Strings(names)
	logRaw, err := json.Marshal(log)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"report": json.RawMessage(report), "csv": csv, "csv_files": names, "callbacks": len(log), "callback_log": expHash(logRaw)}
}

func TestExpBaseline(t *testing.T) {
	got := map[string]any{"demo0": expRecord(t, 0), "demo1": expRecord(t, 1)}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("PROBLAB_PLAYEREXP_BASELINE") == "update" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(expBaselinePath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(expBaselinePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(want) {
		t.Fatal("playerexp baseline changed")
	}
}

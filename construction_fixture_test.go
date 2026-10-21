// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

import (
	"io/fs"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/spec"
)

// fixture 遊戲（techspec §6.3）：
//   - ext 的 Snapshot() 不檢查 isSim，永遠回傳深拷貝，並計數被呼叫次數；
//   - 每轉對 BG mode 做 4 次 YieldResult，觸發 GMR pool 擴容（poolSize = 3）；
//   - 每個 screen act 都附上 ext，讓 Snapshot 路徑被大量執行。
//
// 它只用來驗證「引擎省略 Snapshot() 呼叫」這條路徑；demo 遊戲因為自行檢查
// isSim，驗證不到它。
const (
	fixtureGameID    spec.GID = 900
	fixtureGameName           = "fixture_noguard"
	fixtureLogicKey           = spec.LogicKey("fixture_noguard")
	fixtureBaseYield          = 4
)

var fixtureSnapshotCalls atomic.Int64

var fixtureLogics = func() *slot.LogicRegistry {
	reg := slot.NewLogicRegistry()
	if err := slot.GameRegister(fixtureLogicKey, buildFixtureGame, reg); err != nil {
		panic(err)
	}
	return reg
}()

type fixtureFixed struct {
	FreeRound int    `yaml:"free_round"`
	DemoB     []int  `yaml:"demo_b"`
	DemoC     string `yaml:"demo_c"`
}

type fixtureGame struct {
	fixed       fixtureFixed
	symbolTypes []spec.SymbolType
	ext         *fixtureExt
}

type fixtureExt struct {
	Round        int   `json:"round"`
	ScatterCount int   `json:"scatters"`
	ScatterHits  []int `json:"hits"`
}

func (e *fixtureExt) Reset() {
	e.Round = 0
	e.ScatterCount = 0
	e.ScatterHits = e.ScatterHits[:0]
}

// Snapshot 刻意不檢查 isSim；計數是測試觀察用，不影響遊戲狀態與 RNG。
func (e *fixtureExt) Snapshot() any {
	fixtureSnapshotCalls.Add(1)
	hits := make([]int, len(e.ScatterHits))
	copy(hits, e.ScatterHits)
	return &fixtureExt{Round: e.Round, ScatterCount: e.ScatterCount, ScatterHits: hits}
}

func buildFixtureGame(gh *slot.Game) (slot.GameLogic, error) {
	g := &fixtureGame{ext: &fixtureExt{ScatterHits: make([]int, 0, 16)}}
	if err := spec.DecodeFixed(gh.GameSetting, &g.fixed); err != nil {
		return nil, err
	}
	g.symbolTypes = gh.GameSetting.GameModeSettings[0].SymbolSetting.SymbolTypes
	return g, nil
}

func (g *fixtureGame) GetResult(r *buf.SpinRequest, gh *slot.Game) *buf.SpinResult {
	sr := gh.StartNewSpin(r)
	triggered := false
	for i := range fixtureBaseYield {
		base := g.round(r.BetMult, gh, 0, 1, i)
		if i == 0 && base.Trigger != 0 {
			triggered = true
		}
		sr.AppendModeResult(base)
	}
	if triggered {
		sr.AppendModeResult(g.round(r.BetMult, gh, 1, g.fixed.FreeRound, 0))
	}
	sr.End()
	return sr
}

func (g *fixtureGame) round(betMult int, gh *slot.Game, modeIndex, rounds, tag int) *buf.GameModeResult {
	mode := gh.GameModeHandlerList[modeIndex]
	gmr := mode.GameModeResult
	for i := range rounds {
		g.ext.Reset()
		g.ext.Round = tag*100 + i
		screen := mode.ScreenGenerator.GenScreen()
		for idx, symbol := range screen {
			if g.symbolTypes[symbol] == spec.SymbolTypeScatter {
				g.ext.ScatterCount++
				g.ext.ScatterHits = append(g.ext.ScatterHits, idx)
			}
		}
		gmr.AddAct(buf.FinishAct, "screen", screen, g.ext)
		mode.ScreenCalculator.CalcScreen(betMult, screen, gmr)
		if gmr.GetTmpWin() > 0 {
			gmr.AddAct(buf.FinishAct, "win", nil, g.ext)
		}
		if modeIndex == 0 && g.ext.ScatterCount > 2 {
			gmr.Trigger = 1
		}
		gmr.FinishRound()
	}
	return mode.YieldResult()
}

// fixtureConfigYAML 以 demo_0 的設定為基礎，只替換遊戲身分與 logic key。
func fixtureConfigYAML(t testing.TB) []byte {
	t.Helper()
	raw, err := fs.ReadFile(demo_configs.FS, "game_0_demonormal.yaml")
	if err != nil {
		t.Fatalf("read demo config: %v", err)
	}
	text := string(raw)
	for _, pair := range [][2]string{
		{"game_id: 0", "game_id: 900"},
		{"game_name: demo_normal", "game_name: " + fixtureGameName},
		{"logic_key: demo_normal", "logic_key: " + string(fixtureLogicKey)},
	} {
		next := strings.Replace(text, pair[0], pair[1], 1)
		if next == text {
			t.Fatalf("fixture config replacement failed: %q", pair[0])
		}
		text = next
	}
	return []byte(text)
}

// newFixtureLab 建立只含 fixture 遊戲的凍結 Lab（ChaCha20、無 optimal）。
func newFixtureLab(t testing.TB) (*Problab, error) {
	t.Helper()
	f, err := core.NewChaCha20Factory(newDetReader("fixture-reseed"))
	if err != nil {
		return nil, err
	}
	cfgs := Configs(fstest.MapFS{"fixture.yaml": &fstest.MapFile{Data: fixtureConfigYAML(t)}})
	return NewAuto(f, cfgs, Logics(fixtureLogics), WithSeedEntropy(newDetReader("fixture-seed")))
}

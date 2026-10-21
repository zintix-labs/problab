// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

import (
	"encoding/json"
	"io/fs"
	"reflect"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/zintix-labs/problab/corefmt"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/spec"
)

// 遊戲收到的請求契約：每個執行入口交給遊戲的請求內容。
//
// 探測遊戲包住 demo 的真實邏輯：先記錄收到的請求內容，再交給原邏輯執行，因此
// optimal 開啟時同樣可用。

// probeCheckpoint 是測試用的 checkpoint 型別。
type probeCheckpoint struct {
	Stage int `json:"stage"`
}

var registerProbeCheckpoint sync.Once

// probeRequest 是遊戲收到的請求內容的深拷貝。
type probeRequest struct {
	UID        string
	GameName   string
	GameId     spec.GID
	Bet        int
	BetMode    int
	BetMult    int
	Cycle      int
	Choice     int
	HasChoice  bool
	HasStart   bool   // StartState 是否非 nil
	StartSnap  []byte // StartState.StartCoreSnap 的複製
	Checkpoint any    // *probeCheckpoint 時複製為值
}

type probeLogic struct {
	inner slot.GameLogic
	log   *[]probeRequest
}

func (p *probeLogic) GetResult(r *buf.SpinRequest, g *slot.Game) *buf.SpinResult {
	rec := probeRequest{
		UID: r.UID, GameName: r.GameName, GameId: r.GameId,
		Bet: r.Bet, BetMode: r.BetMode, BetMult: r.BetMult,
		Cycle: r.Cycle, Choice: r.Choice, HasChoice: r.HasChoice,
	}
	if r.StartState != nil {
		rec.HasStart = true
		if r.StartState.StartCoreSnap != nil {
			rec.StartSnap = append([]byte{}, r.StartState.StartCoreSnap...)
		}
		if cp, ok := r.StartState.Checkpoint.(*probeCheckpoint); ok {
			rec.Checkpoint = *cp
		} else {
			rec.Checkpoint = r.StartState.Checkpoint
		}
	}
	*p.log = append(*p.log, rec)
	return p.inner.GetResult(r, g)
}

// probeRegistry 以探測邏輯包住 demo 的 demo_normal 與 demo_cascade。
func probeRegistry(t testing.TB, log *[]probeRequest) *slot.LogicRegistry {
	t.Helper()
	reg := slot.NewLogicRegistry()
	for _, key := range []spec.LogicKey{"demo_normal", "demo_cascade"} {
		if err := reg.Register(key, func(g *slot.Game) (slot.GameLogic, error) {
			inner, err := demo_logic.Logics.Build(key, g)
			if err != nil {
				return nil, err
			}
			return &probeLogic{inner: inner, log: log}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	registerProbeCheckpoint.Do(func() {
		if err := dto.RegisterCheckpoint[probeCheckpoint]("demo_cascade"); err != nil {
			t.Fatal(err)
		}
	})
	return reg
}

func probeOffLab(t testing.TB, log *[]probeRequest) *Problab {
	t.Helper()
	files := fstest.MapFS{}
	for _, name := range []string{"game_0_demonormal.yaml", "game_1_democascade.yaml"} {
		raw, err := fs.ReadFile(demo_configs.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = &fstest.MapFile{Data: raw}
	}
	lab, err := NewAuto(core.Default(), Configs(files), Logics(probeRegistry(t, log)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	return lab
}

func probeManifestLab(t testing.TB, log *[]probeRequest) *Problab {
	t.Helper()
	lab, err := NewAuto(core.Default(), Configs(testManifestConfigFS(t)), Logics(probeRegistry(t, log)), WithOptimalFS(testManifestFS(t, false)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	return lab
}

// internalIdentity 是 SpinInternal 交給遊戲的 GameId／GameName。
//
// 執行核心收斂前，SpinInternal 交給遊戲的是零值；收斂後改為機台本身的值。收斂前後
// 本測試只允許修改這一個函式。
func internalIdentity(m *Machine) (spec.GID, string) { return 0, "" }

func probeLast(t testing.TB, log []probeRequest) probeRequest {
	t.Helper()
	if len(log) == 0 {
		t.Fatal("game was not called")
	}
	return log[len(log)-1]
}

func requireProbe(t testing.TB, step string, got, want probeRequest) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: game received\n got=%+v\nwant=%+v", step, got, want)
	}
}

func TestGameReceivesRequestPerEntry(t *testing.T) {
	var log []probeRequest
	lab := probeOffLab(t, &log)
	m, err := lab.NewMachine(1, MachineConfig{Seed: SeedInt64(42)})
	if err != nil {
		t.Fatal(err)
	}
	unit := m.BetUnits[0]
	internalID, internalName := internalIdentity(m)

	// 1. Spin 不帶快照：遊戲收到 DTO 的值，StartState 為 nil。
	plain := &dto.SpinRequest{UID: "u1", GameName: m.gameName, GameId: m.gameId, Bet: unit, BetMode: 0, BetMult: 1}
	first, err := m.Spin(plain)
	if err != nil {
		t.Fatal(err)
	}
	requireProbe(t, "spin", probeLast(t, log), probeRequest{
		UID: "u1", GameName: m.gameName, GameId: m.gameId, Bet: unit, BetMode: 0, BetMult: 1,
	})

	// 2. Spin 帶外部快照與 checkpoint，並帶選擇欄位：全部原樣交給遊戲。
	snap, err := corefmt.DecodeBase64URL(first.State.StartCoreSnapB64U)
	if err != nil {
		t.Fatal(err)
	}
	replay := &dto.SpinRequest{
		UID: "u2", GameName: m.gameName, GameId: m.gameId, Bet: unit * 2, BetMode: 0, BetMult: 2,
		Cycle: 2, Choice: 1, HasChoice: true,
		StartState: &dto.StartState{StartCoreSnapB64U: first.State.StartCoreSnapB64U, Checkpoint: json.RawMessage(`{"stage":3}`)},
	}
	if _, err := m.Spin(replay); err != nil {
		t.Fatal(err)
	}
	requireProbe(t, "spin-replay", probeLast(t, log), probeRequest{
		UID: "u2", GameName: m.gameName, GameId: m.gameId, Bet: unit * 2, BetMode: 0, BetMult: 2,
		Cycle: 2, Choice: 1, HasChoice: true,
		HasStart: true, StartSnap: snap, Checkpoint: probeCheckpoint{Stage: 3},
	})

	// 3. 緊接 SpinInternal：不帶上一段的任何殘留。
	m.SpinInternal(0)
	requireProbe(t, "internal-after-replay", probeLast(t, log), probeRequest{
		GameName: internalName, GameId: internalID, Bet: unit, BetMode: 0, BetMult: 1,
	})

	// 4. 再帶外部快照與 checkpoint 的 Spin，緊接 SpinOffline：同樣不帶殘留。
	if _, err := m.Spin(replay); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SpinOffline(0, 3); err != nil {
		t.Fatal(err)
	}
	requireProbe(t, "offline-after-replay", probeLast(t, log), probeRequest{
		GameName: m.gameName, GameId: m.gameId, Bet: unit * 3, BetMode: 0, BetMult: 3,
	})

	// 5. SpinInternal 與 SpinOffline 交錯後再 Spin：Spin 不帶離線入口的殘留。
	m.SpinInternal(0)
	if _, err := m.Spin(plain); err != nil {
		t.Fatal(err)
	}
	requireProbe(t, "spin-after-internal", probeLast(t, log), probeRequest{
		UID: "u1", GameName: m.gameName, GameId: m.gameId, Bet: unit, BetMode: 0, BetMult: 1,
	})
	if len(log) != 7 {
		t.Fatalf("game calls=%d, want 7", len(log))
	}
}

func TestGameReceivesOptimalStartPerEntry(t *testing.T) {
	var log []probeRequest
	lab := probeManifestLab(t, &log)
	m, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(42)})
	if err != nil {
		t.Fatal(err)
	}
	unit := m.BetUnits[0]
	internalID, internalName := internalIdentity(m)

	// Spin 抽庫：抽出的 seed 填進 StartState 交給遊戲，checkpoint 為 nil。
	res, err := m.Spin(&dto.SpinRequest{UID: "u1", GameName: m.gameName, GameId: m.gameId, Bet: unit, BetMode: 0, BetMult: 1})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := corefmt.DecodeBase64URL(res.State.StartCoreSnapB64U)
	if err != nil {
		t.Fatal(err)
	}
	got := probeLast(t, log)
	requireProbe(t, "spin-optimal", got, probeRequest{
		UID: "u1", GameName: m.gameName, GameId: m.gameId, Bet: unit, BetMode: 0, BetMult: 1,
		HasStart: true, StartSnap: seed,
	})

	// SpinInternal 與 SpinOffline 抽庫：不把 seed 交給遊戲。
	m.SpinInternal(0)
	requireProbe(t, "internal-optimal", probeLast(t, log), probeRequest{
		GameName: internalName, GameId: internalID, Bet: unit, BetMode: 0, BetMult: 1,
	})
	sr, err := m.SpinOffline(0, 2)
	if err != nil {
		t.Fatal(err)
	}
	requireProbe(t, "offline-optimal", probeLast(t, log), probeRequest{
		GameName: m.gameName, GameId: m.gameId, Bet: unit * 2, BetMode: 0, BetMult: 2,
	})
	// SpinOffline 的結果仍記錄抽出的 seed 作為起始快照（不經由請求交給遊戲）；
	// 值由 E0 逐位元保護，這裡只確認存在。
	if len(sr.State.StartCoreSnap) == 0 {
		t.Fatal("offline optimal start snapshot missing")
	}
}

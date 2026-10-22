// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/zintix-labs/problab/corefmt"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
)

// 故障注入矩陣。案例編號（S1、F2～F16）只用來互相對照。「恢復點」指起點機制在第一次 Restore 起點之前記錄的狀態；抽庫時即
// 抽庫後的選取狀態。

// ---------------------------------------------------------------------------
// 故障注入工具
// ---------------------------------------------------------------------------

// restoreFault 描述第 n 次 Restore 的故障。
type restoreFault struct {
	err     error // 回傳的錯誤
	panicV  any   // 非 nil 時直接 panic 這個值
	partial bool  // 失敗前先改寫狀態
}

// faultPRNG 依呼叫次數注入 Snapshot 與 Restore 的故障。
type faultPRNG struct {
	core.PRNG
	snapshots, restores int
	snapshotFailAt      int
	snapshotErr         error
	restoreFaults       map[int]restoreFault
}

func (p *faultPRNG) Snapshot() ([]byte, error) {
	p.snapshots++
	if p.snapshots == p.snapshotFailAt {
		return nil, p.snapshotErr
	}
	return p.PRNG.Snapshot()
}

func (p *faultPRNG) Restore(b []byte) error {
	p.restores++
	f, ok := p.restoreFaults[p.restores]
	if !ok {
		return p.PRNG.Restore(b)
	}
	if f.partial {
		p.PRNG.Uint64() // 部分改寫：推進串流
	}
	if f.panicV != nil {
		panic(f.panicV)
	}
	return f.err
}

func injectFaults(m *Machine) *faultPRNG {
	p := &faultPRNG{PRNG: m.core.PRNG, restoreFaults: map[int]restoreFault{}}
	m.core.PRNG = p
	return p
}

// countingLogic 包住遊戲邏輯，計數被呼叫次數，並可改為 panic 或回傳 nil。
type countingLogic struct {
	inner  slot.GameLogic
	calls  int
	panicV any
	nilRes bool
}

func (c *countingLogic) GetResult(r *buf.SpinRequest, g *slot.Game) *buf.SpinResult {
	c.calls++
	if c.panicV != nil {
		panic(c.panicV)
	}
	if c.nilRes {
		return nil
	}
	return c.inner.GetResult(r, g)
}

// withCountingLogic 以 countingLogic 包住機台目前的遊戲邏輯。
func withCountingLogic(t testing.TB, m *Machine) *countingLogic {
	t.Helper()
	key := m.gh.GameSetting.LogicKey
	logic := &countingLogic{}
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

// faultLab 建立 demo_0 的 Lab：optimal 為 true 時使用 manifest artifact。
func faultLab(t testing.TB, factory core.PRNGFactory, optimal bool, opts ...ProblabOption) *Problab {
	t.Helper()
	cfg := Configs(demo_configs.FS)
	if optimal {
		cfg = Configs(testManifestConfigFS(t))
		opts = append(opts, WithOptimalFS(testManifestFS(t, false)))
	}
	lab, err := NewAuto(factory, cfg, Logics(demo_logic.Logics), opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	return lab
}

func faultMachine(t testing.TB, optimal bool) *Machine {
	t.Helper()
	m, err := faultLab(t, core.Default(), optimal).NewMachine(0, MachineConfig{Seed: SeedInt64(77)})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// pickedRecoveryPoint 以相同設定的對照機台重現抽庫，回傳抽庫後的選取狀態。
func pickedRecoveryPoint(t testing.TB) []byte {
	t.Helper()
	control := faultMachine(t, true)
	if _, err := control.optimal.PickSeed(0, control.core); err != nil {
		t.Fatal(err)
	}
	return coreState(t, control)
}

func coreState(t testing.TB, m *Machine) []byte {
	t.Helper()
	snap, err := m.SnapshotCore()
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Clone(snap)
}

func catchPanic(f func()) (p any) {
	defer func() { p = recover() }()
	f()
	return nil
}

func spinReq(m *Machine) *dto.SpinRequest {
	return &dto.SpinRequest{GameName: m.gameName, GameId: m.gameId, Bet: m.BetUnits[0], BetMode: 0, BetMult: 1}
}

// entry 是一個執行入口。
type entry struct {
	name string
	call func(m *Machine) error
}

var seedEntries = []entry{
	{"Spin", func(m *Machine) error { _, err := m.Spin(spinReq(m)); return err }},
	{"SpinUnsafe", func(m *Machine) error { m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1}); return nil }},
	{"SpinUnsafe/capture", func(m *Machine) error {
		m.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1, CaptureSnapshots: true})
		return nil
	}},
}

// unsafeInput 回傳 SpinUnsafe 入口使用的輸入；其他入口回傳 false。
func unsafeInput(name string) (SpinInput, bool) {
	switch name {
	case "SpinUnsafe":
		return SpinInput{BetMode: 0, BetMult: 1}, true
	case "SpinUnsafe/capture":
		return SpinInput{BetMode: 0, BetMult: 1, CaptureSnapshots: true}, true
	}
	return SpinInput{}, false
}

// externalEntry 以外部快照呼叫 Spin；快照取自同一台機台建立時的狀態。
func externalSpin(m *Machine, snap []byte) error {
	r := spinReq(m)
	r.StartState = &dto.StartState{StartCoreSnapB64U: corefmt.EncodeBase64URL(snap)}
	_, err := m.Spin(r)
	return err
}

func requireIs(t testing.TB, p any, targets ...error) {
	t.Helper()
	err, ok := p.(error)
	if !ok {
		t.Fatalf("panic value %v (%T) is not an error", p, p)
	}
	for _, target := range targets {
		if !errors.Is(err, target) {
			t.Fatalf("panic %q does not contain %q", err, target)
		}
	}
}

func requireCore(t testing.TB, m *Machine, want []byte) {
	t.Helper()
	if got := coreState(t, m); !bytes.Equal(got, want) {
		t.Fatal("machine RNG is not at the expected state")
	}
}

// requireFaulted 確認 RNG 恢復失敗後機台標記為失效，且 Spin 立即 panic、不呼叫遊戲。
// SpinUnsafe 不檢查失效狀態：發生內部執行 panic 後由呼叫端棄用機台。
func requireFaulted(t testing.TB, m *Machine, logic *countingLogic) {
	t.Helper()
	if !m.faulted {
		t.Fatal("machine was not marked faulted")
	}
	before := logic.calls
	for _, e := range seedEntries {
		if _, isUnsafe := unsafeInput(e.name); isUnsafe {
			continue
		}
		p := catchPanic(func() { _ = e.call(m) })
		if !errors.Is(asError(p), errMachineFaulted) {
			t.Fatalf("%s on a faulted machine: panic=%v", e.name, p)
		}
	}
	if logic.calls != before {
		t.Fatal("faulted machine called the game")
	}
}

// ---------------------------------------------------------------------------
// S1：成功路徑，PRNG 重用自己的 snapshot 緩衝區
// ---------------------------------------------------------------------------

func TestScratchSnapshotPRNGMatchesPlainResults(t *testing.T) {
	for _, optimal := range []bool{false, true} {
		t.Run(fmt.Sprint("optimal=", optimal), func(t *testing.T) {
			plain, scratch := faultMachine(t, optimal), faultMachine(t, optimal)
			p := &offlineScratchPRNG{PRNG: scratch.core.PRNG}
			scratch.core.PRNG = p
			for i := range 6 {
				var a, b []byte
				if i%2 == 0 {
					ra, err := plain.Spin(spinReq(plain))
					if err != nil {
						t.Fatal(err)
					}
					rb, err := scratch.Spin(spinReq(scratch))
					if err != nil {
						t.Fatal(err)
					}
					a, _ = json.Marshal(ra)
					b, _ = json.Marshal(rb)
				} else {
					ra := plain.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1, CaptureSnapshots: true})
					rb := scratch.SpinUnsafe(SpinInput{BetMode: 0, BetMult: 1, CaptureSnapshots: true})
					// 改寫 PRNG 的暫存緩衝區，不得影響已回傳的快照。
					for j := range p.scratch {
						p.scratch[j] ^= 0xff
					}
					da, _ := dto.NewSpinResultDTO(ra)
					db, _ := dto.NewSpinResultDTO(rb)
					a, _ = json.Marshal(da)
					b, _ = json.Marshal(db)
				}
				if !bytes.Equal(a, b) {
					t.Fatalf("call %d: result differs with a buffer-reusing PRNG", i)
				}
				if !bytes.Equal(coreState(t, plain), coreState(t, scratch)) {
					t.Fatalf("call %d: machine RNG differs with a buffer-reusing PRNG", i)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// F2：抽庫失敗
// ---------------------------------------------------------------------------

func TestPickFailurePanicsWithoutConsumingRNG(t *testing.T) {
	for _, e := range seedEntries {
		t.Run(e.name, func(t *testing.T) {
			lab := faultLab(t, core.Default(), true)
			m, err := lab.NewMachine(0, MachineConfig{Seed: SeedInt64(77)})
			if err != nil {
				t.Fatal(err)
			}
			if err := lab.Close(); err != nil {
				t.Fatal(err)
			}
			before := coreState(t, m)
			if p := catchPanic(func() { _ = e.call(m) }); p == nil || !strings.Contains(fmt.Sprint(p), "pick optimal seed") {
				t.Fatalf("panic=%v", p)
			}
			requireCore(t, m, before)
		})
	}
	t.Run("production Spin compares before and after the pick", func(t *testing.T) {
		build := func() *Machine {
			f, err := core.NewChaCha20Factory(newDetReader("f2-reseed"))
			if err != nil {
				t.Fatal(err)
			}
			lab := faultLab(t, f, true, WithSeedEntropy(newDetReader("f2-seed")))
			m, err := newProductionMachineForTest(lab, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := lab.Close(); err != nil {
				t.Fatal(err)
			}
			return m
		}
		control, m := build(), build()
		if err := control.core.Reseed(); err != nil {
			t.Fatal(err)
		}
		afterReseed := coreState(t, control)
		if p := catchPanic(func() { _, _ = m.Spin(spinReq(m)) }); p == nil {
			t.Fatal("pick failure did not panic")
		}
		requireCore(t, m, afterReseed)
	})
}

// ---------------------------------------------------------------------------
// F3～F6b：起點準備
// ---------------------------------------------------------------------------

func TestStartPreparationFaults(t *testing.T) {
	startErr := errors.New("injected start restore failure")
	recoverErr := errors.New("injected recovery restore failure")
	snapErr := errors.New("injected snapshot failure")

	for _, e := range seedEntries {
		t.Run(e.name, func(t *testing.T) {
			recovery := pickedRecoveryPoint(t)

			t.Run("F3 recovery point snapshot fails", func(t *testing.T) {
				m := faultMachine(t, true)
				logic := withCountingLogic(t, m)
				p := injectFaults(m)
				p.snapshotFailAt, p.snapshotErr = 1, snapErr
				requireIs(t, catchPanic(func() { _ = e.call(m) }), snapErr)
				if m.faulted || logic.calls != 0 {
					t.Fatal("snapshot failure must not fault the machine or run the game")
				}
			})
			for _, partial := range []bool{false, true} {
				t.Run(fmt.Sprintf("F4/F5 seed restore fails partial=%t", partial), func(t *testing.T) {
					m := faultMachine(t, true)
					logic := withCountingLogic(t, m)
					p := injectFaults(m)
					p.restoreFaults[1] = restoreFault{err: startErr, partial: partial}
					requireIs(t, catchPanic(func() { _ = e.call(m) }), startErr)
					requireCore(t, m, recovery)
					if m.faulted || logic.calls != 0 {
						t.Fatal("recovered seed restore failure must not fault the machine or run the game")
					}
				})
			}
			for _, mode := range []string{"error", "panic"} {
				t.Run("F6 seed restore fails and recovery "+mode, func(t *testing.T) {
					m := faultMachine(t, true)
					logic := withCountingLogic(t, m)
					p := injectFaults(m)
					p.restoreFaults[1] = restoreFault{err: startErr, partial: true}
					p.restoreFaults[2] = recoveryFault(mode, recoverErr)
					requireIs(t, catchPanic(func() { _ = e.call(m) }), startErr, recoverErr)
					requireFaulted(t, m, logic)
				})
			}
			for _, v := range []any{"string start panic", errors.New("error start panic")} {
				t.Run(fmt.Sprintf("F6a seed restore panics %T", v), func(t *testing.T) {
					m := faultMachine(t, true)
					p := injectFaults(m)
					p.restoreFaults[1] = restoreFault{panicV: v, partial: true}
					if got := catchPanic(func() { _ = e.call(m) }); got != v {
						t.Fatalf("panic value changed: %v (%T)", got, got)
					}
					requireCore(t, m, recovery)
					if m.faulted {
						t.Fatal("recovered start panic must not fault the machine")
					}
				})
			}
			for _, mode := range []string{"error", "panic"} {
				t.Run("F6b seed restore panics and recovery "+mode, func(t *testing.T) {
					m := faultMachine(t, true)
					logic := withCountingLogic(t, m)
					p := injectFaults(m)
					p.restoreFaults[1] = restoreFault{panicV: startErr, partial: true}
					p.restoreFaults[2] = recoveryFault(mode, recoverErr)
					requireIs(t, catchPanic(func() { _ = e.call(m) }), startErr, recoverErr)
					requireFaulted(t, m, logic)
				})
			}
		})
	}
}

func recoveryFault(mode string, err error) restoreFault {
	if mode == "panic" {
		return restoreFault{panicV: err}
	}
	return restoreFault{err: err}
}

// ---------------------------------------------------------------------------
// F6a、F6b、F7、F8：外部快照（只有 Spin）
// ---------------------------------------------------------------------------

func TestExternalSnapshotFaults(t *testing.T) {
	startErr := errors.New("injected external restore failure")
	recoverErr := errors.New("injected recovery restore failure")

	t.Run("F7 wrong length is rejected", func(t *testing.T) {
		m := faultMachine(t, false)
		logic := withCountingLogic(t, m)
		before := coreState(t, m)
		err := externalSpin(m, []byte{1, 2, 3})
		if e, ok := errs.AsErr(err); !ok || e.ErrLv != errs.Warn || !strings.Contains(err.Error(), "restore core err ") {
			t.Fatalf("err=%v", err)
		}
		requireCore(t, m, before)
		if logic.calls != 0 || m.faulted {
			t.Fatal("rejected snapshot ran the game or faulted the machine")
		}
	})
	t.Run("F7 partial write then rejected", func(t *testing.T) {
		m := faultMachine(t, false)
		logic := withCountingLogic(t, m)
		before := coreState(t, m)
		p := injectFaults(m)
		p.restoreFaults[1] = restoreFault{err: startErr, partial: true}
		err := externalSpin(m, before)
		if e, ok := errs.AsErr(err); !ok || e.ErrLv != errs.Warn || !strings.Contains(err.Error(), startErr.Error()) {
			t.Fatalf("err=%v", err)
		}
		requireCore(t, m, before)
		if logic.calls != 0 || m.faulted {
			t.Fatal("rejected snapshot ran the game or faulted the machine")
		}
	})
	for _, mode := range []string{"error", "panic"} {
		t.Run("F8 rejected and recovery "+mode, func(t *testing.T) {
			m := faultMachine(t, false)
			logic := withCountingLogic(t, m)
			before := coreState(t, m)
			p := injectFaults(m)
			p.restoreFaults[1] = restoreFault{err: startErr, partial: true}
			p.restoreFaults[2] = recoveryFault(mode, recoverErr)
			requireIs(t, catchPanic(func() { _ = externalSpin(m, before) }), startErr, recoverErr)
			requireFaulted(t, m, logic)
		})
	}
	for _, v := range []any{"string external panic", errors.New("error external panic")} {
		t.Run(fmt.Sprintf("F6a external restore panics %T", v), func(t *testing.T) {
			m := faultMachine(t, false)
			before := coreState(t, m)
			p := injectFaults(m)
			p.restoreFaults[1] = restoreFault{panicV: v, partial: true}
			if got := catchPanic(func() { _ = externalSpin(m, before) }); got != v {
				t.Fatalf("panic value changed (or became Warn): %v (%T)", got, got)
			}
			requireCore(t, m, before)
		})
	}
	for _, mode := range []string{"error", "panic"} {
		t.Run("F6b external restore panics and recovery "+mode, func(t *testing.T) {
			m := faultMachine(t, false)
			logic := withCountingLogic(t, m)
			before := coreState(t, m)
			p := injectFaults(m)
			p.restoreFaults[1] = restoreFault{panicV: startErr, partial: true}
			p.restoreFaults[2] = recoveryFault(mode, recoverErr)
			requireIs(t, catchPanic(func() { _ = externalSpin(m, before) }), startErr, recoverErr)
			requireFaulted(t, m, logic)
		})
	}
}

// ---------------------------------------------------------------------------
// F9～F14：執行與收尾
// ---------------------------------------------------------------------------

func TestExecutionAndFinishFaults(t *testing.T) {
	recoverErr := errors.New("injected recovery restore failure")
	snapErr := errors.New("injected after snapshot failure")

	for _, e := range seedEntries {
		t.Run(e.name, func(t *testing.T) {
			recovery := pickedRecoveryPoint(t)
			for _, v := range []any{"string game panic", errors.New("error game panic")} {
				t.Run(fmt.Sprintf("F9 game panic %T", v), func(t *testing.T) {
					m := faultMachine(t, true)
					withCountingLogic(t, m).panicV = v
					if got := catchPanic(func() { _ = e.call(m) }); got != v {
						t.Fatalf("panic value changed: %v (%T)", got, got)
					}
					requireCore(t, m, recovery)
					if m.faulted {
						t.Fatal("recovered game panic must not fault the machine")
					}
				})
			}
			for _, mode := range []string{"error", "panic"} {
				t.Run("F10 game panic and recovery "+mode, func(t *testing.T) {
					m := faultMachine(t, true)
					logic := withCountingLogic(t, m)
					gameErr := errors.New("game fault")
					logic.panicV = gameErr
					p := injectFaults(m)
					p.restoreFaults[2] = recoveryFault(mode, recoverErr)
					requireIs(t, catchPanic(func() { _ = e.call(m) }), gameErr, recoverErr)
					logic.panicV = nil
					requireFaulted(t, m, logic)
				})
				t.Run("F12 recovery after a normal run "+mode, func(t *testing.T) {
					m := faultMachine(t, true)
					logic := withCountingLogic(t, m)
					p := injectFaults(m)
					p.restoreFaults[2] = recoveryFault(mode, recoverErr)
					requireIs(t, catchPanic(func() { _ = e.call(m) }), recoverErr)
					requireFaulted(t, m, logic)
				})
			}
			if e.name != "SpinUnsafe" { // 不擷取快照的 SpinUnsafe 沒有結束快照
				t.Run("F11 after snapshot fails", func(t *testing.T) {
					m := faultMachine(t, true)
					p := injectFaults(m)
					p.snapshotFailAt, p.snapshotErr = 2, snapErr // 1：恢復點；2：結束快照
					requireIs(t, catchPanic(func() { _ = e.call(m) }), snapErr)
					requireCore(t, m, recovery)
				})
			}
			t.Run("F13 game returns nil", func(t *testing.T) {
				m := faultMachine(t, true)
				withCountingLogic(t, m).nilRes = true
				var got *buf.SpinResult
				in, isUnsafe := unsafeInput(e.name)
				p := catchPanic(func() {
					if isUnsafe {
						got = m.SpinUnsafe(in)
						return
					}
					_ = e.call(m)
				})
				if isUnsafe {
					// SpinUnsafe 原樣回傳 nil，兩種快照模式都不觸碰 State。
					if p != nil || got != nil {
						t.Fatalf("%s: panic=%v result=%v, want nil result without panic", e.name, p, got)
					}
					if fs := m.fallbackState; fs.StartCoreSnap != nil || fs.AfterCoreSnap != nil || fs.Checkpoint != nil {
						t.Fatalf("%s: nil result wrote the fallback State", e.name)
					}
				} else if p == nil {
					t.Fatal("nil game result did not panic")
				}
				requireCore(t, m, recovery)
			})
		})
	}
	for _, e := range seedEntries {
		t.Run("F14 game panic without start "+e.name, func(t *testing.T) {
			m := faultMachine(t, false)
			v := "string game panic"
			withCountingLogic(t, m).panicV = v
			if got := catchPanic(func() { _ = e.call(m) }); got != v {
				t.Fatalf("panic value changed: %v (%T)", got, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// F15：可預期的輸入錯誤
// ---------------------------------------------------------------------------

func TestExpectedInputErrorsDoNotRunTheGame(t *testing.T) {
	m := faultMachine(t, true)
	logic := withCountingLogic(t, m)
	before := coreState(t, m)
	bad := spinReq(m)
	bad.GameId++
	if _, err := m.Spin(bad); err == nil {
		t.Fatal("Spin accepted a wrong game id")
	}
	requireCore(t, m, before)
	if logic.calls != 0 {
		t.Fatal("input error ran the game")
	}
}

// ---------------------------------------------------------------------------
// MachinePool
// ---------------------------------------------------------------------------

func TestPoolCountsExecutionFaults(t *testing.T) {
	ctx := context.Background()
	t.Run("F1 reseed failure stays Fatal", func(t *testing.T) {
		factory, err := core.NewChaCha20Factory(errorReader{})
		if err != nil {
			t.Fatal(err)
		}
		lab := faultLab(t, factory, false, WithSeedEntropy(newDetReader("pool-seed")))
		rt, err := lab.BuildRuntime(1)
		if err != nil {
			t.Fatal(err)
		}
		defer rt.Close()
		pool := rt.pools[0]
		_, err = rt.Spin(ctx, spinReq(<-peek(pool)))
		if !isFatalErr(err) || !strings.Contains(err.Error(), "reseed PRNG") {
			t.Fatalf("err=%v", err)
		}
		if pool.fatals.Load() != 1 || pool.panics.Load() != 0 || pool.rebuild.Load() != 1 {
			t.Fatalf("fatals=%d panics=%d rebuild=%d", pool.fatals.Load(), pool.panics.Load(), pool.rebuild.Load())
		}
	})
	t.Run("F4 seed restore failure panics and replaces the machine", func(t *testing.T) {
		lab := faultLab(t, core.Default(), true, WithSeedEntropy(newDetReader("pool-seed")))
		rt, err := lab.BuildRuntime(1)
		if err != nil {
			t.Fatal(err)
		}
		defer rt.Close()
		pool := rt.pools[0]
		m := <-pool.pool
		p := injectFaults(m)
		p.restoreFaults[1] = restoreFault{err: errors.New("injected seed restore failure")}
		pool.pool <- m
		_, err = rt.Spin(ctx, spinReq(m))
		if !isFatalErr(err) || !strings.Contains(err.Error(), "panic") {
			t.Fatalf("err=%v", err)
		}
		if pool.panics.Load() != 1 || pool.fatals.Load() != 0 || pool.rebuild.Load() != 1 {
			t.Fatalf("fatals=%d panics=%d rebuild=%d", pool.fatals.Load(), pool.panics.Load(), pool.rebuild.Load())
		}
	})
	t.Run("F7 rejected external snapshot keeps the machine", func(t *testing.T) {
		lab := faultLab(t, core.Default(), false, WithSeedEntropy(newDetReader("pool-seed")))
		rt, err := lab.BuildRuntime(1)
		if err != nil {
			t.Fatal(err)
		}
		defer rt.Close()
		pool := rt.pools[0]
		m := <-peek(pool)
		r := spinReq(m)
		r.StartState = &dto.StartState{StartCoreSnapB64U: corefmt.EncodeBase64URL([]byte{1, 2, 3})}
		_, err = rt.Spin(ctx, r)
		if e, ok := errs.AsErr(err); !ok || e.ErrLv != errs.Warn {
			t.Fatalf("err=%v", err)
		}
		if pool.panics.Load() != 0 || pool.fatals.Load() != 0 || pool.rebuild.Load() != 0 {
			t.Fatalf("fatals=%d panics=%d rebuild=%d", pool.fatals.Load(), pool.panics.Load(), pool.rebuild.Load())
		}
	})
}

// peek 取出池中的機台後立即放回，用來讀取機台的識別資訊。
func peek(p *MachinePool) <-chan *Machine {
	m := <-p.pool
	p.pool <- m
	out := make(chan *Machine, 1)
	out <- m
	return out
}

// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/zintix-labs/problab/corefmt"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/spec"
)

func TestProductionMachineReseedsOnlyForNewCycles(t *testing.T) {
	reseedEntropy := bytes.NewReader(bytes.Repeat([]byte{0x3c}, 32*4))
	base, err := core.NewChaCha20Factory(reseedEntropy)
	if err != nil {
		t.Fatal(err)
	}
	factory := &lifecycleFactory{base: base}
	lab := newOneGameLab(t, factory, WithSeedEntropy(bytes.NewReader(make([]byte, 32*8))))
	defer func() { _ = lab.Close() }()

	machine, err := lab.NewMachine(1, false)
	if err != nil {
		t.Fatal(err)
	}
	newCalls := factory.newCalls.Load()
	generateCalls := factory.generateCalls.Load()
	first, err := machine.Spin(testSpinRequest())
	if err != nil {
		t.Fatalf("first production Spin: %v", err)
	}
	if factory.reseedCalls.Load() != 1 {
		t.Fatalf("reseed calls=%d, want 1", factory.reseedCalls.Load())
	}
	if factory.newCalls.Load() != newCalls || factory.generateCalls.Load() != generateCalls {
		t.Fatal("Spin created a new PRNG or generated a root seed")
	}

	replayRequest := testSpinRequest()
	replayRequest.StartState = &dto.StartState{StartCoreSnapB64U: first.State.StartCoreSnapB64U}
	replayed, err := machine.Spin(replayRequest)
	if err != nil {
		t.Fatalf("replay Spin: %v", err)
	}
	if factory.reseedCalls.Load() != 1 {
		t.Fatal("replay path called Reseed")
	}
	firstJSON, _ := json.Marshal(first)
	replayedJSON, _ := json.Marshal(replayed)
	if !bytes.Equal(firstJSON, replayedJSON) {
		t.Fatal("replay from post-reseed start snapshot was not bit-exact")
	}

	second, err := machine.Spin(testSpinRequest())
	if err != nil {
		t.Fatalf("second production Spin: %v", err)
	}
	if factory.reseedCalls.Load() != 2 {
		t.Fatalf("reseed calls=%d, want 2", factory.reseedCalls.Load())
	}
	if first.State.StartCoreSnapB64U == second.State.StartCoreSnapB64U {
		t.Fatal("successive production cycles exposed the same post-reseed start state")
	}
}

func TestOptimalProductionReseedsBeforeSelectorAndReturnsArtifactLineage(t *testing.T) {
	base, _ := core.NewChaCha20Factory(bytes.NewReader(bytes.Repeat([]byte{0x6d}, 64)))
	factory := &lifecycleFactory{base: base}
	artifactFS := testManifestFS(t, false)
	lab, err := NewAuto(
		factory,
		Configs(testManifestConfigFS(t)),
		Logics(demo_logic.Logics),
		WithOptimalFS(artifactFS),
		WithSeedEntropy(bytes.NewReader(make([]byte, 64))),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	machine, err := lab.NewMachine(0, false)
	if err != nil {
		t.Fatal(err)
	}
	factory.clearEvents()
	result, err := machine.Spin(&dto.SpinRequest{
		UID: "optimal", GameName: "demo_normal", GameId: 0,
		Bet: 40, BetMode: 0, BetMult: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := factory.recordedEvents()
	if len(events) < 2 || events[0] != "reseed" || events[1] != "intn" {
		t.Fatalf("RNG event order=%v, want reseed before selector draw", events)
	}
	start, err := corefmt.DecodeBase64URL(result.State.StartCoreSnapB64U)
	if err != nil {
		t.Fatal(err)
	}
	bank, err := fs.ReadFile(artifactFS, "game0/seed_bank.bin")
	if err != nil {
		t.Fatal(err)
	}
	if len(start) == 0 || len(bank)%len(start) != 0 {
		t.Fatalf("invalid start/bank dimensions: %d/%d", len(start), len(bank))
	}
	found := false
	for offset := 0; offset < len(bank); offset += len(start) {
		if bytes.Equal(start, bank[offset:offset+len(start)]) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Optimal response start snapshot was not selected from Artifact lineage")
	}
}

func TestExplicitSeedAndSimulationPathsRemainDeterministic(t *testing.T) {
	base, _ := core.NewChaCha20Factory(bytes.NewReader(bytes.Repeat([]byte{0x5a}, 32*4)))
	factory := &lifecycleFactory{base: base}
	lab := newOneGameLab(t, factory, WithSeedEntropy(bytes.NewReader(make([]byte, 32*4))))
	defer func() { _ = lab.Close() }()

	explicit, err := lab.NewMachineWithSeedString(1, "fixed-seed", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := explicit.Spin(testSpinRequest()); err != nil {
		t.Fatal(err)
	}
	explicit.SpinInternal(0)
	if factory.reseedCalls.Load() != 0 {
		t.Fatal("explicit seeded or SpinInternal path called Reseed")
	}

	simulated, err := lab.NewMachine(1, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := simulated.Spin(testSpinRequest()); err != nil {
		t.Fatal(err)
	}
	if factory.reseedCalls.Load() != 0 {
		t.Fatal("simulation Machine called Reseed")
	}

	a, err := lab.NewMachineWithSeedBytes(1, []byte("same"), false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := lab.NewMachineWithSeedString(1, "same", false)
	if err != nil {
		t.Fatal(err)
	}
	aSnapshot, _ := a.SnapshotCore()
	bSnapshot, _ := b.SnapshotCore()
	if !bytes.Equal(aSnapshot, bSnapshot) {
		t.Fatal("string seed was not equivalent to its unmodified UTF-8 bytes")
	}
	bytesCore, err := lab.NewCoreWithSeedBytes([]byte("core-seed"))
	if err != nil {
		t.Fatal(err)
	}
	stringCore, err := lab.NewCoreWithSeedString("core-seed")
	if err != nil {
		t.Fatal(err)
	}
	bytesCoreSnapshot, _ := bytesCore.Snapshot()
	stringCoreSnapshot, _ := stringCore.Snapshot()
	if !bytes.Equal(bytesCoreSnapshot, stringCoreSnapshot) {
		t.Fatal("NewCoreWithSeedString changed UTF-8 seed bytes")
	}
}

func TestProductionReseedFailureIsFatalAndStateAtomic(t *testing.T) {
	factory, _ := core.NewChaCha20Factory(errorReader{})
	lab := newOneGameLab(t, factory, WithSeedEntropy(bytes.NewReader(make([]byte, 64))))
	defer func() { _ = lab.Close() }()
	machine, err := lab.NewMachine(1, false)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := machine.SnapshotCore()
	if _, err := machine.Spin(testSpinRequest()); err == nil || !isFatalErr(err) {
		t.Fatalf("Spin error=%v, want fatal reseed failure", err)
	}
	after, _ := machine.SnapshotCore()
	if !bytes.Equal(before, after) {
		t.Fatal("failed production Reseed changed Machine state")
	}
}

func TestPCG64ProductionLifecycleCallsCompatibilityNoOp(t *testing.T) {
	factory := &lifecycleFactory{base: core.PCG64()}
	lab := newOneGameLab(t, factory, WithSeedEntropy(bytes.NewReader(make([]byte, 64))))
	defer func() { _ = lab.Close() }()
	machine, err := lab.NewMachine(1, false)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := machine.SnapshotCore()
	result, err := machine.Spin(testSpinRequest())
	if err != nil {
		t.Fatal(err)
	}
	if factory.reseedCalls.Load() != 1 {
		t.Fatalf("PCG64 lifecycle Reseed calls=%d, want 1", factory.reseedCalls.Load())
	}
	start, err := corefmt.DecodeBase64URL(result.State.StartCoreSnapB64U)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, start) {
		t.Fatal("PCG64 compatibility Reseed changed the continuous start state")
	}
}

func TestPCG64RetainedPublicInt64APIUsesCanonicalEncoding(t *testing.T) {
	lab := newOneGameLab(t, core.PCG64(), WithSeedEntropy(bytes.NewReader(make([]byte, 64))))
	defer func() { _ = lab.Close() }()
	fromInt, err := lab.NewCore(-1)
	if err != nil {
		t.Fatal(err)
	}
	fromBytes, err := lab.NewCoreWithSeedBytes(core.EncodeInt64Seed(-1))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := fromInt.Snapshot()
	b, _ := fromBytes.Snapshot()
	if !bytes.Equal(a, b) {
		t.Fatal("retained int64 API did not use canonical byte encoding")
	}
	if got := hex.EncodeToString(a); got != "7063673ab4d055fcf2cbbd7be82a6cb8f1b79d73" {
		t.Fatalf("public PCG64 snapshot=%s", got)
	}
}

func TestProblabSeedEntropyValidationAndSerialization(t *testing.T) {
	if _, err := newOneGameLabE(core.Default(), WithSeedEntropy(nil)); err == nil {
		t.Fatal("WithSeedEntropy(nil) was accepted")
	}
	if _, err := newOneGameLabE(core.Default(), WithSeedEntropy(errorReader{})); err == nil {
		t.Fatal("Factory probe seed failure was accepted")
	}

	reader := new(overlapSeedReader)
	lab := newOneGameLab(t, core.Default(), WithSeedEntropy(reader))
	defer func() { _ = lab.Close() }()
	const count = 24
	var wg sync.WaitGroup
	wg.Add(count)
	for range count {
		go func() {
			defer wg.Done()
			if _, err := lab.NewMachine(1, true); err != nil {
				t.Errorf("NewMachine: %v", err)
			}
		}()
	}
	wg.Wait()
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.overlap {
		t.Fatal("Problab did not serialize GenerateSeed reader access")
	}
}

func TestSeedMakerRetainsLegacyPCGChildren(t *testing.T) {
	want := []int64{
		128037461401270284, 3843965774113154634, 3104562361521036581, 3010530040661569343,
		6255338083666203554, 7412225948540403199, 7697872098978370805, 6379807614506516475,
		3658024938864814316, 3169766205137987970, 5507285551312934437, 4097404573191847130,
		6723555382643201725, 597629193858431833, 8035924143795794687, 8246506636712719301,
	}
	seedMaker := NewSeedMaker(0)
	for i, expected := range want {
		if got := seedMaker.Next(); got != expected {
			t.Fatalf("child[%d]=%d, want %d", i, got, expected)
		}
	}
}

func TestSimulatorAndMachinePoolUseFactoryStreamOrdinals(t *testing.T) {
	root := core.EncodeInt64Seed(1234)
	lab := newOneGameLab(t, core.PCG64(), WithSeedEntropy(bytes.NewReader(make([]byte, 64))))
	defer func() { _ = lab.Close() }()

	simulator, err := lab.NewSimulatorWithSeedBytes(1, root)
	if err != nil {
		t.Fatal(err)
	}
	workerZero, err := simulator.workerSeed(0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(workerZero, root) {
		t.Fatal("Simulator worker zero did not retain root identity")
	}
	legacy := NewSeedMaker(1234)
	for worker := 1; worker <= 16; worker++ {
		seed, err := simulator.workerSeed(worker)
		if err != nil {
			t.Fatal(err)
		}
		got, err := core.DecodeInt64Seed(seed)
		if err != nil {
			t.Fatal(err)
		}
		if want := legacy.Next(); got != want {
			t.Fatalf("Simulator worker %d seed=%d, want legacy child %d", worker, got, want)
		}
	}

	gs, err := lab.cat.GameSettingById(1)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := newMachinePool(4, gs, lab.reg, core.PCG64(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	legacy = NewSeedMaker(1234)
	for slot := uint64(0); slot < 4; slot++ {
		seed, err := pool.machineSeed(slot, 0)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := core.DecodeInt64Seed(seed)
		if want := legacy.Next(); got != want {
			t.Fatalf("pool slot %d seed=%d, want legacy child %d", slot, got, want)
		}
	}
	seed, err := pool.machineSeed(2, 3)
	if err != nil {
		t.Fatal(err)
	}
	want, err := core.PCG64().DeriveSeed(root, core.StreamID{Domain: "runtime/machine", Index: 14})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(seed, want) {
		t.Fatal("MachinePool slot/generation ordinal mapping changed")
	}
	if _, err := pool.machineSeed(4, 0); err == nil {
		t.Fatal("MachinePool accepted out-of-range slot")
	}
	if _, err := pool.machineSeed(3, math.MaxUint64/4+1); err == nil {
		t.Fatal("MachinePool accepted overflowing generation")
	}
}

func newOneGameLab(t testing.TB, factory core.PRNGFactory, opts ...ProblabOption) *Problab {
	t.Helper()
	lab, err := newOneGameLabE(factory, opts...)
	if err != nil {
		t.Fatalf("new one-game Problab: %v", err)
	}
	return lab
}

func newOneGameLabE(factory core.PRNGFactory, opts ...ProblabOption) (*Problab, error) {
	raw, err := fs.ReadFile(demo_configs.FS, "game_1_democascade.yaml")
	if err != nil {
		return nil, err
	}
	return NewAuto(
		factory,
		Configs(fstest.MapFS{"game.yaml": &fstest.MapFile{Data: raw}}),
		Logics(demo_logic.Logics),
		opts...,
	)
}

func testSpinRequest() *dto.SpinRequest {
	return &dto.SpinRequest{
		UID: "test", GameName: "demo_cascade", GameId: spec.GID(1),
		Bet: 30, BetMode: 0, BetMult: 1,
	}
}

type lifecycleFactory struct {
	base          core.PRNGFactory
	newCalls      atomic.Int64
	generateCalls atomic.Int64
	reseedCalls   atomic.Int64
	eventMu       sync.Mutex
	events        []string
}

func (f *lifecycleFactory) New(seed []byte) (core.PRNG, error) {
	f.newCalls.Add(1)
	rng, err := f.base.New(seed)
	if err != nil {
		return nil, err
	}
	return &lifecyclePRNG{owner: f, PRNG: rng, reseedCalls: &f.reseedCalls}, nil
}

func (f *lifecycleFactory) GenerateSeed(reader io.Reader) ([]byte, error) {
	f.generateCalls.Add(1)
	return f.base.GenerateSeed(reader)
}

func (f *lifecycleFactory) DeriveSeed(parent []byte, stream core.StreamID) ([]byte, error) {
	return f.base.DeriveSeed(parent, stream)
}

type lifecyclePRNG struct {
	owner *lifecycleFactory
	core.PRNG
	reseedCalls *atomic.Int64
}

func (r *lifecyclePRNG) Reseed() error {
	r.owner.recordEvent("reseed")
	r.reseedCalls.Add(1)
	return r.PRNG.Reseed()
}

func (r *lifecyclePRNG) Uint64() uint64 {
	r.owner.recordEvent("uint64")
	return r.PRNG.Uint64()
}

func (r *lifecyclePRNG) UintN(max uint) uint {
	r.owner.recordEvent("uintn")
	return r.PRNG.UintN(max)
}

func (r *lifecyclePRNG) IntN(max int) int {
	r.owner.recordEvent("intn")
	return r.PRNG.IntN(max)
}

func (r *lifecyclePRNG) Float64() float64 {
	r.owner.recordEvent("float64")
	return r.PRNG.Float64()
}

func (r *lifecyclePRNG) SnapshotFormat() string {
	if formatter, ok := r.PRNG.(core.SnapshotFormatter); ok {
		return formatter.SnapshotFormat()
	}
	return ""
}

func (f *lifecycleFactory) recordEvent(event string) {
	f.eventMu.Lock()
	f.events = append(f.events, event)
	f.eventMu.Unlock()
}

func (f *lifecycleFactory) clearEvents() {
	f.eventMu.Lock()
	f.events = nil
	f.eventMu.Unlock()
}

func (f *lifecycleFactory) recordedEvents() []string {
	f.eventMu.Lock()
	defer f.eventMu.Unlock()
	return append([]string(nil), f.events...)
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }

type overlapSeedReader struct {
	mu      sync.Mutex
	reading bool
	overlap bool
	next    byte
}

func (r *overlapSeedReader) Read(dst []byte) (int, error) {
	r.mu.Lock()
	if r.reading {
		r.overlap = true
	}
	r.reading = true
	value := r.next
	r.next++
	r.mu.Unlock()
	for range 20 {
		runtime.Gosched()
	}
	for i := range dst {
		dst[i] = value
	}
	r.mu.Lock()
	r.reading = false
	r.mu.Unlock()
	return len(dst), nil
}

// Copyright 2025 Zintix Labs
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

package v2

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/spec"
)

type deriveFailingFactory struct {
	base  core.PRNGFactory
	mu    sync.Mutex
	calls int
}

func (f *deriveFailingFactory) New(seed []byte) (core.PRNG, error) {
	return f.base.New(seed)
}

func (f *deriveFailingFactory) GenerateSeed(entropy io.Reader) ([]byte, error) {
	return f.base.GenerateSeed(entropy)
}

func (f *deriveFailingFactory) DeriveSeed([]byte, core.StreamID) ([]byte, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return nil, errors.New("deliberate derive failure")
}

func (f *deriveFailingFactory) deriveCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type derivedSeedRejectingFactory struct {
	base  core.PRNGFactory
	child []byte
}

func (f *derivedSeedRejectingFactory) New(seed []byte) (core.PRNG, error) {
	if bytes.Equal(seed, f.child) {
		return nil, errors.New("deliberate child rejection")
	}
	return f.base.New(seed)
}

func (f *derivedSeedRejectingFactory) GenerateSeed(entropy io.Reader) ([]byte, error) {
	return f.base.GenerateSeed(entropy)
}

func (f *derivedSeedRejectingFactory) DeriveSeed([]byte, core.StreamID) ([]byte, error) {
	return append([]byte(nil), f.child...), nil
}

func TestValidateRuntimeTargetRejectsRootSeedUnacceptableToFactory(t *testing.T) {
	lab := newCollectionFactoryLab(t, core.PCG64())
	defer func() { _ = lab.Close() }()
	config, err := ParseConfig([]byte(validConfigYAML))
	if err != nil {
		t.Fatal(err)
	}
	seed, err := HexSeed(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	config.Plans[0].Seed = seed
	tuner, err := NewTuner(config, lab)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tuner.Run(context.Background(), RunRequest{PlanID: config.Plans[0].ID})
	if err != nil {
		t.Fatalf("Run returned operational error: %v", err)
	}
	if result.Status != StatusInfeasibleConfig || len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != DiagnosticConfigInvalid {
		t.Fatalf("result status=%s diagnostics=%+v", result.Status, result.Diagnostics)
	}
	message := result.Diagnostics[0].Message
	t.Logf("root seed rejection: %s", message)
	for _, fragment := range []string{config.Plans[0].ID, "kind=hex", "32 bytes", "int64 seed must be exactly 8 bytes"} {
		if !strings.Contains(message, fragment) {
			t.Fatalf("root rejection message %q lacks %q", message, fragment)
		}
	}
}

func TestValidateRuntimeTargetDoesNotProbeDeriveSeed(t *testing.T) {
	factory := &deriveFailingFactory{base: core.Default()}
	lab := newCollectionFactoryLab(t, factory)
	defer func() { _ = lab.Close() }()
	plan := collectionFixturePlan(77, 1, 1, 1, 1)
	plan.Plan.ID = "one-root-worker"
	if _, diagnostic, err := validateRuntimeTarget(lab, plan); err != nil || diagnostic.StopsRun() {
		t.Fatalf("static validation diagnostic=%+v err=%v", diagnostic, err)
	}
	collected, diagnostics, err := NewCollector(lab).Collect(context.Background(), plan, 0)
	if err != nil || diagnostics.StopsRun() || len(collected.Classes[0].Samples) != 1 {
		t.Fatalf("single-root collection=%+v diagnostics=%+v err=%v", collected, diagnostics, err)
	}
	if calls := factory.deriveCalls(); calls != 0 {
		t.Fatalf("DeriveSeed calls=%d want=0", calls)
	}
}

func TestReplayFilledQuotaNeverDerivesWorkerSeed(t *testing.T) {
	factory := &deriveFailingFactory{base: core.Default()}
	lab := newCollectionFactoryLab(t, factory)
	defer func() { _ = lab.Close() }()
	workingDirectory, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	plan := collectionFixturePlan(77, 4, 16, 16, 4)
	plan.Plan.Collection.CollectedSeed = []string{preSeedSpecBankFixture}
	collector := NewCollector(lab)
	collector.WorkingDirectory = workingDirectory
	collected, diagnostics, err := collector.Collect(context.Background(), plan, 0)
	if err != nil || diagnostics.StopsRun() || collected.Evidence.ReplayAccepted != 16 || collected.Evidence.FreshSpins != 0 {
		t.Fatalf("replay-filled collection=%+v diagnostics=%+v err=%v", collected, diagnostics, err)
	}
	if calls := factory.deriveCalls(); calls != 0 {
		t.Fatalf("DeriveSeed calls=%d want=0", calls)
	}
}

func TestFreshFanOutSurfacesDeriveFailureWithSeedContext(t *testing.T) {
	factory := &deriveFailingFactory{base: core.Default()}
	lab := newCollectionFactoryLab(t, factory)
	defer func() { _ = lab.Close() }()
	plan := collectionFixturePlan(77, 1, 1, 1, 1)
	plan.Plan.Collection.CollectedSeed = []string{"seed_bank_1700000001_s1.bin"}
	collector := NewCollector(lab)
	collector.WorkingDirectory = t.TempDir()
	_, _, err := collector.Collect(context.Background(), plan, 0)
	if err == nil {
		t.Fatal("fresh fan-out unexpectedly succeeded")
	}
	for _, fragment := range []string{"worker 0", "logical ordinal 1", "root seed kind=int64", "8 bytes", "deliberate derive failure"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("derive error %q lacks %q", err, fragment)
		}
	}
}

func TestFreshFanOutSurfacesDerivedSeedRejectionAsFactoryContractFailure(t *testing.T) {
	factory := &derivedSeedRejectingFactory{base: core.Default(), child: []byte("bad-child")}
	lab := newCollectionFactoryLab(t, factory)
	defer func() { _ = lab.Close() }()
	plan := collectionFixturePlan(77, 1, 1, 1, 1)
	plan.Plan.Collection.CollectedSeed = []string{"seed_bank_1700000001_s1.bin"}
	collector := NewCollector(lab)
	collector.WorkingDirectory = t.TempDir()
	_, _, err := collector.Collect(context.Background(), plan, 0)
	if err == nil {
		t.Fatal("factory contract violation unexpectedly succeeded")
	}
	t.Logf("derived child rejection: %v", err)
	for _, fragment := range []string{
		"PRNGFactory contract failure", "worker 0", "logical ordinal 1",
		"root seed (kind=int64, 8 bytes)", "derived seed (9 bytes)", "deliberate child rejection",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("child rejection error %q lacks %q", err, fragment)
		}
	}
}

func TestValidateRuntimeTargetAcceptsArbitraryLengthSeedOnChaCha20(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	utf8Seed, _ := UTF8Seed("short")
	hexSeed, _ := HexSeed(strings.Repeat("ab", 32))
	for _, seed := range []SeedSpec{utf8Seed, hexSeed} {
		plan := collectionFixturePlan(1, 1, 1, 1, 1)
		plan.Plan.ID = "arbitrary-seed"
		plan.Plan.Seed = seed
		if _, diagnostic, err := validateRuntimeTarget(lab, plan); err != nil || diagnostic.StopsRun() {
			t.Fatalf("seed %q diagnostic=%+v err=%v", seed.String(), diagnostic, err)
		}
	}
}

func TestResolvedPlanSeedCopiesDoNotAlias(t *testing.T) {
	raw := strings.Replace(validConfigYAML, "seed: 4127483647", `seed: "utf8:immutable"`, 1)
	config, err := ParseConfig([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := config.ResolvePlan(config.Plans[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	config.Plans[0].Seed.material[0] = 'X'
	if resolved.Plan.Seed.String() != "utf8:immutable" {
		t.Fatalf("resolved seed aliased Config: %q", resolved.Plan.Seed.String())
	}

	override, _ := UTF8Seed("override")
	overridden, err := resolved.WithOverrides(RunOverrides{Seed: &override})
	if err != nil {
		t.Fatal(err)
	}
	override.material[0] = 'X'
	if overridden.Plan.Seed.String() != "utf8:override" {
		t.Fatalf("override seed aliased caller: %q", overridden.Plan.Seed.String())
	}
}

func TestRunReportOverridesAreDeepCopied(t *testing.T) {
	config, err := ParseConfig([]byte(validConfigYAML))
	if err != nil {
		t.Fatal(err)
	}
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	tuner, err := NewTuner(config, lab)
	if err != nil {
		t.Fatal(err)
	}
	game := spec.GID(999999)
	mode := 0
	seed, _ := UTF8Seed("report-seed")
	result, err := tuner.Run(context.Background(), RunRequest{
		PlanID:    config.Plans[0].ID,
		Overrides: RunOverrides{Game: &game, BetMode: &mode, Seed: &seed},
	})
	if err != nil || result.Status != StatusInfeasibleConfig {
		t.Fatalf("Run result=%+v err=%v", result, err)
	}
	game = 1
	mode = 9
	seed.material[0] = 'X'
	assertReportOverrides(t, result.Report.Overrides, 999999, 0, "utf8:report-seed")
}

func TestEarlyFailureRunReportOverridesAreDeepCopied(t *testing.T) {
	config, err := ParseConfig([]byte(validConfigYAML))
	if err != nil {
		t.Fatal(err)
	}
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	tuner, err := NewTuner(config, lab)
	if err != nil {
		t.Fatal(err)
	}
	game := spec.GID(7)
	mode := 3
	seed, _ := UTF8Seed("early-seed")
	result, err := tuner.Run(context.Background(), RunRequest{
		PlanID:    "missing-plan",
		Overrides: RunOverrides{Game: &game, BetMode: &mode, Seed: &seed},
	})
	if err != nil || result.Status != StatusInfeasibleConfig {
		t.Fatalf("Run result=%+v err=%v", result, err)
	}
	game = 8
	mode = 4
	seed.material[0] = 'X'
	assertReportOverrides(t, result.Report.Overrides, 7, 3, "utf8:early-seed")
}

func assertReportOverrides(t *testing.T, overrides RunOverrides, game spec.GID, betMode int, seed string) {
	t.Helper()
	if overrides.Game == nil || *overrides.Game != game || overrides.BetMode == nil || *overrides.BetMode != betMode ||
		overrides.Seed == nil || overrides.Seed.String() != seed {
		t.Fatalf("report overrides=%+v seed=%v", overrides, overrides.Seed)
	}
}

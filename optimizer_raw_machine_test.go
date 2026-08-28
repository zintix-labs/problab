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

package problab

import (
	"encoding/json"
	"testing"

	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	demooptimal "github.com/zintix-labs/problab/demo/optimal"
	"github.com/zintix-labs/problab/sdk/core"
)

// TestNewUnoptimizedMachineWithSeedPreservesReplayIdentity locks the collection
// boundary needed by optimizer/v2. Game 0 enables a published Optimal Artifact;
// a normal Machine therefore owns that artifact, while the raw optimizer Machine
// must bypass it and reproduce a spin directly from its pre-spin Core snapshot.
func TestNewUnoptimizedMachineWithSeedPreservesReplayIdentity(t *testing.T) {
	lab, err := NewAuto(
		core.Default(), Configs(demo_configs.FS), Logics(demo_logic.Logics),
		WithOptimalFS(demooptimal.FS),
	)
	if err != nil {
		t.Fatalf("NewAuto: %v", err)
	}
	defer func() { _ = lab.Close() }()

	normal, err := lab.NewMachineWithSeed(0, 77, true)
	if err != nil {
		t.Fatalf("NewMachineWithSeed: %v", err)
	}
	if normal.optimal == nil {
		t.Fatal("game 0 fixture did not enable its existing Optimal Artifact")
	}
	raw, err := lab.NewUnoptimizedMachineWithSeed(0, 77, true)
	if err != nil {
		t.Fatalf("NewUnoptimizedMachineWithSeed: %v", err)
	}
	if raw.optimal != nil {
		t.Fatal("raw optimizer Machine retained an Optimal Artifact")
	}

	before, err := raw.SnapshotCore()
	if err != nil {
		t.Fatalf("SnapshotCore: %v", err)
	}
	first, err := json.Marshal(raw.SpinInternal(0))
	if err != nil {
		t.Fatalf("marshal first raw spin: %v", err)
	}
	if err := raw.RestoreCore(before); err != nil {
		t.Fatalf("RestoreCore: %v", err)
	}
	second, err := json.Marshal(raw.SpinInternal(0))
	if err != nil {
		t.Fatalf("marshal replayed raw spin: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("pre-spin raw Core snapshot did not reproduce the collected outcome")
	}
}

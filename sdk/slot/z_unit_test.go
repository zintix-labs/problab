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

package slot_test

import (
	"testing"

	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/slot"
)

type testLogic struct{}

func (t *testLogic) GetResult(r *dto.SpinRequest, g *slot.Game) *buf.SpinResult {
	res := g.StartNewSpin(r)
	res.TotalWin = r.Bet
	return res
}

func TestLogicRegistry(t *testing.T) {
	reg := slot.NewLogicRegistry()
	if err := reg.Register("demo", func(g *slot.Game) (slot.GameLogic, error) { return &testLogic{}, nil }); err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if err := reg.Register("demo", func(g *slot.Game) (slot.GameLogic, error) { return &testLogic{}, nil }); err == nil {
		t.Fatalf("expected duplicate register error")
	}
	if _, err := reg.Build("demo", nil); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	reg2 := slot.NewLogicRegistry()
	_ = reg2.Register("demo", func(g *slot.Game) (slot.GameLogic, error) { return &testLogic{}, nil })
	if _, err := slot.MergeLogicRegistry(reg, reg2); err == nil {
		t.Fatalf("expected merge duplicate error")
	}
}

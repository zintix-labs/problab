// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package v2

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
)

// unfinishedLogic 包住 demo_normal，讓每一段都停在選擇點。
type unfinishedLogic struct{ inner slot.GameLogic }

func (l unfinishedLogic) GetResult(r *buf.SpinRequest, g *slot.Game) *buf.SpinResult {
	sr := l.inner.GetResult(r, g)
	sr.IsGameEnd = false
	sr.ChooseNext = append(sr.ChooseNext[:0], 1)
	return sr
}

// RGS 只交付單段遊戲：重播到未結束的結果時回報錯誤，不當成一局輸出。
func TestRGSReplayRejectsUnfinishedSegment(t *testing.T) {
	raw, err := fs.ReadFile(demo_configs.FS, "game_0_demonormal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "logic_key: demo_normal", "logic_key: rgs_unfinished", 1))
	reg := slot.NewLogicRegistry()
	if err := reg.Register("rgs_unfinished", func(g *slot.Game) (slot.GameLogic, error) {
		inner, err := demo_logic.Logics.Build("demo_normal", g)
		return unfinishedLogic{inner}, err
	}); err != nil {
		t.Fatal(err)
	}
	lab, err := problab.NewAuto(core.Default(), problab.Configs(fstest.MapFS{"game.yaml": &fstest.MapFile{Data: raw}}), problab.Logics(reg))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	m, err := newOptimizerMachine(lab, 0, []byte("seed"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := m.SnapshotCore()
	if err != nil {
		t.Fatal(err)
	}
	snap = append([]byte(nil), snap...)
	r := &rgsReplay{machine: m, snapshotLength: len(snap), mode: 0, bet: m.BetUnits[0]}
	_, err = r.spin(context.Background(), snap, 0)
	if !errors.Is(err, errRGSUnfinishedSegment) {
		t.Fatalf("err %v, want an unfinished-segment error", err)
	}
	var mismatch *rgsReplayMismatch
	if errors.As(err, &mismatch) {
		t.Fatal("an unfinished segment must not be reported as a replay mismatch")
	}
}

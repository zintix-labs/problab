// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab_test

// 公開 API 契約：遊戲單元測試可在 problab 套件外直接呼叫 slot.NewGame，並以帶欄位名的
// literal 建立 buf.SpinRequest。

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"testing"

	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/spec"
)

// 遊戲單元測試直接呼叫 slot.NewGame（位置參數 bool 的型別與極性不變），並以帶欄位名的
// literal 建立 buf.SpinRequest 後直接呼叫 GetResult。
func TestDirectSlotNewGameStillCompiles(t *testing.T) {
	raw, err := fs.ReadFile(demo_configs.FS, "game_0_demonormal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	gs, err := spec.GetGameSettingByYAML(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, isSim := range []bool{true, false} {
		var outputs [2][]byte
		for k := range outputs {
			prng, err := core.Default().New(core.EncodeInt64Seed(11))
			if err != nil {
				t.Fatal(err)
			}
			gh, err := slot.NewGame(gs, demo_logic.Logics, core.New(prng), isSim)
			if err != nil {
				t.Fatal(err)
			}
			if gh.OmitExtend() != isSim {
				t.Fatal("slot.NewGame bool polarity changed")
			}
			req := &buf.SpinRequest{BetMode: 0, BetMult: 1, Bet: 40}
			out, err := dto.NewSpinResultDTO(gh.GetResult(req))
			if err != nil {
				t.Fatal(err)
			}
			outputs[k], _ = json.Marshal(out)
		}
		if !bytes.Equal(outputs[0], outputs[1]) {
			t.Fatal("direct slot.NewGame usage is not deterministic")
		}
	}
}

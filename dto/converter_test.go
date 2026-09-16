// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package dto

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestIdentityConverterMatchesMarshal(t *testing.T) {
	r := SpinResult{TotalWin: 50, Bet: 30, BetMult: 1, State: SpinState{StartCoreSnapB64U: "start", AfterCoreSnapB64U: "after", Checkpoint: json.RawMessage(`{"choice":1}`)},
		GameModes: []GameModeResultDTO{{ActResults: []ActResultDTO{{ExtendResult: map[string]any{"text": "測試", "value": 3}}}}}}
	want, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := IdentityConverter(r)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%s %s %v", got, want, err)
	}
}

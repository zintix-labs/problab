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

package dto

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zintix-labs/problab/spec"
)

func TestDecodeSpinRequestGET(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/spin?bet_id=b1&game=demo&gid=7&bet=10&bet_mode=1&bet_mult=2&cycle=0", nil)
	req, err := DecodeSpinRequest(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.BetID != "b1" || req.GameName != "demo" || req.GameId != 7 {
		t.Fatalf("unexpected request: %+v", req)
	}
	if req.Bet != 10 || req.BetMode != 1 || req.BetMult != 2 || req.Cycle != 0 || req.Choice != nil || req.ChooseNext != nil {
		t.Fatalf("unexpected request: %+v", req)
	}
}

// GET 只接受新局參數：續段參數與已刪除的參數都明確拒絕，不能被忽略後當成新局執行。
func TestDecodeSpinRequestGETRejectsContinuationAndRemoved(t *testing.T) {
	for _, q := range []string{"uid=u1", "has_choice=true", "choice=0", "choose_next=2", "cycle=1"} {
		r := httptest.NewRequest(http.MethodGet, "/spin?game=demo&gid=7&bet=10&bet_mode=0&bet_mult=1&"+q, nil)
		if _, err := DecodeSpinRequest(r); err == nil {
			t.Errorf("GET with %s was accepted", q)
		}
	}
}

func TestDecodeSpinRequestPOST(t *testing.T) {
	payload := map[string]any{
		"bet_id":      "b2",
		"game":        "demo",
		"gid":         9,
		"bet":         5,
		"bet_mode":    0,
		"bet_mult":    1,
		"cycle":       2,
		"choice":      0,
		"choose_next": []int{4, 5},
	}
	data, _ := json.Marshal(payload)
	r := httptest.NewRequest(http.MethodPost, "/spin", bytes.NewReader(data))
	req, err := DecodeSpinRequest(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.GameId != 9 || req.Bet != 5 || req.BetMode != 0 || req.BetID != "b2" || req.Cycle != 2 {
		t.Fatalf("unexpected request: %+v", req)
	}
	if req.Choice == nil || *req.Choice != 0 || len(req.ChooseNext) != 2 {
		t.Fatalf("choice 0 must be distinguishable from no choice: %+v", req)
	}
}

func TestDecodeSpinRequestRejectsUnknownFields(t *testing.T) {
	for _, field := range []string{`"unknown":true`, `"uid":"u"`, `"has_choice":false`} {
		data := []byte(`{"gid":1,"game":"demo","bet":1,"bet_mode":0,"bet_mult":1,` + field + `}`)
		r := httptest.NewRequest(http.MethodPost, "/spin", bytes.NewReader(data))
		if _, err := DecodeSpinRequest(r); err == nil {
			t.Errorf("expected error for %s", field)
		}
	}
}

func TestParseChoiceAndCheckpoint(t *testing.T) {
	zero := 0
	for _, c := range []struct {
		name string
		req  SpinRequest
		ok   bool
	}{
		{"new game", SpinRequest{}, true},
		{"choice without choose_next", SpinRequest{Cycle: 1, Choice: &zero}, false},
		{"choose_next without choice", SpinRequest{Cycle: 1, ChooseNext: []int{1}}, false},
		{"continuation", SpinRequest{Cycle: 1, Choice: &zero, ChooseNext: []int{1}}, true},
	} {
		p, err := c.req.Parse("any")
		if (err == nil) != c.ok {
			t.Errorf("%s: err %v, want ok=%v", c.name, err, c.ok)
		}
		if err == nil && p.HasChoice != (c.req.Choice != nil) {
			t.Errorf("%s: HasChoice %v", c.name, p.HasChoice)
		}
	}
	// 只有 checkpoint、沒有快照的請求合法；快照與 checkpoint 分開交出。
	key := spec.LogicKey("dto_parse_checkpoint")
	if err := RegisterCheckpoint[parseCheckpoint](key); err != nil {
		t.Fatal(err)
	}
	req := SpinRequest{Cycle: 1, Choice: &zero, ChooseNext: []int{3}, StartState: &StartState{Checkpoint: json.RawMessage(`{"n":4}`)}}
	p, err := req.Parse(key)
	if err != nil {
		t.Fatal(err)
	}
	if cp, ok := p.Checkpoint.(*parseCheckpoint); !ok || cp.N != 4 || p.StartSnap != nil {
		t.Fatalf("unexpected parse: %+v", p)
	}
}

type parseCheckpoint struct {
	N int `json:"n"`
}

func TestSpinResultSegmentFields(t *testing.T) {
	// 單段的空欄位不輸出，選了索引 0 時輸出 "choice": 0。
	raw, err := json.Marshal(SpinResult{})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"bet_id", "cycle", "choice", "choose_next"} {
		if bytes.Contains(raw, []byte(`"`+key+`"`)) {
			t.Errorf("empty %s is in %s", key, raw)
		}
	}
	zero := 0
	raw, err = json.Marshal(SpinResult{BetID: "b", Cycle: 1, Choice: &zero, ChooseNext: []int{2}})
	if err != nil {
		t.Fatal(err)
	}
	var back SpinResult
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Choice == nil || *back.Choice != 0 || back.Cycle != 1 || back.BetID != "b" || len(back.ChooseNext) != 1 {
		t.Fatalf("round trip lost segment fields: %s", raw)
	}
}

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
)

func TestDecodeSpinRequestGET(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/spin?uid=u1&game=demo&gid=7&bet=10&bet_mode=1&bet_mult=2&cycle=3&choice=4", nil)
	req, err := DecodeSpinRequest(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.UID != "u1" || req.GameName != "demo" || req.GameId != 7 {
		t.Fatalf("unexpected request: %+v", req)
	}
	if req.Bet != 10 || req.BetMode != 1 || req.BetMult != 2 || req.Cycle != 3 {
		t.Fatalf("unexpected request: %+v", req)
	}
	if req.Choice != 4 {
		t.Fatalf("unexpected choice: %+v", req.Choice)
	}
}

func TestDecodeSpinRequestPOST(t *testing.T) {
	payload := map[string]any{
		"uid":      "u2",
		"game":     "demo",
		"gid":      9,
		"bet":      5,
		"bet_mode": 0,
		"bet_mult": 1,
		"cycle":    2,
	}
	data, _ := json.Marshal(payload)
	r := httptest.NewRequest(http.MethodPost, "/spin", bytes.NewReader(data))
	req, err := DecodeSpinRequest(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.GameId != 9 || req.Bet != 5 || req.BetMode != 0 {
		t.Fatalf("unexpected request: %+v", req)
	}
}

func TestDecodeSpinRequestRejectsUnknownFields(t *testing.T) {
	data := []byte(`{"gid":1,"game":"demo","bet":1,"bet_mode":0,"bet_mult":1,"unknown":true}`)
	r := httptest.NewRequest(http.MethodPost, "/spin", bytes.NewReader(data))
	if _, err := DecodeSpinRequest(r); err == nil {
		t.Fatalf("expected error for unknown field")
	}
}

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

package v1

import (
	"crypto/rand"
	"encoding/json"
	"math"
	"math/big"
	"net/http"

	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/server/httperr"
)

// SetByJson 傳入 JSON設定格式 以及希望模擬的局數
func (sh *SimHandler) SetByJson(w http.ResponseWriter, r *http.Request) {
	type SimRequestByJson struct {
		BetMode     int             `json:"bet_mode"`
		Rounds      int             `json:"round"`
		GameSetting json.RawMessage `json:"cfg"`
		Seed        *int64          `json:"seed,omitempty"`
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 1. decode request
	req := new(SimRequestByJson)
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20) // 5MB
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		httperr.Errs(w, errs.Wrap(err, "json decode failed"))
		return
	}

	// 2. vaild reounds
	if req.Rounds < 1 {
		httperr.Errs(w, errs.NewWarn("round must be at least 1"))
		return
	}
	if req.BetMode < 0 {
		httperr.Errs(w, errs.NewWarn("bet_mode must be non-negative integer"))
		return
	}
	if req.Seed == nil {
		rnd, err := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
		if err != nil {
			httperr.Errs(w, errs.NewWarn("seed generate failed"))
			return
		}
		v := rnd.Int64()
		req.Seed = &v
	}

	// 4. NewSimulator
	sim, err := sh.Problab.NewSimulatorByJSON(req.GameSetting, *req.Seed)
	if err != nil {
		httperr.Errs(w, err)
		return
	}
	result, _, err := sim.Sim(req.BetMode, req.Rounds, false)
	if err != nil {
		httperr.Log(sh.log, "simulate failed", err)
		httperr.Errs(w, err)
		return
	}

	// 6. 回傳Json
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

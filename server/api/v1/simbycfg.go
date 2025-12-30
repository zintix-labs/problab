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
		httperr.Errs(w, err)
		return
	}

	// 6. 回傳Json
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

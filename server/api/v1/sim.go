package v1

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"strconv"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/server/httperr"
	"github.com/zintix-labs/problab/spec"
	"github.com/zintix-labs/problab/stats"
)

type SimHandler struct {
	Problab *problab.Problab
}

func NewSimHandler(pb *problab.Problab) (*SimHandler, error) {
	return &SimHandler{Problab: pb}, nil
}

func (sh *SimHandler) Sim(w http.ResponseWriter, q *http.Request) {
	// 內部結構 不影響外部 也不被外部使用
	type SimRequestBody struct {
		GID     spec.GID `json:"gid"`
		BetMode int      `json:"bet_mode"`
		Round   int      `json:"round"`
		Seed    *int64   `json:"seed,omitempty"`
	}
	// 內部結構 不影響外部 也不被外部使用
	type SimResponse struct {
		Stats    *stats.StatReport `json:"stats"`
		UsedTime int64             `json:"used_ms"`
	}
	// ---
	req := new(SimRequestBody)
	if q.Method != http.MethodGet && q.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if q.Method == http.MethodGet {
		// gid
		if s := q.URL.Query().Get("gid"); s != "" {
			u, err := strconv.ParseUint(s, 10, 64)
			if err != nil {
				httperr.Errs(w, errs.NewWarn("gid must be non-negative integer"))
				return
			}
			req.GID = spec.GID(u)
		} else {
			// 直接空值
			httperr.Errs(w, errs.NewWarn("gid is required"))
			return
		}

		// bet_mode
		if m := q.URL.Query().Get("bet_mode"); m != "" {
			u, err := strconv.ParseInt(m, 10, 64)
			if err != nil {
				httperr.Errs(w, errs.NewWarn("bet_mode must be integer"))
				return
			}
			req.BetMode = int(u)
		} else {
			httperr.Errs(w, errs.NewWarn("bet_mode is required"))
			return
		}

		// round
		if r := q.URL.Query().Get("round"); r != "" {
			u, err := strconv.ParseInt(r, 10, 64)
			if err != nil {
				httperr.Errs(w, errs.NewWarn("round must be integer"))
				return
			}
			req.Round = int(u)
		} else {
			httperr.Errs(w, errs.NewWarn("round is required"))
			return
		}

		// seed
		if s := q.URL.Query().Get("seed"); s != "" {
			u, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				httperr.Errs(w, errs.NewWarn("seed must be int64"))
				return
			}
			v := u
			req.Seed = &v
		}
	}
	if q.Method == http.MethodPost {
		if err := json.NewDecoder(q.Body).Decode(req); err != nil {
			httperr.Errs(w, errs.NewWarn("invalid json:"+err.Error()))
			return
		}
	}
	// 業務檢驗
	_, ok := sh.Problab.EntryById(req.GID)
	if !ok {
		httperr.Errs(w, errs.NewWarn("gid not found"))
		return
	}
	if req.BetMode < 0 {
		httperr.Errs(w, errs.NewWarn("bet_mode must be non-negative integer"))
		return
	}
	if req.Round < 1 || req.Round > 1000000 {
		httperr.Errs(w, errs.NewWarn("round must be between 1 to 1,000,000"))
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
	sim, err := sh.Problab.NewSimulatorWithSeed(req.GID, *req.Seed)
	if err != nil {
		// 這裡的錯誤是來自problab 尊重錯誤分級
		httperr.Errs(w, errs.Wrap(err, fmt.Sprintf("build simulator err: %d", req.GID)))
		return
	}
	st, used, err := sim.Sim(req.BetMode, req.Round, false)
	if err != nil {
		// 這裡的錯誤來自simulator 尊重錯誤分級
		httperr.Errs(w, errs.Wrap(err, "simulate err"))
		return
	}
	resp := SimResponse{
		Stats:    st,
		UsedTime: used.Milliseconds(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (sh *SimHandler) SimPlayers(w http.ResponseWriter, r *http.Request) {
	// 內部結構 不影響外部 也不被外部使用
	type SimPlayerRequestBody struct {
		GID     spec.GID `json:"gid"`
		Player  int      `json:"player"`
		Bets    int      `json:"bets"`
		BetMode int      `json:"bet_mode"`
		Round   int      `json:"round"`
		Seed    *int64   `json:"seed,omitempty"`
	}
	// 內部結構 不影響外部 也不被外部使用
	type SimPlayerResponse struct {
		StatsReport *stats.StatReport       `json:"stats"`
		Estimator   *stats.EstimatorPlayers `json:"est"`
		UsedTime    int64                   `json:"used_ms"`
	}
	// ---
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req := new(SimPlayerRequestBody)
	if r.Method == http.MethodGet {
		gid := r.URL.Query().Get("gid")
		playersStr := r.URL.Query().Get("player")
		initBetsStr := r.URL.Query().Get("bets")
		betModeStr := r.URL.Query().Get("bet_mode")
		roundStr := r.URL.Query().Get("round")

		// gid
		if gid != "" {
			u, err := strconv.ParseUint(gid, 10, 64)
			if err != nil {
				httperr.Errs(w, errs.NewWarn("gid must be non-negative integer"))
				return
			}
			req.GID = spec.GID(u)
		} else {
			httperr.Errs(w, errs.NewWarn("gid is required"))
			return
		}

		// player
		if playersStr != "" {
			players, err := strconv.Atoi(playersStr)
			if err != nil {
				httperr.Errs(w, errs.NewWarn("player must be integer"))
				return
			}
			req.Player = players
		} else {
			httperr.Errs(w, errs.NewWarn("player is required"))
			return
		}

		// bets
		if initBetsStr != "" {
			initBet, err := strconv.Atoi(initBetsStr)
			if err != nil {
				httperr.Errs(w, errs.NewWarn("bets must be integer"))
				return
			}
			req.Bets = initBet
		} else {
			httperr.Errs(w, errs.NewWarn("bets is required"))
			return
		}

		// bet_mode
		if betModeStr != "" {
			betMode, err := strconv.Atoi(betModeStr)
			if err != nil {
				httperr.Errs(w, errs.NewWarn("bet_mode must be an integer"))
				return
			}
			req.BetMode = betMode
		} else {
			httperr.Errs(w, errs.NewWarn("bet_mode is required"))
			return
		}

		// round
		if roundStr != "" {
			rounds, err := strconv.Atoi(roundStr)
			if err != nil {
				httperr.Errs(w, errs.NewWarn("round must be integer"))
				return
			}
			req.Round = rounds
		} else {
			httperr.Errs(w, errs.NewWarn("round is required"))
			return
		}

		// seed
		if s := r.URL.Query().Get("seed"); s != "" {
			u, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				httperr.Errs(w, errs.NewWarn("seed must be int64"))
				return
			}
			v := u
			req.Seed = &v
		}
	}
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(r.Body).Decode(req); err != nil {
			httperr.Errs(w, errs.NewWarn("invalid json:"+err.Error()))
			return
		}
	}
	// 業務邏輯判斷
	if _, ok := sh.Problab.EntryById(req.GID); !ok {
		httperr.Errs(w, errs.NewWarn("gid not found"))
		return
	}
	if req.Player < 1 || req.Player > 100000 {
		httperr.Errs(w, errs.NewWarn("player must be between 1 and 100,000"))
		return
	}
	if req.Bets < 1 {
		httperr.Errs(w, errs.NewWarn("bets must be at least 1"))
		return
	}
	if req.BetMode < 0 {
		httperr.Errs(w, errs.NewWarn("bet_mode must be non-negative integer"))
		return
	}
	if req.Round < 1 || req.Round > 15000 {
		httperr.Errs(w, errs.NewWarn("round must be between 1 and 15,000"))
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
	// 取得sim
	sim, err := sh.Problab.NewSimulatorWithSeed(req.GID, *req.Seed)
	if err != nil {
		httperr.Errs(w, errs.Wrap(err, fmt.Sprintf("build simulator err: %d", req.GID)))
		return
	}
	st, est, used, err := sim.SimPlayers(4, req.Player, req.Bets, req.BetMode, req.Round, false)
	if err != nil {
		httperr.Errs(w, errs.Wrap(err, fmt.Sprintf("simulator err: %d", req.GID)))
		return
	}
	resp := &SimPlayerResponse{
		StatsReport: st,
		Estimator:   est,
		UsedTime:    used.Milliseconds(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

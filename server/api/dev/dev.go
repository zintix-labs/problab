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

// Package dev 提供 ProbLab Dev（make dev）的 HTTP endpoints：逐段執行一批局、player 模式的
// 續段，以及從每段的快照回放一整局。
//
// 執行與正式後端同一條路徑：每個請求以 NewMachine 建立 deterministic 的機台，逐段呼叫
// Machine.Spin。不提供統計模擬（改用 make run 或 /v1/sim）。
//
// 這些路由只在 ModeDev 註冊，沒有任何驗證機制，不得對外公開。
package dev

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/corefmt"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/seedspec"
	"github.com/zintix-labs/problab/server/httperr"
	"github.com/zintix-labs/problab/server/netsvr"
	"github.com/zintix-labs/problab/server/svrcfg"
	"github.com/zintix-labs/problab/spec"
)

const (
	maxRounds   = 1000    // 一次批次的局數上限，超過時截斷
	maxBodySize = 1 << 20 // 請求內容上限；續段與回放的請求帶有前端送回的 cp
)

// Register 註冊 ProbLab Dev 的頁面與 API：
//   - GET  /dev        ：內建的前端；/dev/games/<gid>.js 是機台外掛（先找 SvrCfg.DevGames）。
//   - GET  /dev/meta   ：機台清單與畫面需要的設定。
//   - POST /dev/spin   ：以 seed 跑一批局，或從 snap 回放一局的第 0 段。
//   - POST /dev/next   ：player 模式的續段。
//   - POST /dev/replay ：以每段的快照回放一整局。
func Register(svr netsvr.NetRouter, cfg *svrcfg.SvrCfg) {
	svr.Get("/dev/meta", devMeta(cfg))
	svr.Post("/dev/spin", devSpin(cfg))
	svr.Post("/dev/next", devNext(cfg))
	svr.Post("/dev/replay", devReplay(cfg))
	registerStatic(svr, cfg)
}

func devMeta(cfg *svrcfg.SvrCfg) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pb, err := getProblab(cfg)
		if err != nil {
			httperr.Errs(w, err)
			return
		}
		meta, err := buildMeta(pb)
		if err != nil {
			httperr.Errs(w, err)
			return
		}
		writeJSON(w, meta)
	}
}

// spinRequest 是 POST /dev/spin 的輸入。input 明確指定 value 是 seed 還是 snap，後端不從
// 內容判斷：utf8 seed 與 base64url 快照可能長得一樣。
type spinRequest struct {
	GID     *int   `json:"gid"`
	BetMode *int   `json:"betmode"`
	Choose  string `json:"choose"` // first、last、player
	Input   string `json:"input"`  // seed、snap
	Value   string `json:"value"`  // seed：空字串表示自動產生；snap：某一段的 start_b64u
	Rounds  int    `json:"rounds"`
}

type spinResponse struct {
	GID             spec.GID `json:"gid"`
	BetMode         int      `json:"betmode"`
	Choose          string   `json:"choose"`
	Input           string   `json:"input"`
	Seed            string   `json:"seed,omitempty"` // 實際使用的 seed，可以原樣貼回重現；snap 時省略
	RequestedRounds int      `json:"requested_rounds"`
	Rounds          int      `json:"rounds"`
	Clamped         bool     `json:"clamped"`        // 局數超過上限而被截斷
	PartialReplay   bool     `json:"partial_replay"` // snap 回放的局超過一段：只保證第 0 段與原局相同
	TotalBet        int      `json:"total_bet"`      // 只計已結束的局
	TotalWin        int      `json:"total_win"`      // 只計已結束的局
	RTP             float64  `json:"rtp"`            // 百分比
	Bets            []bet    `json:"bets"`
}

func devSpin(cfg *svrcfg.SvrCfg) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req := new(spinRequest)
		if err := decodeJSON(w, r, req); err != nil {
			httperr.Errs(w, err)
			return
		}
		resp, err := runSpin(cfg, req)
		if err != nil {
			httperr.Errs(w, err)
			return
		}
		writeJSON(w, resp)
	}
}

func runSpin(cfg *svrcfg.SvrCfg, req *spinRequest) (spinResponse, error) {
	pb, err := getProblab(cfg)
	if err != nil {
		return spinResponse{}, err
	}
	gid, betMode, err := requireGame(req.GID, req.BetMode)
	if err != nil {
		return spinResponse{}, err
	}
	mode, err := parseChoose(req.Choose)
	if err != nil {
		return spinResponse{}, err
	}
	resp := spinResponse{GID: gid, BetMode: betMode, Choose: string(mode), Input: req.Input, RequestedRounds: req.Rounds}

	var seed seedspec.Spec
	snap := ""
	rounds := 1
	switch req.Input {
	case "seed":
		if req.Value == "" {
			seed, err = randomSeed()
		} else if seed, err = seedspec.ParseText(req.Value); err != nil {
			err = errs.NewWarn("invalid seed: " + err.Error())
		}
		if err != nil {
			return spinResponse{}, err
		}
		resp.Seed = seed.String()
		if mode != choosePlayer {
			rounds = min(max(req.Rounds, 1), maxRounds)
		}
	case "snap":
		if snap = strings.TrimSpace(req.Value); snap == "" {
			return spinResponse{}, errs.NewWarn("snap must not be empty")
		}
		if _, err := corefmt.DecodeBase64URL(snap); err != nil {
			return spinResponse{}, errs.NewWarn("snap must be base64url: " + err.Error())
		}
		// 快照決定第 0 段的起點；機台本身的 seed 只影響之後的段。
		if seed, err = randomSeed(); err != nil {
			return spinResponse{}, err
		}
	case "":
		return spinResponse{}, errs.NewWarn("input is required: seed or snap")
	default:
		return spinResponse{}, errs.NewWarn(fmt.Sprintf("input must be seed or snap, got %q", req.Input))
	}
	resp.Rounds = rounds
	resp.Clamped = req.Input == "seed" && mode != choosePlayer && req.Rounds > maxRounds

	run, err := newRunner(pb, gid, betMode, seed)
	if err != nil {
		return spinResponse{}, err
	}
	resp.Bets = make([]bet, 0, rounds)
	for i := 1; i <= rounds; i++ {
		b, err := run.play(i, mode, snap)
		if err != nil {
			return spinResponse{}, err
		}
		if b.IsEnd {
			resp.TotalBet += b.Bet
			resp.TotalWin += b.Win
		}
		resp.Bets = append(resp.Bets, b)
	}
	if resp.TotalBet > 0 {
		resp.RTP = 100 * float64(resp.TotalWin) / float64(resp.TotalBet)
	}
	resp.PartialReplay = snap != "" && len(resp.Bets[0].Segments) > 1
	return resp, nil
}

// nextRequest 是 player 模式的續段，格式與正式環境的續段請求相同；伺服器不保存狀態。
type nextRequest struct {
	GID        *int            `json:"gid"`
	BetMode    *int            `json:"betmode"`
	Cycle      int             `json:"cycle"`
	Choice     *int            `json:"choice"`
	ChooseNext []int           `json:"choose_next"`
	Checkpoint json.RawMessage `json:"cp"` // 上一段結果的 cp，原樣送回
}

type nextResponse struct {
	Segment dto.SpinResult `json:"segment"`
}

func devNext(cfg *svrcfg.SvrCfg) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req := new(nextRequest)
		if err := decodeJSON(w, r, req); err != nil {
			httperr.Errs(w, err)
			return
		}
		seg, err := runNext(cfg, req)
		if err != nil {
			httperr.Errs(w, err)
			return
		}
		writeJSON(w, nextResponse{Segment: seg})
	}
}

func runNext(cfg *svrcfg.SvrCfg, req *nextRequest) (dto.SpinResult, error) {
	pb, err := getProblab(cfg)
	if err != nil {
		return dto.SpinResult{}, err
	}
	gid, betMode, err := requireGame(req.GID, req.BetMode)
	if err != nil {
		return dto.SpinResult{}, err
	}
	if err := validContinuation(req.Cycle, req.Choice, req.ChooseNext); err != nil {
		return dto.SpinResult{}, err
	}
	// 續段的起點依機台的 RNG 政策取得（正式規則），所以每次以新的 seed 建立機台。
	seed, err := randomSeed()
	if err != nil {
		return dto.SpinResult{}, err
	}
	run, err := newRunner(pb, gid, betMode, seed)
	if err != nil {
		return dto.SpinResult{}, err
	}
	seg, err := run.segment(req.Cycle, req.Choice, req.ChooseNext, "", req.Checkpoint)
	if err != nil {
		return dto.SpinResult{}, segmentErr(1, req.Cycle, err)
	}
	return seg, nil
}

// replaySegment 是一段的完整輸入：起始快照，以及續段的 choice、choose_next 與輸入 cp。
type replaySegment struct {
	StartB64U  string          `json:"start_b64u"`
	Cycle      int             `json:"cycle"`
	Choice     *int            `json:"choice"`
	ChooseNext []int           `json:"choose_next"`
	Checkpoint json.RawMessage `json:"cp"`
}

type replayRequest struct {
	GID      *int            `json:"gid"`
	BetMode  *int            `json:"betmode"`
	Segments []replaySegment `json:"segments"`
}

type replayResponse struct {
	Bet bet `json:"bet"`
}

func devReplay(cfg *svrcfg.SvrCfg) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req := new(replayRequest)
		if err := decodeJSON(w, r, req); err != nil {
			httperr.Errs(w, err)
			return
		}
		b, err := runReplay(cfg, req)
		if err != nil {
			httperr.Errs(w, err)
			return
		}
		writeJSON(w, replayResponse{Bet: b})
	}
}

// runReplay 讓每一段都從自己記錄的快照開始執行，所以整局完全重現，包含 optimal 續段與
// 選擇類遊戲；機台本身的 seed 不影響結果。
func runReplay(cfg *svrcfg.SvrCfg, req *replayRequest) (bet, error) {
	pb, err := getProblab(cfg)
	if err != nil {
		return bet{}, err
	}
	gid, betMode, err := requireGame(req.GID, req.BetMode)
	if err != nil {
		return bet{}, err
	}
	if n := len(req.Segments); n == 0 || n > problab.DefaultMaxSegments {
		return bet{}, errs.NewWarn(fmt.Sprintf("segments must contain 1 to %d entries, got %d", problab.DefaultMaxSegments, n))
	}
	for i, s := range req.Segments {
		if s.StartB64U == "" {
			return bet{}, errs.NewWarn(fmt.Sprintf("segment %d has no start_b64u", i))
		}
		if s.Cycle != i {
			return bet{}, errs.NewWarn(fmt.Sprintf("segment %d has cycle %d; cycles must be 0, 1, 2, ...", i, s.Cycle))
		}
		if i == 0 {
			if s.Choice != nil || len(s.ChooseNext) != 0 {
				return bet{}, errs.NewWarn("segment 0 must not carry choice or choose_next")
			}
		} else if err := validContinuation(s.Cycle, s.Choice, s.ChooseNext); err != nil {
			return bet{}, errs.Wrap(err, fmt.Sprintf("segment %d", i))
		}
	}
	seed, err := randomSeed()
	if err != nil {
		return bet{}, err
	}
	run, err := newRunner(pb, gid, betMode, seed)
	if err != nil {
		return bet{}, err
	}
	segments := make([]dto.SpinResult, 0, len(req.Segments))
	for i, s := range req.Segments {
		seg, err := run.segment(s.Cycle, s.Choice, s.ChooseNext, s.StartB64U, s.Checkpoint)
		if err != nil {
			return bet{}, segmentErr(1, i, err)
		}
		segments = append(segments, seg)
	}
	return newBet(1, run.wager, segments), nil
}

func validContinuation(cycle int, choice *int, chooseNext []int) error {
	switch {
	case cycle < 1:
		return errs.NewWarn(fmt.Sprintf("cycle must be at least 1 for a continuation, got %d", cycle))
	case len(chooseNext) == 0:
		return errs.NewWarn("choose_next must not be empty for a continuation")
	case choice == nil:
		return errs.NewWarn("choice is required for a continuation")
	case *choice < 0 || *choice >= len(chooseNext):
		return errs.NewWarn(fmt.Sprintf("choice %d out of range [0, %d)", *choice, len(chooseNext)))
	}
	return nil
}

func requireGame(gid, betMode *int) (spec.GID, int, error) {
	if gid == nil {
		return 0, 0, errs.NewWarn("gid is required")
	}
	if *gid < 0 {
		return 0, 0, errs.NewWarn(fmt.Sprintf("gid %d not found", *gid))
	}
	if betMode == nil {
		return 0, 0, errs.NewWarn("betmode is required")
	}
	return spec.GID(*gid), *betMode, nil
}

func getProblab(cfg *svrcfg.SvrCfg) (*problab.Problab, error) {
	if cfg == nil || cfg.Problab == nil {
		return nil, errs.NewFatal("problab is required")
	}
	return cfg.Problab, nil
}

// decodeJSON 解碼請求內容：限制大小、拒絕未知欄位與多餘的內容。
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return errs.NewWarn(fmt.Sprintf("request body exceeds %d bytes", maxBodySize))
		}
		return errs.NewWarn("invalid json: " + err.Error())
	}
	if dec.More() {
		return errs.NewWarn("invalid json: unexpected data after the request object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

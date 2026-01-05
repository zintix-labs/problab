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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/zintix-labs/problab/corefmt"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/spec"
)

type SpinRequest struct {
	UID       string   `json:"uid"`                  // 唯一識別碼
	GameName  string   `json:"game"`                 // 要玩的遊戲
	GameId    spec.GID `json:"gid"`                  // 遊戲機台編號
	Bet       int      `json:"bet"`                  // 投注額
	BetMode   int      `json:"bet_mode"`             // 投注模式(走BetUnit[i])
	BetMult   int      `json:"bet_mult"`             // 投注倍數(BetUnit[0]的幾倍)
	Cycle     int      `json:"cycle"`                // 第幾段會話
	Choice    int      `json:"choice,omitempty"`     // 可選：玩家在本段（cycle）所做的選擇值（允許為 0）。
	HasChoice bool     `json:"has_choice,omitempty"` // 可選：是否有「提供選擇」。
	// Contract（強硬約束，避免 choice=0 的雙重語意）：
	//   - 若 has_choice 為 false（或未提供），則 choice 必須省略；否則視為 request 格式錯誤。
	//   - 若 has_choice 為 true，則視為有選擇；choice 若省略則視為 0。
	StartState *StartState `json:"start_state,omitempty"` // 可選：由業務端帶入的引擎狀態（nil=新局；帶 start_b64u=回放/續玩）。
}

// DecodeSpinRequest 會把 HTTP 請求解碼成 SpinRequest。
//
// 支援：
//   - GET：從 query string 讀取參數（uid/game/gid/bet/bet_mode/bet_mult/cycle/choice/has_choice）。
//     注意：GET 建議僅用於「新局」或簡單測試；巢狀狀態（start_state/cp/jp）建議使用 POST。
//   - POST：從 JSON body 反序列化（支援 start_state）。
//
// StartState（start_state）語意：
//   - start_state 缺省 / 為 null / 為空物件：視為「新局」。
//   - start_state.start_b64u 有值：視為「回放（replay）/ 續玩（resume/continue）」：
//   - 回放：帶入當初記錄的 start_b64u，可在相同輸入條件下重現該局結果。
//   - 續玩：帶入上一段回傳的 after_b64u 作為新的 start_b64u，以延續 RNG 流水。
//   - 引擎的輸入只接受 start_b64u（Start）；after_b64u 只會出現在回應（SpinState），請求端不得自行填寫 after。
//
// Choice / HasChoice Contract（強硬約束，避免 choice=0 的雙重語意）：
//   - 若 has_choice 為 false（或未提供），則 choice 必須省略；否則視為 request 格式錯誤。
//   - 若 has_choice 為 true，則視為有選擇；choice 若省略則視為 0。
//
// 注意：
//   - 這裡只負責「解碼（decode）」與基本型別轉換，不做任何遊戲合法性校驗；
//     合法性（例如該 GID 是否存在、bet 是否可用）應由上層（Machine/Runtime）決定。
//   - 為避免過大 body 影響服務，POST 會對 body 做大小限制（預設 1MiB）。
//   - POST 會開啟 DisallowUnknownFields()，對未知欄位採用嚴格拒絕，以避免靜默丟資料。
func DecodeSpinRequest(r *http.Request) (*SpinRequest, error) {
	if r == nil {
		return nil, errs.NewWarn("nil request")
	}

	req := new(SpinRequest)

	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		req.UID = q.Get("uid")
		req.GameName = q.Get("game")

		if s := q.Get("gid"); s != "" {
			u, err := strconv.ParseUint(s, 10, 0)
			if err != nil {
				return nil, errs.NewWarn(fmt.Sprintf("invalid gid: %v", err))
			}
			req.GameId = spec.GID(u)
		}

		if s := q.Get("bet"); s != "" {
			v, err := strconv.Atoi(s)
			if err != nil {
				return nil, errs.NewWarn(fmt.Sprintf("invalid bet: %v", err))
			}
			req.Bet = v
		}

		if s := q.Get("bet_mode"); s != "" {
			v, err := strconv.Atoi(s)
			if err != nil {
				return nil, errs.NewWarn(fmt.Sprintf("invalid bet_mode: %v", err))
			}
			req.BetMode = v
		}

		if s := q.Get("bet_mult"); s != "" {
			v, err := strconv.Atoi(s)
			if err != nil {
				return nil, errs.NewWarn(fmt.Sprintf("invalid bet_mult: %v", err))
			}
			req.BetMult = v
		}

		if s := q.Get("cycle"); s != "" {
			v, err := strconv.Atoi(s)
			if err != nil {
				return nil, errs.NewWarn(fmt.Sprintf("invalid cycle: %v", err))
			}
			req.Cycle = v
		}

		if s := q.Get("choice"); s != "" {
			v, err := strconv.Atoi(s)
			if err != nil {
				return nil, errs.NewWarn(fmt.Sprintf("invalid choice: %v", err))
			}
			req.Choice = v
		}

		if s := q.Get("has_choice"); s != "" {
			v, err := strconv.ParseBool(s)
			if err != nil {
				return nil, errs.NewWarn("invalid has_choice value " + err.Error())
			}
			req.HasChoice = v
		}

		return req, nil

	case http.MethodPost:
		// 防止 body 過大（預設 1MiB）
		const maxBody = 1 << 20
		body := io.LimitReader(r.Body, maxBody)
		dec := json.NewDecoder(body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(req); err != nil {
			return nil, fmt.Errorf("invalid json: %w", err)
		}
		return req, nil

	default:
		return nil, fmt.Errorf("method not allowed")
	}
}

// StartState 是由業務端帶入的「引擎可恢復狀態」（可選）。
//
// 設計目標：
//   - 讓引擎維持純計算器（stateless / deterministic），而「可回放/可續玩」所需的狀態由業務端保存與回送。
//   - 新局：start_state 缺省即可；引擎會自行產生本局的 RNG 內部狀態並在回應中回傳 Start/After。
//   - 回放（Replay）：業務端帶入當初記錄的 start_b64u與狀態快照，即可重現該局結果。
//   - 續玩（Resume/Continue，多段流程）：業務端把上一段回應的 after_b64u 當作下一段的 start_b64u 以及必要的 cp/jp 快照送入，。
//
// 重要約束：
//   - Request 只允許提供 Start（start_b64u）；After（after_b64u）只會由引擎在 Response 回傳。
//   - cp / jp 為 opaque payload：業務端必須能 round-trip 保存與回送；請勿做二次 JSON 編碼（不要把 JSON 再包成字串）。
type StartState struct {
	// StartCoreSnapB64U：RNG Core 的「起始快照」Base64URL（URL-safe base64）字串。
	//   - 缺省：視為新局（引擎自行起始 RNG）。
	//   - 有值：視為回放/續玩（引擎從該快照 restore RNG）。
	// 注意：請求端不得提供 After；After 由引擎在回應中回傳，用於下一段續玩或審計存證。
	StartCoreSnapB64U string `json:"start_b64u,omitempty"`

	// Checkpoint：遊戲自定義的最小恢復狀態（opaque JSON）。
	//   - 典型用途：多段流程（例如 pick/respins/free 的中斷續玩）所需的最小狀態。
	//   - 引擎不要求固定結構；各遊戲可自行定義（建議包含 ver/version 以利未來演進）。
	//   - 業務端必須能完整 round-trip 保存與回送（建議 DB JSON/JSONB；或以 UTF-8 JSON 字串存 TEXT/Redis）。
	Checkpoint json.RawMessage `json:"cp,omitempty"`

	// JP（Jackpot）：本次版本先定義語意與協議（見下方說明）；實際欄位待後續接入。
	//   - Request 端：承載「輸入」所需的 JP 快照（含期數 issue/period 等）。
	//   - Response 端：回傳 hit 與 delta（異動值）等輸出。
	// TODO: add JP snapshot fields here.
}

// JP（Jackpot）(ToDo)
//
// 引擎層不維護彩金池的「最終金額」，而是以業務端提供的 JP 快照作為輸入，並在本次 Spin 結束後回傳：
//   - 是否中獎（hit）
//   - 各個 Pot 的異動值（delta：本次要 +多少 / -多少）
//
// ★ 期數（Issue / Period）與 inflight 競態處理（非常關鍵）：
//   - JP 快照必須包含「期數/期號」（例如 issue_id / period_id），業務端需以期數做 inflight 管控。
//   - 若本次 Spin 回來判定中 JP，業務端必須先確認該期是否仍可被拉走（claim）。
//       * 若該期尚未被拉走：正常結算，並依 delta 以原子操作更新池金額。
//       * 若該期已被拉走（代表同時 inflight 的其他 Spin 先一步成功 claim）：
//           1) 業務端必須拒絕/作廢這筆回傳結果（不可直接結算），避免重複或錯期發獎。
//           2) 立刻重送同一筆「原期數」的請求，且沿用本次的 StartCoreSnapB64U（確保盤面/命中仍一致）。
//           3) 重送時提供的 JP 快照需移除/清空「累積屯水彩金」（accumulated/pool amount），只保留「補底/保底金額」（base/guarantee）。
//              這會讓引擎在 RNG/盤面一致的前提下，回傳同樣的命中結果，但 JP 僅能以補底金額結算（不再含累積屯水）。
//           4) 重送時 JP 快照的「期數」必須維持為玩家當初 Spin 所在的原期數。
//              原因：即使該期在重送當下已被其他 inflight 成功 claim（邏輯上已「過期/被拉走」），
//              我們仍需保留玩家當初 Spin 時所處的期數語意與審計脈絡；
//              因此重送請求應同時帶上「原期數 + 只剩補底金額」的快照，代表該期累積屯水已不可用，只能以補底結算。
//
// 補充：上述 inflight 競態重送理論上發生機率極低。
//   - 只有在極短時間內，同一個 JP pool 有多筆 inflight 尚未回傳，且其中「多筆同時命中 JP」時才會觸發。
//   - 因此此策略的實務成本很低，但能徹底避免併發下的重複/錯期發獎風險。
//
// 目的：
//   - 避免併發下由引擎做加總造成競態；業務端應把 delta 以原子操作（例如 Redis INCRBY / Lua）套用到池金額。
//   - 業務端可決定是否要在發生網路丟包/重送時，把同一筆 request 視為「未成立」而重新發起新局。
//
// 備註：
//   - 這裡的 StartState 僅承載「輸入」所需的 JP 快照；輸出格式（snapshot/hit/delta）由 Response/SpinState 定義。

func (ss *StartState) HasPayload() bool {
	if ss == nil {
		return false
	}
	return ss.StartCoreSnapB64U != "" || len(ss.Checkpoint) != 0 // || JP...
}

func (sr *SpinRequest) Parse(key spec.LogicKey) (*buf.SpinRequest, error) {
	var state *buf.StartState
	start := sr.StartState
	if start.HasPayload() {
		state = new(buf.StartState)
		b64u := start.StartCoreSnapB64U
		if b64u == "" {
			return nil, errs.NewWarn("core snap is required")
		}
		snap, err := corefmt.DecodeBase64URL(b64u)
		if err != nil {
			return nil, errs.NewWarn("core snap decode failed " + err.Error())
		}
		state.StartCoreSnap = snap
		check := start.Checkpoint
		if len(check) != 0 {
			cp, err := DecodeCheckpoint(key, check)
			if err != nil {
				return nil, errs.NewWarn("check point decode failed " + err.Error())
			}
			state.Checkpoint = cp
		}
	}
	if !sr.HasChoice && sr.Choice != 0 {
		return nil, errs.NewWarn("has_choice is false but choice is not zero")
	}

	req := &buf.SpinRequest{
		UID:        sr.UID,
		GameName:   sr.GameName,
		GameId:     sr.GameId,
		Bet:        sr.Bet,
		BetMode:    sr.BetMode,
		BetMult:    sr.BetMult,
		Cycle:      sr.Cycle,
		Choice:     sr.Choice,
		HasChoice:  sr.HasChoice,
		StartState: state,
	}
	return req, nil
}

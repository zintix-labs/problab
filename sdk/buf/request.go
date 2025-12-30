package buf

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/spec"
)

type SpinRequest struct {
	UID      string   `json:"uid"`              // 唯一識別碼
	GameName string   `json:"game"`             // 要玩的遊戲
	GameId   spec.GID `json:"gid"`              // 遊戲機台編號
	Bet      int      `json:"bet"`              // 投注額
	BetMode  int      `json:"bet_mode"`         // 投注模式(走BetUnit[i])
	BetMult  int      `json:"bet_mult"`         // 投注倍數(BetUnit[0]的幾倍)
	Session  int      `json:"session"`          // 第幾段會話
	Choice   *int     `json:"choice,omitempty"` // 選擇遊戲做的選擇
}

// DecodeSpinRequest 會把 HTTP 請求解碼成 SpinRequest。
//
// 支援：
//   - GET：從 query string 讀取參數（uid/game/gid/bet/bet_mode/bet_mult/session/choice）。
//   - POST：從 JSON body 反序列化。
//
// 注意：
//   - 這裡只負責「解碼（decode）」與基本型別轉換，不做任何遊戲合法性校驗；
//     合法性（例如該 GID 是否存在、bet 是否可用）應由上層（Machine/Runtime）決定。
//   - 為避免過大 body 影響服務，POST 會對 body 做大小限制（預設 1MiB）。
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

		if s := q.Get("session"); s != "" {
			v, err := strconv.Atoi(s)
			if err != nil {
				return nil, errs.NewWarn(fmt.Sprintf("invalid session: %v", err))
			}
			req.Session = v
		}

		if s := q.Get("choice"); s != "" {
			v, err := strconv.Atoi(s)
			if err != nil {
				return nil, errs.NewWarn(fmt.Sprintf("invalid choice: %v", err))
			}
			req.Choice = &v
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

package v1

import (
	"encoding/json"
	"net/http"

	"github.com/zintix-labs/problab/recorder"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/stats"
)

type DistStat struct {
	// SpinRequest
	GameName string `json:"game_name"`
	BetUnits []int  `json:"bet_units"`
	BetMode  int    `json:"bet_mode"`
	BetMult  int    `json:"bet_mult"`
	Bet      int    `json:"bet"`
	// ResultRecord
	TotalWins []int `json:"total_wins"`
	BaseWins  []int `json:"base_wins"`
	FreeWins  []int `json:"free_wins"`
	Triggers  []int `json:"triggers"`
}

func Stat(w http.ResponseWriter, r *http.Request) {
	// Post方法限定
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// 嘗試解析
	dst := new(DistStat)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 對齊局數
	round := min(len(dst.TotalWins), len(dst.BaseWins), len(dst.FreeWins), len(dst.Triggers))
	if round < 1 {
		http.Error(w, "round must > 0", http.StatusBadRequest)
		return
	}

	// 繞過New方法，自己構造 SpinRecorder (否則會出錯)
	rec := &recorder.SpinRecorder{
		BetUnits: dst.BetUnits,
		BetUnit:  dst.BetUnits[dst.BetMode],
		Basic:    new(recorder.BasicRecord),
		Dist:     new(recorder.DistRecord),
		Player:   new(recorder.PlayerRecord),
	}
	rec.Dist.Bucket = stats.Buckets.GetBucketByBetUnit(rec.BetUnit)
	rec.Dist.TotalWinCollect = make([]int, len(stats.Buckets.WinBucketStr()))
	rec.Dist.BaseWinCollect = make([]int, len(stats.Buckets.WinBucketStr()))
	rec.Dist.FreeWinCollect = make([]int, len(stats.Buckets.WinBucketStr()))

	// 繞過New方法，自己構造 SpinResult (否則會出錯)
	sr := &buf.SpinResult{
		GameName:      dst.GameName,
		Bet:           dst.Bet,
		BetUnits:      dst.BetUnits,
		BetMode:       dst.BetMode,
		BetMult:       dst.BetMult,
		GameModeCount: 0,
		GameModeList:  make([]*buf.GameModeResult, 0, 2), // 預留2個作為fg判斷
	}
	m1 := &buf.GameModeResult{}
	m2 := &buf.GameModeResult{}
	for i := 0; i < round; i++ {
		// 賦值 spin request
		sr.TotalWin = dst.TotalWins[i]
		sr.GameModeCount++
		m1.TotalWin = dst.BaseWins[i]
		sr.GameModeList = append(sr.GameModeList, m1)
		if dst.Triggers[i] > 0 {
			sr.GameModeCount++
			m2.TotalWin = dst.FreeWins[i]
			sr.GameModeList = append(sr.GameModeList, m2)
		}
		// 紀錄
		rec.Record(sr)
		// 重置sr
		sr.GameModeCount = 0
		sr.GameModeList = sr.GameModeList[:0] // 清空長度
	}
	st := rec.Done()
	st.Done()
	st.Summary.GameName = dst.GameName
	if err := json.NewEncoder(w).Encode(st); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
}

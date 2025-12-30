package stats

import (
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/zintix-labs/problab/spec"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

var lang language.Tag = language.English

// 信賴區間
type CI struct {
	Lo float64 `json:"Lo"`
	Hi float64 `json:"Hi"`
}

// StatReport 遊戲統計報告
type StatReport struct {
	Summary *SummaryReport `json:"Summary"`
	Mult    *MultReport    `json:"Mult"`
	Dist    *DistReport    `json:"Dist"`
	Player  *PlayerReport  `json:"Player,omitzero"`
	isDone  bool
}

type SummaryReport struct {
	GameName    string   `json:"GameName"`
	GameId      spec.GID `json:"GameId"`
	BetUnits    []int    `json:"BetUnits"`
	BetUnit     int      `json:"BetUnit"`
	BetMode     int      `json:"BetMode"`
	BetMult     int      `json:"BetMult"`
	TotalBet    int      `json:"TotalBet"`
	TotalWin    int      `json:"TotalWin"`
	BaseWin     int      `json:"BaseWin"`
	FreeWin     int      `json:"FreeWin"`
	RTP         float64  `json:"RTP"`
	RtpCI       CI       `json:"RtpCI"`
	Std         float64  `json:"Std"`
	Cv          float64  `json:"Cv"`
	Trigger     int      `json:"Trigger"`
	TriggerRate float64  `json:"TriggerRate"`
	NoWinRounds int      `json:"NoWinRounds"`
	HitRate     float64  `json:"HitRate"`
	Rounds      int      `json:"Rounds"`
}

// MultReport 贏倍統計
//
// 紀錄時不紀錄，避免轉型成本。紀錄完成後Done()會將結果整理填入
type MultReport struct {
	TotalWinMult      float64 `json:"TotalWinMult"`
	BaseWinMult       float64 `json:"BaseWinMult"`
	FreeWinMult       float64 `json:"FreeWinMult"`
	TotalWinMultSqSum float64 `json:"TotalWinMultSqSum"` // 平方和
	BaseWinMultSqSum  float64 `json:"BaseWinMultSqSum"`  // 平方和
	FreeWinMultSqSum  float64 `json:"FreeWinMultSqSum"`  // 平方和
}

// DistReport 分數區間落點統計
type DistReport struct {
	WinBucket       []string  `json:"WinBucket"`
	TotalWinCollect []int     `json:"TotalWinCollect"`
	BaseWinCollect  []int     `json:"BaseWinCollect"`
	FreeWinCollect  []int     `json:"FreeWinCollect"`
	TotalWinDist    []float64 `json:"TotalWinDist"`
	BaseWinDist     []float64 `json:"BaseWinDist"`
	FreeWinDist     []float64 `json:"FreeWinDist"`
}

// PlayerReport 玩家統計
//
// 需使用PlayerRecord 才會統計
type PlayerReport struct {
	InitBalance int  `json:"InitBalance"`
	Balance     int  `json:"Balance"`
	MaxBalance  int  `json:"MaxBalance"`
	MinBalance  int  `json:"MinBalance"`
	Bust        bool `json:"Bust"`
	Cashout     bool `json:"Cashout"`
	Alive       bool `json:"Alive"`
}

// ============================================================
// ** 公開方法 **
// ============================================================

// Done 將累積計數轉換為最終統計結果並鎖定 isDone 標記。
//
// 所有遊戲統計過程因為性能原因只處理int的紀錄，所以統計完成後
//
// 請使用 Done 來通知 Statistician 統計已經完成，可以一次性計算統計結果
func (s *StatReport) Done() {
	if s.isDone {
		return
	}
	// Summary
	s.Summary.RTP = s.Rtp()
	s.Summary.RtpCI = s.Ci()
	s.Summary.Std = s.Std()
	s.Summary.Cv = s.Cv()

	// Player
	s.Player.Alive = !(s.Player.Bust || s.Player.Cashout)

	s.isDone = true
}

// Rtp 回傳整體 RTP（總贏分 / 總押注）
func (s *StatReport) Rtp() float64 {
	if s.Summary.Rounds == 0 {
		return 0
	}
	return (float64(s.Summary.TotalWin) / float64(s.Summary.TotalBet))
}

// Std 回傳單局贏分的標準差（以投注單位為基礎）
func (s *StatReport) Std() float64 {
	if s.Summary.Rounds < 2 || s.Summary.BetUnit == 0 {
		return 0
	}
	rounds := float64(s.Summary.Rounds)

	winMultPow := s.Mult.TotalWinMult * s.Mult.TotalWinMult
	variance := (s.Mult.TotalWinMultSqSum - winMultPow/rounds) / (rounds - 1)

	if variance < 0 {
		variance = 0
	}

	std := math.Sqrt(variance)
	return std
}

// Cv 回傳單局贏分的變異係數
func (s *StatReport) Cv() float64 {
	rtp := s.Rtp()
	std := s.Std()
	if rtp <= 0 {
		return 0
	}
	return (std / rtp)
}

// Ci 回傳(95% Rtp)信賴區間
func (s *StatReport) Ci() CI {
	rtp := s.Rtp()
	std := s.Std()
	rtpSe := float64(0)
	if s.Summary.Rounds > 1 {
		rtpSe = std / math.Sqrt(float64(s.Summary.Rounds))
	}
	ci := CI{
		Lo: max(rtp-1.96*rtpSe, 0.0),
		Hi: rtp + 1.96*rtpSe,
	}
	return ci
}

func (s *StatReport) WriteWith(w io.Writer, rep StatReportRender) error {
	s.Done()
	return rep.Write(w, s)
}

func (s *StatReport) StdOut(ut time.Duration) {
	formatDuration(ut, s.Summary.Rounds)
	sk, sm := s.fmtBasic()
	str := fmtTable(s.Summary.GameName, sk, sm)
	fmt.Println(str)
}

// ============================================================
// ** 內部方法 **
// ============================================================

func formatDuration(d time.Duration, spins int) {
	p := message.NewPrinter(lang)
	if d < 0 {
		d = -d
	}
	sec := d.Seconds()
	if sec <= 0 {
		sec = 1e-9
	}
	sps := int(float64(spins) / sec)
	if sec < 60.0 {
		p.Printf("used: %.2f seconds\nsps : %d spins/sec\n", sec, sps)
		return
	}
	s := int(d.Seconds()) % 60
	m := int(d.Minutes()) % 60
	h := int(d.Hours())
	if h == 0 {
		s = s % 60
		p.Printf("used: %dm %ds\nsps : %d spins/sec\n", m, s, sps)
		return
	}
	p.Printf("used: %dh:%dm:%ds\nsps : %d spins/sec\n", h, m, s, sps)
}

// StdOut

func (s *StatReport) fmtBasic() ([]string, map[string]string) {
	p := message.NewPrinter(lang)
	basic := map[string]string{
		"Game Name":    p.Sprintf("%s", s.Summary.GameName),
		"Game ID":      fmt.Sprintf("%d", s.Summary.GameId),
		"Total Rounds": p.Sprintf("%d", s.Summary.Rounds),
		"Total RTP":    p.Sprintf("%.2f %%", 100.0*s.Summary.RTP),
		"RTP 95% CI":   p.Sprintf("[%.2f%%,%.2f%%]", 100.0*s.Summary.RtpCI.Lo, 100.0*s.Summary.RtpCI.Hi),
		"Total Bet":    p.Sprintf("%d", s.Summary.TotalBet),
		"Total Win":    p.Sprintf("%d", s.Summary.TotalWin),
		"Base Win":     p.Sprintf("%d", s.Summary.BaseWin),
		"Free Win":     p.Sprintf("%d", s.Summary.FreeWin),
		"NoWin Rounds": p.Sprintf("%d", s.Summary.NoWinRounds),
		"Trigger":      p.Sprintf("%d", s.Summary.Trigger),
		"STD":          p.Sprintf("%.3f", s.Summary.Std),
		"CV":           p.Sprintf("%.3f", s.Summary.Cv),
	}
	keys := []string{"Game Name", "Game ID", "Total Rounds", "Total RTP", "RTP 95% CI", "Total Bet", "Total Win", "Base Win", "Free Win", "NoWin Rounds", "Trigger", "STD", "CV"}
	return keys, basic
}

func fmtTable(title string, keys []string, msg map[string]string) string {
	p := message.NewPrinter(lang)
	maxKeyLen := 0
	maxValLen := 0
	for k, m := range msg {
		if w := runewidth.StringWidth(k); w > maxKeyLen {
			maxKeyLen = w
		}
		if w := runewidth.StringWidth(m); w > maxValLen {
			maxValLen = w
		}
	}
	maxKeyLen += 2
	maxValLen += 2

	divider := "+" + strings.Repeat("-", maxKeyLen) + "+" + strings.Repeat("-", maxValLen) + "+\n"
	top := "+" + strings.Repeat("-", maxKeyLen+1+maxValLen) + "+\n"

	totalInner := maxKeyLen + maxValLen + 1
	titleW := runewidth.StringWidth(title)

	left := (totalInner - titleW) / 2
	right := totalInner - titleW - left

	fmtStr := top
	fmtStr += p.Sprintf("|%s%s%s|\n", blank(left), title, blank(right))
	fmtStr += divider
	for _, k := range keys {
		fmtStr += p.Sprintf("| %s%s | %s%s |\n", k, blank(maxKeyLen-2-runewidth.StringWidth(k)), msg[k], blank(maxValLen-2-runewidth.StringWidth(msg[k])))
	}
	fmtStr += divider

	return fmtStr
}

func blank(w int) string {
	if w < 1 {
		return ""
	}
	return strings.Repeat(" ", w)
}

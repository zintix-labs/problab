// Package rgs demonstrates application-owned conversion and Tuner injection.
package rgs

import (
	"encoding/json"
	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/dto"
	v2 "github.com/zintix-labs/problab/optimizer/v2"
)

// Convert illustrates a platform payload without replay state. Real applications
// must include the presentation/game fields required by their own consumers.
func Convert(r dto.SpinResult) (json.RawMessage, error) {
	return json.Marshal(struct {
		Win       int                     `json:"win"`
		Bet       int                     `json:"bet"`
		GameModes []dto.GameModeResultDTO `json:"game_modes,omitempty"`
	}{r.TotalWin, r.Bet, r.GameModes})
}

// New uses the SAME Lab that the application's optimizer runs against.
// No GameID registry or separate bank-to-JSONL process is required.
func New(config v2.Config, lab *problab.Problab) (*v2.Tuner, error) {
	return v2.NewTuner(config, lab, v2.WithResultConverter(Convert))
}

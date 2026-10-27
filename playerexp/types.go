// Copyright 2026 Zintix Labs
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

// Package playerexp analyzes finite player experiences on a sequential,
// deterministic game. Each game runs to its end through a BetRunner; at choice
// points the player's strategy chooses. It does not save or resume a player's
// session across processes.
package playerexp

import (
	"errors"
	"reflect"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/sdk/strategy"
	"github.com/zintix-labs/problab/sdk/tag"
)

// ErrInvalidDecision reports a strategy decision that does not fit the moment
// it was returned at: a choice outside a choice point, or anything but a valid
// Choice at one. A Choice out of range wraps problab.ErrChoiceOutOfRange instead.
var ErrInvalidDecision = errors.New("invalid strategy decision")

// MaxSpinsPerPlayer bounds the experience window, not engine performance.
// Longer sessions saturate encounter counts and belong in Simulator. Changing
// this product boundary changes report comparability; it is not configurable.
const MaxSpinsPerPlayer = 20_000

func nilLike(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	}
	return false
}

// Options borrows Lab. Run never closes it. Tags are scoped to the requested
// game, with no built-in names. Callbacks are synchronous and read-only.
type Options struct {
	Lab        *problab.Problab
	Strategies *strategy.Registry
	Tags       tag.GameTagCatalog
	Reporter   Reporter
}

// Reporter is called synchronously on the Run goroutine. It must be quick and
// must not mutate dependencies. A panic terminates the run with an error.
type Reporter interface{ Report(ProgressEvent) }

// ProgressEvent counts committed players, never a partially observed player.
// Kind is start/progress/completed/failed/canceled; indices are zero-based.
type ProgressEvent struct {
	Kind             string
	GroupIndex       int
	GroupName        string
	Completed, Total int
}

// Status describes execution, not the player's exit classification.
type Status string

const (
	NotStarted Status = "NOT_STARTED"
	Running    Status = "RUNNING"
	Completed  Status = "COMPLETED"
	Failed     Status = "FAILED"
	Canceled   Status = "CANCELED"
)

// Estimate/Interval explicitly distinguish unavailable values from real zeros.
type Estimate struct {
	Value   float64
	Defined bool
}

// Interval carries a reason when unavailable; its numeric zeros then have no
// statistical meaning. Method identifies the confidence interval algorithm.
type Interval struct {
	Lo, Hi         float64
	Available      bool
	Reason, Method string
}

// PointStat is a point estimate and independently available confidence interval.
type PointStat struct {
	Estimate
	CI Interval
}

// BucketStat counts complete spins from committed players, without a CI.
// Nil Max is unbounded. ObservedMax does not change the bucket boundary.
type BucketStat struct {
	Min                    float64
	Max                    *float64
	IncludeMin, IncludeMax bool
	Count                  uint64
	ObservedMax            Estimate
}

// EventStat.Counts/Stats are ordered zero/one/two/more (3+) encounters per
// player. Hits is the separate total number of matching spins, not players.
type EventStat struct {
	Counts [4]uint64
	Hits   uint64
	Stats  [4]PointStat
}

// RTPStat has Metric quantile or threshold (<= Parameter). N excludes players
// with no wagers; Count applies only to threshold rows.
type RTPStat struct {
	Metric    string
	Parameter float64
	N, Count  uint64
	PointStat
}

// ModeSource records actual loaded distribution selection, native or optimal.
type ModeSource struct {
	BetUnit int
	Source  string
}

// GroupReport contains only committed players. Started-Completed are excluded
// incomplete sessions. MaxSpinReached is a subset of the Others exit count.
type GroupReport struct {
	Config                                                            GroupConfig
	Status                                                            Status
	InitialBalance, Started, Completed                                int
	CompleteSpins, TotalBet, TotalWin, UndefinedRTPPlayers, Unmatched uint64
	Buckets                                                           []BucketStat
	Events                                                            []EventStat
	Exits                                                             [4]uint64 // bust, satisfied, dissatisfied, others
	ExitStats                                                         [4]PointStat
	MaxSpinReached                                                    uint64
	MaxSpinStat                                                       PointStat
	PooledRTP                                                         Estimate
	RTP                                                               []RTPStat
	Error                                                             string
	rtps                                                              []float64
}

// Report preserves ordered groups, including NOT_STARTED groups on early stop.
// It contains seed material and is not a public-player response format.
type Report struct {
	Config Config
	Status Status
	Modes  []ModeSource
	Groups []GroupReport
	Error  string
}

// JSONReport is the versioned full report exported beside the CSV tables.
// Report includes ordered analysis settings (including event tags), aggregate
// statistics and partial-run status, but no private per-player RTP samples.
// Nested field names follow the exported Report types. Undefined statistics
// must be interpreted using Defined/Available, never as a measured zero.
// Like Report, this document contains seed material; it is not for players.
type JSONReport struct {
	SchemaVersion string `json:"schema_version"`
	Report        Report `json:"report"`
}

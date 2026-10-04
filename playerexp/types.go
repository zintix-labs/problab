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
// deterministic, complete-result game. It does not support checkpoint recovery.
package playerexp

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/tag"
	"github.com/zintix-labs/problab/spec"
)

// MaxSpinsPerPlayer bounds the experience window, not engine performance.
// Longer sessions saturate encounter counts and belong in Simulator. Changing
// this product boundary changes report comparability; it is not configurable.
const MaxSpinsPerPlayer = 20_000

// PlayerState is a value snapshot of the executor's authoritative ledger.
// Next receives the settled current spin, not the pre-wager balance.
type PlayerState struct{ Balance, Spins, TotalBet, TotalWin int }

// Bet selects a game-specific mode and a strictly positive multiplier.
type Bet struct{ BetMode, BetMult int }

// EndType is a mutually exclusive player outcome. Bust is executor-owned.
type EndType uint8

const (
	Other EndType = iota
	Bust
	Satisfied
	Dissatisfied
)

// Decision uses Bet only while KeepPlay; otherwise only EndType is used.
// Its zero value leaves as Other without making a wager.
type Decision struct {
	KeepPlay bool
	Bet
	EndType
}

// StrategyFactory creates independent player memory. Configure factories before
// registration; they and their closure dependencies must not change during Run.
type StrategyFactory interface {
	NewStrategy(initialBalance int) Strategy
}

// Strategy reads authoritative state and borrowed, read-only results. Copy only
// needed values for history; dto.NewSpinResultDTO is an optional full-result
// copy (Extend independence still depends on the game's Snapshot contract).
// A blocked callback cannot be preempted by context cancellation.
type Strategy interface {
	FirstSpin(PlayerState) Decision
	Next(PlayerState, *buf.SpinResult) Decision
}

// StrategyRegistry is game-scoped. Its zero value works; registration and Run
// must not overlap. Factories themselves are borrowed, not cloned.
type StrategyRegistry struct {
	factories map[spec.GID]map[string]StrategyFactory
}

// NewStrategyRegistry creates an empty game-scoped registry.
func NewStrategyRegistry() *StrategyRegistry { return &StrategyRegistry{} }

// Register binds an exact name; duplicates and nil/typed-nil factories fail.
func (r *StrategyRegistry) Register(gid spec.GID, name string, f StrategyFactory) error {
	if r == nil || strings.TrimSpace(name) == "" || nilLike(f) {
		return fmt.Errorf("invalid strategy registration %q", name)
	}
	if _, ok := r.Get(gid, name); ok {
		return fmt.Errorf("duplicate strategy %q for game %d", name, gid)
	}
	if r.factories == nil {
		r.factories = make(map[spec.GID]map[string]StrategyFactory)
	}
	if r.factories[gid] == nil {
		r.factories[gid] = make(map[string]StrategyFactory)
	}
	r.factories[gid][name] = f
	return nil
}

// Get resolves only the requested game's namespace; nil registries are empty.
func (r *StrategyRegistry) Get(gid spec.GID, name string) (StrategyFactory, bool) {
	if r == nil {
		return nil, false
	}
	f, ok := r.factories[gid][name]
	return f, ok
}
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
	Strategies *StrategyRegistry
	Tags       map[spec.GID]map[string]tag.IsTag
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

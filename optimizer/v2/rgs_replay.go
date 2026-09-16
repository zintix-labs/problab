// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package v2

import (
	"context"
	"fmt"
	"reflect"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/dto"
	"github.com/zintix-labs/problab/sdk/buf"
)

// A family owns its replay machine. No worker stream or disk bank is consumed.
type rgsReplay struct {
	machine                   *problab.Machine
	previous                  *buf.SpinResult
	snapshotLength, mode, bet int
}

// Only definite source/result mismatches are representation failures. Unknown
// third-party Restore/Snapshot errors remain operational and keep their chain.
type rgsReplayMismatch struct{ message string }

func (e *rgsReplayMismatch) Error() string { return e.message }

func newRGSReplay(lab *problab.Problab, plan ResolvedPlan, bet int) (*rgsReplay, error) {
	machine, err := newOptimizerMachine(lab, plan.Plan.Target.Game, plan.Plan.Seed.Bytes())
	if err != nil {
		return nil, err
	}
	snapshot, err := machine.SnapshotCore()
	if err != nil {
		return nil, err
	}
	return &rgsReplay{machine: machine, snapshotLength: len(snapshot), mode: plan.Plan.Target.BetModes[0], bet: bet}, nil
}

func (r *rgsReplay) spin(ctx context.Context, snapshot []byte, win float64) (*buf.SpinResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(snapshot) != r.snapshotLength {
		return nil, &rgsReplayMismatch{message: fmt.Sprintf("snapshot length=%d want=%d", len(snapshot), r.snapshotLength)}
	}
	request := r.machine.SpinRequest
	request.Choice, request.HasChoice, request.Cycle, request.StartState = 0, false, 0, nil
	if r.previous != nil && r.previous.State != nil {
		r.previous.State.StartCoreSnap, r.previous.State.AfterCoreSnap, r.previous.State.Checkpoint = nil, nil, nil
	}
	if err := r.machine.RestoreCore(snapshot); err != nil {
		return nil, fmt.Errorf("restore: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := r.machine.SpinInternal(r.mode)
	r.previous = result
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if result == nil || result.Bet <= 0 || result.Bet != r.bet || result.BetMode != r.mode || result.BetMult != 1 || result.TotalWin < 0 {
		return nil, &rgsReplayMismatch{message: "invalid replay result or bet identity"}
	}
	actual := float64(result.TotalWin) / float64(result.Bet)
	if !isFinite(actual) || actual != win {
		return nil, &rgsReplayMismatch{message: fmt.Sprintf("replay multiplier %.17g differs from source %.17g", actual, win)}
	}
	if result.State == nil {
		result.State = &buf.SpinState{}
	}
	result.State.StartCoreSnap = append([]byte(nil), snapshot...)
	after, err := r.machine.SnapshotCore()
	if err != nil {
		return nil, fmt.Errorf("snapshot after replay: %w", err)
	}
	result.State.AfterCoreSnap = append([]byte(nil), after...)
	return result, nil
}

// WithResultConverter injects one synchronous converter per Tuner. Passing nil
// selects dto.IdentityConverter, which includes replay state in the payload.
func WithResultConverter(converter dto.ResultConverter) TunerOption {
	return func(t *Tuner) error {
		t.resultConverter = converter
		t.customConverter = converter != nil &&
			reflect.ValueOf(converter).Pointer() != reflect.ValueOf(dto.IdentityConverter).Pointer()
		return nil
	}
}

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

package v2

import (
	"bytes"
	"fmt"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/sdk/buf"
)

const (
	entryContractCheck    = "artifact.entry_contract"
	entryContractRequired = "every entry-segment seed gives the same outcome and post-run Core with another positive cycle and with the entry as the only option"
)

// entrySample is the outcome and post-run Core of one replay of an entry
// segment, copied out of the Machine's buffers.
type entrySample struct {
	outcome segmentFingerprint
	core    []byte
}

// captureEntrySample copies the segment just run and the Machine's Core.
// PRNG snapshots may reuse their return buffer, so the Core is copied at once;
// keeping the slice would let the next run overwrite it and hide a difference.
func captureEntrySample(machine *problab.Machine, result *buf.SpinResult) (entrySample, error) {
	core, err := machine.SnapshotCore()
	if err != nil {
		return entrySample{}, fmt.Errorf("snapshot Core after entry replay: %w", err)
	}
	return entrySample{outcome: fingerprintSegment(result), core: append([]byte(nil), core...)}, nil
}

// checkEntryContract replays seed twice more, with another positive cycle and
// with the entry as the only option, and compares both with base. A finite
// counterexample search: passing does not prove the contract, which remains
// the game author's guarantee.
func checkEntryContract(machine *problab.Machine, target Target, betMode int, seed []byte, sampleIndex int, base entrySample) (VerificationCheck, error) {
	entry := target.Input.Entry()
	contrasts := []struct {
		name string
		in   problab.SpinInput
	}{
		{"cycle 2", problab.SpinInput{BetMode: betMode, BetMult: 1, Cycle: 2, ChooseNext: target.Input.ChooseNext, Choice: *target.Input.Choice}},
		{"single option", problab.SpinInput{BetMode: betMode, BetMult: 1, Cycle: 1, ChooseNext: []int{entry}}},
	}
	for _, contrast := range contrasts {
		if err := machine.RestoreCore(seed); err != nil {
			return textVerificationCheck(entryContractCheck, false,
				fmt.Sprintf("sample[%d] seed-bank snapshot cannot be restored for the %s contrast: %v", sampleIndex, contrast.name, err),
				entryContractRequired), nil
		}
		result := machine.SpinUnsafe(contrast.in)
		if result == nil {
			return textVerificationCheck(entryContractCheck, false,
				fmt.Sprintf("sample[%d] entry %d was rejected with %s", sampleIndex, entry, contrast.name),
				entryContractRequired), nil
		}
		got, err := captureEntrySample(machine, result)
		if err != nil {
			return VerificationCheck{}, err
		}
		if !got.outcome.equal(base.outcome) {
			return textVerificationCheck(entryContractCheck, false,
				fmt.Sprintf("sample[%d] entry %d outcome differs with %s", sampleIndex, entry, contrast.name),
				entryContractRequired), nil
		}
		if !bytes.Equal(got.core, base.core) {
			return textVerificationCheck(entryContractCheck, false,
				fmt.Sprintf("sample[%d] entry %d consumes the RNG differently with %s", sampleIndex, entry, contrast.name),
				entryContractRequired), nil
		}
	}
	return VerificationCheck{Name: entryContractCheck, Pass: true}, nil
}

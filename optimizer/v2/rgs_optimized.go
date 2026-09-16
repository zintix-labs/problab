// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package v2

import (
	"fmt"
	"math"

	"github.com/zintix-labs/problab/sdk/buf"
)

func normalizeRGSProbabilities(samples []MaterializedSample, tolerance float64) ([]float64, error) {
	var sum compensatedSum
	for i, s := range samples {
		if !isFinite(s.Probability) || s.Probability < 0 {
			return nil, fmt.Errorf("sample[%d] invalid probability", i)
		}
		sum.Add(s.Probability)
	}
	total := sum.Value()
	if !isFinite(total) || total <= 0 || math.Abs(total-1) > scaledTolerance(tolerance, total, 1) {
		return nil, fmt.Errorf("point probability sum %.17g is not near one", total)
	}
	p := make([]float64, len(samples))
	var normalized compensatedSum
	for i, s := range samples {
		p[i] = s.Probability / total
		if !isFinite(p[i]) || p[i] < 0 || p[i] > 1 {
			return nil, fmt.Errorf("invalid normalized probability at %d", i)
		}
		normalized.Add(p[i])
	}
	if math.Abs(normalized.Value()-1) > artifactProbabilityTolerance {
		return nil, fmt.Errorf("normalized probability sum differs from one")
	}
	return p, nil
}

func verifyRGSPoints(compiled CompiledModel, solution EngineSolution, samples []MaterializedSample, p []float64) VerificationReport {
	replay, failure := replayPointDistribution(compiled, samples, p)
	checks := []VerificationCheck{textVerificationCheck("rgs.point_distribution", failure == "", verificationFailureActual(failure, "finite point probabilities"), "normalized point probabilities, not alias marginals")}
	return verifyPointSemantics(compiled, solution, samples, compiled.Prepared.BetMode, compiled.Prepared.BetUnit, replay, failure, checks)
}

func optimizedRGSRows(compiled CompiledModel, samples []MaterializedSample, p []float64) (rgsRows, error) {
	tagger, predicates, err := compileTagPredicates(compiled.Prepared.Plan.Intent.Classes)
	if err != nil {
		return nil, err
	}
	classes := make(map[string]int, len(compiled.Prepared.Classes))
	for i, c := range compiled.Prepared.Plan.Intent.Classes {
		if _, exists := classes[c.Name]; exists {
			return nil, fmt.Errorf("ambiguous Class %q", c.Name)
		}
		classes[c.Name] = i
	}
	return func(visit func(rgsSourceRow) error) error {
		for i, s := range samples {
			ci, ok := classes[s.ClassID]
			if !ok {
				return fmt.Errorf("unknown Class %q", s.ClassID)
			}
			row := rgsSourceRow{row: collectionDescriptorRow{RecordIndex: int64(i), ClassID: int32(ci), ClassName: s.ClassID, Probability: &p[i], WinMultiplier: s.Win}, snapshot: s.Snapshot}
			row.validate = func(result *buf.SpinResult) error {
				tags := uint64(0)
				if tagger != nil {
					tags = tagger.Tagging(result)
				}
				intent := compiled.Prepared.Plan.Intent.Classes[ci]
				bucket := 0
				if compiled.Prepared.Classes[ci].Intent {
					bucket = atomicBucketIndex(intent.Design.Subjective.Buckets, s.Win)
				}
				if !classAccepts(intent, predicates[ci], tags, s.Win) || bucket != s.BucketIndex {
					v := finalizeVerification([]VerificationCheck{textVerificationCheck("rgs.snapshot_runtime_replay", false, fmt.Sprintf("sample[%d] Class/bucket mismatch", i), "replay matches modeled Class and bucket")})
					return &rgsVerificationError{report: v}
				}
				return nil
			}
			if err := visit(row); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

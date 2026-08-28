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
	"math"
	"testing"
)

// TestBuildBucketDistributionReportUsesRuntimeSeedMarginals protects every
// coordinate shown by cmd/opt: conditional Bucket mass, whole-game mass, actual
// per-seed range, endpoint semantics, and the inverted 25% collision threshold.
func TestBuildBucketDistributionReportUsesRuntimeSeedMarginals(t *testing.T) {
	enabled, disabled := true, false
	compiled := CompiledModel{Prepared: PreparedProblem{Classes: []PreparedClass{
		{
			ID: "controlled", Probability: 0.4, Intent: true,
			Design: ClassDesign{Subjective: SubjectiveIntent{Intent: &enabled, Buckets: []float64{0, 1, 2}}},
			Buckets: []PreparedBucket{
				{Samples: []CollectedSample{{}, {}}},
				{Samples: []CollectedSample{{}}},
			},
		},
		{
			ID: "uniform", Probability: 0.6, Intent: false, CollectRange: ClosedInterval{0, 4},
			Design:  ClassDesign{Subjective: SubjectiveIntent{Intent: &disabled}},
			Buckets: []PreparedBucket{{Samples: []CollectedSample{{}, {}}}},
		},
	}}}
	mode := MaterializedMode{
		BetMode: 2,
		Samples: []MaterializedSample{
			{ClassID: "controlled", BucketIndex: 0},
			{ClassID: "controlled", BucketIndex: 0},
			{ClassID: "controlled", BucketIndex: 1},
			{ClassID: "uniform", BucketIndex: 0},
			{ClassID: "uniform", BucketIndex: 0},
		},
		EffectiveProbabilities: []float64{0.05, 0.15, 0.20, 0.30, 0.30},
	}

	report, err := BuildBucketDistributionReport(compiled, mode)
	if err != nil {
		t.Fatalf("BuildBucketDistributionReport: %v", err)
	}
	if report.BetMode != 2 || report.CollisionProbability != 0.25 || len(report.Classes) != 2 {
		t.Fatalf("report header = %+v", report)
	}
	first := report.Classes[0].Buckets[0]
	distributionTestClose(t, "conditional mass", first.ConditionalProbability, 0.5)
	distributionTestClose(t, "unconditional mass", first.UnconditionalProbability, 0.2)
	distributionTestClose(t, "minimum seed probability", first.SeedProbabilityMin, 0.05)
	distributionTestClose(t, "maximum seed probability", first.SeedProbabilityMax, 0.15)
	if first.Lower != 0 || first.Upper != 1 || first.UpperInclusive || first.SeedCount != 2 {
		t.Fatalf("first controlled bucket = %+v", first)
	}
	if first.DrawsAtCollisionProbability != 6 {
		t.Fatalf("first controlled bucket collision draws = %.0f, want 6", first.DrawsAtCollisionProbability)
	}
	last := report.Classes[0].Buckets[1]
	if !last.UpperInclusive || last.DrawsAtCollisionProbability != 5 {
		t.Fatalf("last controlled bucket = %+v", last)
	}
	uniform := report.Classes[1].Buckets[0]
	if uniform.Lower != 0 || uniform.Upper != 4 || !uniform.UpperInclusive || uniform.DrawsAtCollisionProbability != 3 {
		t.Fatalf("uniform bucket = %+v", uniform)
	}
}

func distributionTestClose(t *testing.T, name string, actual, expected float64) {
	t.Helper()
	if math.Abs(actual-expected) > 1e-12 {
		t.Fatalf("%s = %.17g, want %.17g", name, actual, expected)
	}
}

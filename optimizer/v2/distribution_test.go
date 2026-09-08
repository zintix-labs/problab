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
// average seed probability, empirical payout statistics, endpoint semantics,
// and the inverted 25% collision threshold from actual runtime marginals.
func TestBuildBucketDistributionReportUsesRuntimeSeedMarginals(t *testing.T) {
	enabled, disabled := true, false
	compiled := CompiledModel{Prepared: PreparedProblem{Classes: []PreparedClass{
		{
			ID: "controlled", Probability: 0.4, Intent: true,
			Design: ClassDesign{Subjective: SubjectiveIntent{Intent: &enabled, Buckets: []float64{0, 1, 2}}},
			Buckets: []PreparedBucket{
				{Samples: []CollectedSample{{Win: 0.8}, {Win: 0.2}}, Mean: 0.5},
				{Samples: []CollectedSample{{Win: 1.5}}, Mean: 1.5},
			},
		},
		{
			ID: "uniform", Probability: 0.6, Intent: false, CollectRange: ClosedInterval{0, 4},
			Design:  ClassDesign{Subjective: SubjectiveIntent{Intent: &disabled}},
			Buckets: []PreparedBucket{{Samples: []CollectedSample{{Win: 4}, {Win: 0}, {Win: 1}}, Mean: 5.0 / 3}},
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
			{ClassID: "uniform", BucketIndex: 0},
		},
		EffectiveProbabilities: []float64{0.05, 0.15, 0.20, 0.20, 0.20, 0.20},
	}
	compiled.Prepared.Plan.EngineOptions = DefaultEngineOptions()

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
	distributionTestClose(t, "seed probability", first.SeedProbability, 0.1)
	distributionTestClose(t, "even lower median", first.Median, 0.2)
	distributionTestClose(t, "mean", first.Mean, 0.5)
	if compiled.Prepared.Classes[0].Buckets[0].Samples[0].Win != 0.8 {
		t.Fatal("report mutated prepared sample order")
	}
	if first.Lower != 0 || first.Upper != 1 || first.UpperInclusive || first.SeedCount != 2 {
		t.Fatalf("first controlled bucket = %+v", first)
	}
	if first.DrawsAtCollisionProbability != 6 {
		t.Fatalf("first controlled bucket collision draws = %.0f, want 6", first.DrawsAtCollisionProbability)
	}
	last := report.Classes[0].Buckets[1]
	distributionTestClose(t, "single seed median", last.Median, 1.5)
	distributionTestClose(t, "single seed mean", last.Mean, 1.5)
	if !last.UpperInclusive || last.DrawsAtCollisionProbability != 5 {
		t.Fatalf("last controlled bucket = %+v", last)
	}
	uniform := report.Classes[1].Buckets[0]
	distributionTestClose(t, "odd median", uniform.Median, 1)
	distributionTestClose(t, "uniform mean", uniform.Mean, 5.0/3)
	if uniform.Lower != 0 || uniform.Upper != 4 || !uniform.UpperInclusive || uniform.DrawsAtCollisionProbability != 3 {
		t.Fatalf("uniform bucket = %+v", uniform)
	}

	compiled.Prepared.Plan.EngineOptions.DistributionCollisionProbability = 0.5
	changed, err := BuildBucketDistributionReport(compiled, mode)
	if err != nil {
		t.Fatal(err)
	}
	if changed.CollisionProbability != 0.5 || changed.Classes[0].Buckets[0].DrawsAtCollisionProbability != 8 {
		t.Fatalf("configured collision report = %+v", changed)
	}
	for ci := range report.Classes {
		for bi, original := range report.Classes[ci].Buckets {
			updated := changed.Classes[ci].Buckets[bi]
			updated.DrawsAtCollisionProbability = original.DrawsAtCollisionProbability
			if updated != original {
				t.Fatalf("report threshold changed bucket distribution: got %+v want %+v", updated, original)
			}
		}
	}
}

func TestBuildBucketDistributionReportEmptyBucket(t *testing.T) {
	compiled := CompiledModel{Prepared: PreparedProblem{Classes: []PreparedClass{{
		ID: "empty", Probability: 1, CollectRange: ClosedInterval{0, 1},
		Buckets: []PreparedBucket{{}},
	}}}}
	compiled.Prepared.Plan.EngineOptions = DefaultEngineOptions()
	report, err := BuildBucketDistributionReport(compiled, MaterializedMode{})
	if err != nil {
		t.Fatal(err)
	}
	bucket := report.Classes[0].Buckets[0]
	if bucket.Median != 0 || bucket.Mean != 0 || bucket.SeedProbability != 0 || bucket.DrawsAtCollisionProbability != 0 {
		t.Fatalf("empty bucket statistics = %+v", bucket)
	}
}

func distributionTestClose(t *testing.T, name string, actual, expected float64) {
	t.Helper()
	if math.Abs(actual-expected) > 1e-12 {
		t.Fatalf("%s = %.17g, want %.17g", name, actual, expected)
	}
}

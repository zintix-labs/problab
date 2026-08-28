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
	"fmt"
	"math"
)

const distributionCollisionProbability = 0.25

// BuildBucketDistributionReport summarizes the verified runtime distribution,
// not the pre-alias LP vector. This distinction matters for large alias tables:
// their reconstruction can differ by a tiny accepted numerical amount, and an
// operator report must describe what production will actually sample.
//
// Bucket probability is exposed in both coordinate systems used by the model:
// ConditionalProbability is within its Class, while UnconditionalProbability
// is the chance of selecting the Bucket in one complete game draw. Per-seed
// probabilities are unconditional marginals and therefore feed directly into
// the collision calculation.
func BuildBucketDistributionReport(compiled CompiledModel, mode MaterializedMode) (BucketDistributionReport, error) {
	if len(mode.Samples) != len(mode.EffectiveProbabilities) {
		return BucketDistributionReport{}, fmt.Errorf(
			"sample/effective probability length mismatch: samples=%d probabilities=%d",
			len(mode.Samples),
			len(mode.EffectiveProbabilities),
		)
	}

	report := BucketDistributionReport{
		BetMode:              mode.BetMode,
		CollisionProbability: distributionCollisionProbability,
		Classes:              make([]ClassDistributionReport, len(compiled.Prepared.Classes)),
	}
	classIndex := make(map[string]int, len(compiled.Prepared.Classes))
	concentrations := make([][]compensatedSum, len(compiled.Prepared.Classes))
	bucketTotals := make([][]compensatedSum, len(compiled.Prepared.Classes))
	for index, class := range compiled.Prepared.Classes {
		if _, duplicate := classIndex[class.ID]; duplicate {
			return BucketDistributionReport{}, fmt.Errorf("duplicate prepared class %q", class.ID)
		}
		classIndex[class.ID] = index
		report.Classes[index] = ClassDistributionReport{
			Class:       class.ID,
			Probability: class.Probability,
			Buckets:     make([]BucketProbabilityReport, len(class.Buckets)),
		}
		concentrations[index] = make([]compensatedSum, len(class.Buckets))
		bucketTotals[index] = make([]compensatedSum, len(class.Buckets))
		for bucketIndex, bucket := range class.Buckets {
			lower, upper, upperInclusive, err := reportBucketInterval(class, bucketIndex)
			if err != nil {
				return BucketDistributionReport{}, err
			}
			report.Classes[index].Buckets[bucketIndex] = BucketProbabilityReport{
				Index:          bucketIndex,
				Lower:          lower,
				Upper:          upper,
				UpperInclusive: upperInclusive,
				SeedCount:      len(bucket.Samples),
			}
		}
	}

	seedSeen := make([][]int, len(compiled.Prepared.Classes))
	for class := range seedSeen {
		seedSeen[class] = make([]int, len(compiled.Prepared.Classes[class].Buckets))
	}
	for sampleIndex, sample := range mode.Samples {
		class, exists := classIndex[sample.ClassID]
		if !exists {
			return BucketDistributionReport{}, fmt.Errorf("sample[%d] references unknown class %q", sampleIndex, sample.ClassID)
		}
		if sample.BucketIndex < 0 || sample.BucketIndex >= len(report.Classes[class].Buckets) {
			return BucketDistributionReport{}, fmt.Errorf(
				"sample[%d] class %q bucket index out of range: %d",
				sampleIndex,
				sample.ClassID,
				sample.BucketIndex,
			)
		}
		probability := mode.EffectiveProbabilities[sampleIndex]
		if !isFinite(probability) || probability < 0 {
			return BucketDistributionReport{}, fmt.Errorf("sample[%d] effective probability must be finite and nonnegative", sampleIndex)
		}
		bucket := &report.Classes[class].Buckets[sample.BucketIndex]
		bucketTotals[class][sample.BucketIndex].Add(probability)
		if seedSeen[class][sample.BucketIndex] == 0 || probability < bucket.SeedProbabilityMin {
			bucket.SeedProbabilityMin = probability
		}
		if seedSeen[class][sample.BucketIndex] == 0 || probability > bucket.SeedProbabilityMax {
			bucket.SeedProbabilityMax = probability
		}
		seedSeen[class][sample.BucketIndex]++
		concentrations[class][sample.BucketIndex].Add(probability * probability)
	}

	for classIndex, class := range compiled.Prepared.Classes {
		for bucketIndex := range class.Buckets {
			bucket := &report.Classes[classIndex].Buckets[bucketIndex]
			bucket.UnconditionalProbability = bucketTotals[classIndex][bucketIndex].Value()
			if seedSeen[classIndex][bucketIndex] != bucket.SeedCount {
				return BucketDistributionReport{}, fmt.Errorf(
					"class %q bucket %d seed count mismatch: materialized=%d prepared=%d",
					class.ID,
					bucketIndex,
					seedSeen[classIndex][bucketIndex],
					bucket.SeedCount,
				)
			}
			if class.Probability > 0 {
				bucket.ConditionalProbability = bucket.UnconditionalProbability / class.Probability
			}
			bucket.DrawsAtCollisionProbability = drawsAtCollisionProbability(
				concentrations[classIndex][bucketIndex].Value(),
				distributionCollisionProbability,
			)
		}
	}
	return report, nil
}

// reportBucketInterval preserves the classifier's exact endpoint semantics.
// Controlled atomic Buckets are half-open except for the final Bucket; an
// empirical-uniform Class has one inclusive configured collection interval.
func reportBucketInterval(class PreparedClass, bucketIndex int) (float64, float64, bool, error) {
	if !class.Intent {
		if len(class.Buckets) != 1 || bucketIndex != 0 {
			return 0, 0, false, fmt.Errorf("empirical-uniform class %q must contain exactly one bucket", class.ID)
		}
		return class.CollectRange.Lower(), class.CollectRange.Upper(), true, nil
	}
	boundaries := class.Design.Subjective.Buckets
	if bucketIndex < 0 || bucketIndex+1 >= len(boundaries) {
		return 0, 0, false, fmt.Errorf("class %q bucket %d has no configured interval", class.ID, bucketIndex)
	}
	return boundaries[bucketIndex], boundaries[bucketIndex+1], bucketIndex == len(class.Buckets)-1, nil
}

// drawsAtCollisionProbability inverts the same birthday/Poisson approximation
// used by risk caps. If q_j is the unconditional runtime probability of seed j,
// then P(any repeat by r draws) ~= 1-exp(-C(r,2)*sum(q_j^2)). The returned value
// is the first whole draw count whose approximation reaches target. A zero
// concentration means the Bucket is never selected and returns zero.
func drawsAtCollisionProbability(concentration, target float64) float64 {
	if !isFinite(concentration) || concentration <= 0 || !isFinite(target) || target <= 0 || target >= 1 {
		return 0
	}
	requiredPairs := -math.Log1p(-target) / concentration
	draws := (1 + math.Sqrt(1+8*requiredPairs)) / 2
	if !isFinite(draws) {
		return 0
	}
	return math.Ceil(draws)
}

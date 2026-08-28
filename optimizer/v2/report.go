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

// BuildIntentQualityReport measures the selected primary bucket vector against
// each semantic optimization stage without combining them into a synthetic
// score. UniformityRetentionReport remains a report-only description and the
// structured freedom report states exactly how much Main Group shape remains.
func BuildIntentQualityReport(compiled CompiledModel, solution EngineSolution) IntentQualityReport {
	report := IntentQualityReport{
		MainProfileOptimization:                 solution.MainProfileOptimization,
		OtherBucketVisibilityOptimization:       solution.OtherBucketVisibilityOptimization,
		MainGroupInternalVisibilityOptimization: solution.MainGroupInternalVisibilityOptimization,
		CanonicalBucketProbabilitySelection:     solution.CanonicalBucketProbabilitySelection,
		Classes:                                 make([]ClassIntentReport, 0, len(compiled.Prepared.Classes)),
	}
	report.PhaseA = report.MainProfileOptimization
	report.PhaseB = report.OtherBucketVisibilityOptimization
	primary, err := solutionPrimaryMasses(compiled, solution)
	if err != nil {
		primary = zeroPrimaryMasses(compiled.Prepared)
	}
	for classIndex, class := range compiled.Prepared.Classes {
		classReport := ClassIntentReport{Class: class.ID}
		if class.Intent {
			classReport = buildControlledClassIntentReport(
				class,
				primary[classIndex],
				solution.MainGroupInternalVisibilityOptimization,
				mainGroupReportTolerance(compiled.Prepared.Plan.EngineOptions.FeasibilityTolerance),
			)
		}
		report.Classes = append(report.Classes, classReport)
	}
	return report
}

// zeroPrimaryMasses creates a shape-safe zero matrix for reporting a malformed
// EngineSolution without panicking. Verification remains responsible for
// exposing the invalid dimensions; this fallback must not manufacture success.
func zeroPrimaryMasses(prepared PreparedProblem) [][]float64 {
	masses := make([][]float64, len(prepared.Classes))
	for classIndex, class := range prepared.Classes {
		masses[classIndex] = make([]float64, len(class.Buckets))
	}
	return masses
}

// buildControlledClassIntentReport computes Main and Other measurements for one
// intent:true Class. It uses only semantic group membership and primary masses;
// no auxiliary LP values or backend slack columns can influence the report.
func buildControlledClassIntentReport(class PreparedClass, masses []float64, optimization BisectionReport, tolerance float64) ClassIntentReport {
	report := ClassIntentReport{
		Class:                 class.ID,
		WantedMainProfile:     make([]float64, len(class.Groups)),
		ActualMainProfile:     make([]float64, len(class.Groups)),
		OtherVisibility:       OtherVisibilityReport{Applicable: false},
		MainRelativeDeviation: 0,
	}
	groupMasses := make([]float64, len(class.Groups))
	for groupIndex, group := range class.Groups {
		report.WantedMainProfile[groupIndex] = group.PreferShare
		for _, bucketIndex := range group.BucketIndexes {
			if bucketIndex >= 0 && bucketIndex < len(masses) && bucketIndex < len(class.Buckets) && class.Buckets[bucketIndex].Supported() {
				groupMasses[groupIndex] += masses[bucketIndex]
			}
		}
		report.MainTotal += groupMasses[groupIndex]
		visibility, freedom := buildMainGroupVisibilityReport(class, group, masses, optimization.FixedValue, tolerance)
		report.MainGroupVisibility = append(report.MainGroupVisibility, visibility)
		if freedom.Path != "" {
			report.RemainingDegreesOfFreedom = append(report.RemainingDegreesOfFreedom, freedom)
			if freedom.State == "unconstrained" {
				report.UnconstrainedDimensions = append(report.UnconstrainedDimensions, freedom.Path)
			}
		}
	}
	if report.MainTotal > 0 {
		for groupIndex := range groupMasses {
			report.ActualMainProfile[groupIndex] = groupMasses[groupIndex] / report.MainTotal
			report.MainRelativeDeviation += math.Abs(report.ActualMainProfile[groupIndex] - report.WantedMainProfile[groupIndex])
		}
	}
	// The L1 distance between two probability profiles is in [0, 2]. Mapping
	// it to [0, 1] keeps retention descriptive and avoids inventing a second
	// optimization objective: 1 is an exact profile match and 0 is disjoint.
	report.MainProfileRetention = clampUnit(1 - report.MainRelativeDeviation/2)
	report.OtherVisibility = buildOtherVisibilityReport(class, masses, report.MainTotal, tolerance)
	return report
}

func mainGroupReportTolerance(configured float64) float64 {
	if isFinite(configured) && configured > 0 {
		return configured
	}
	return DefaultFeasibilityTolerance
}

// buildMainGroupVisibilityReport preserves every configured bucket in its
// original order while limiting ratios and the retention denominator to replay-
// supported buckets.
func buildMainGroupVisibilityReport(class PreparedClass, group PreparedGroup, masses []float64, rho, tolerance float64) (MainGroupVisibilityReport, RemainingDegreeOfFreedomReport) {
	report := MainGroupVisibilityReport{
		GroupIndex: group.Index,
		Buckets:    make([]MainGroupBucketVisibilityReport, 0, len(group.BucketIndexes)),
	}
	minimum := math.Inf(1)
	for _, bucketIndex := range group.BucketIndexes {
		bucketReport := MainGroupBucketVisibilityReport{Index: bucketIndex}
		if bucketIndex >= 0 && bucketIndex < len(class.Buckets) && class.Buckets[bucketIndex].Supported() {
			bucketReport.Supported = true
			if bucketIndex < len(masses) {
				bucketReport.Mass = masses[bucketIndex]
			}
			report.SupportedCount++
			report.GroupTotal += bucketReport.Mass
		}
		report.Buckets = append(report.Buckets, bucketReport)
	}
	if report.SupportedCount < 2 {
		report.InapplicableReason = "fewer-than-two-supported-buckets"
		return report, RemainingDegreeOfFreedomReport{}
	}
	path := fmt.Sprintf("main_group[%d].internal_atomic_bucket_shape", group.Index)
	freedom := RemainingDegreeOfFreedomReport{Path: path, State: "unconstrained"}
	// A Main Group whose supported mass is numerically zero keeps fully free
	// internal shape: the visibility row p_i >= share*GroupTotal collapses to
	// p_i >= ~0 and constrains nothing, whatever the model-wide rho lock is.
	// Report it as unconstrained before the rho upgrade so the freedom state
	// never advertises a floor that is only vacuously satisfied; this matches
	// MainGroupVisibilityReport.Applicable staying false for the same group.
	if report.GroupTotal <= tolerance {
		report.InapplicableReason = "main-group-total-not-positive"
		return report, freedom
	}
	if rho > tolerance {
		freedom.State = "visibility-floor-only"
		freedom.Constraint = fmt.Sprintf("main_group_internal_visibility_rho=%.12g", rho)
	}
	report.Applicable = true
	report.PerfectUniformShare = 1 / float64(report.SupportedCount)
	for i := range report.Buckets {
		if !report.Buckets[i].Supported {
			continue
		}
		report.Buckets[i].RelativeShare = report.Buckets[i].Mass / report.GroupTotal
		minimum = math.Min(minimum, report.Buckets[i].RelativeShare)
	}
	report.MinimumShare = minimum
	report.Retention = clampUnit(float64(report.SupportedCount) * minimum)
	if 1-rho <= tolerance {
		freedom.State = "fully-equalized"
		freedom.Constraint = fmt.Sprintf("main_group_internal_visibility_rho=%.12g", rho)
	}
	return report, freedom
}

// buildOtherVisibilityReport separates normalized max-min Other retention
// from the report-only uniformity description. Ratios are deliberately omitted
// when there are no supported Others or OtherTotal is numerically zero.
func buildOtherVisibilityReport(class PreparedClass, masses []float64, mainTotal, tolerance float64) OtherVisibilityReport {
	otherTotal := 1 - mainTotal
	if len(class.Others) == 0 || !isFinite(otherTotal) || otherTotal <= tolerance {
		return OtherVisibilityReport{Applicable: false}
	}
	perfectShare := 1 / float64(len(class.Others))
	report := OtherVisibilityReport{
		Applicable:          true,
		OtherTotal:          otherTotal,
		PerfectUniformShare: perfectShare,
		Buckets:             make([]OtherBucketReport, 0, len(class.Others)),
	}
	minimumShare := math.Inf(1)
	uniformDistance := 0.0
	for _, bucketIndex := range class.Others {
		mass := 0.0
		if bucketIndex >= 0 && bucketIndex < len(masses) {
			mass = masses[bucketIndex]
		}
		relativeShare := mass / otherTotal
		minimumShare = math.Min(minimumShare, relativeShare)
		uniformDistance += math.Abs(relativeShare - perfectShare)
		bucketReport := OtherBucketReport{
			Index:         bucketIndex,
			Mass:          mass,
			RelativeShare: relativeShare,
		}
		if bucketIndex >= 0 && bucketIndex < len(class.Buckets) && isFinite(class.Buckets[bucketIndex].RiskCap) {
			bucketReport.RiskCap = class.Buckets[bucketIndex].RiskCap
		}
		report.Buckets = append(report.Buckets, bucketReport)
	}
	report.ClassRetention = clampUnit(float64(len(class.Others)) * minimumShare)
	report.UniformityRetentionReport = clampUnit(1 - uniformDistance/2)
	return report
}

// clampUnit removes tiny numerical excursions from dimensionless retention
// metrics whose mathematical range is [0,1]. It does not change bucket masses
// or any value used for verification and therefore cannot relax intent.
func clampUnit(value float64) float64 {
	if !isFinite(value) {
		return 0
	}
	return math.Max(0, math.Min(1, value))
}

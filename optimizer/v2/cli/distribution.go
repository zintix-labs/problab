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

package cli

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	optimizerv2 "github.com/zintix-labs/problab/optimizer/v2"
)

// writeModeDistributionCSVs persists the verified distribution of each
// generated mode as one CSV beneath directory, keeping the terminal clean while
// still giving Designers the actual runtime distribution. Both conditional and
// unconditional Bucket probabilities are emitted so a within-Class shape is
// never confused with a whole-game hit rate. Per-seed probability summarizes
// the uniform allocation within each Bucket; median and mean describe its
// empirical sample payout multipliers.
// It returns the paths it wrote, in mode order.
func writeModeDistributionCSVs(directory string, modes []optimizerv2.ModeRunReport) ([]string, error) {
	written := make([]string, 0, len(modes))
	for _, mode := range modes {
		report := mode.Distribution
		if len(report.Classes) == 0 {
			continue
		}
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return written, fmt.Errorf("create distribution output directory %q: %w", directory, err)
		}
		path := filepath.Join(directory, fmt.Sprintf("distribution_mode_%d.csv", report.BetMode))
		if report.Source == optimizerv2.DistributionSourcePointProbabilities {
			path = filepath.Join(directory, fmt.Sprintf("distribution_points_mode_%d.csv", report.BetMode))
		}
		if err := writeModeDistributionCSV(path, report); err != nil {
			return written, err
		}
		written = append(written, path)
	}
	return written, nil
}

func writeModeDistributionCSV(path string, report optimizerv2.BucketDistributionReport) (err error) {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create distribution file %q: %w", path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close distribution file %q: %w", path, closeErr)
		}
	}()

	writer := csv.NewWriter(file)
	_ = writer.Write([]string{
		"class",
		"bucket",
		"class_global_probability",
		"conditional_probability",
		"unconditional_probability",
		"median",
		"mean",
		"seed_count",
		"seed_probability",
		"collision_probability",
		"draws_at_collision_probability",
	})
	collision := strconv.FormatFloat(report.CollisionProbability, 'g', 6, 64)
	for _, class := range report.Classes {
		classProbability := strconv.FormatFloat(class.Probability, 'g', 9, 64)
		for _, bucket := range class.Buckets {
			_ = writer.Write([]string{
				class.Class,
				formatBucketInterval(bucket),
				classProbability,
				strconv.FormatFloat(bucket.ConditionalProbability, 'g', 9, 64),
				strconv.FormatFloat(bucket.UnconditionalProbability, 'g', 9, 64),
				strconv.FormatFloat(bucket.Median, 'g', 9, 64),
				strconv.FormatFloat(bucket.Mean, 'g', 9, 64),
				strconv.Itoa(bucket.SeedCount),
				strconv.FormatFloat(bucket.SeedProbability, 'e', 9, 64),
				collision,
				formatCollisionDraws(bucket.DrawsAtCollisionProbability),
			})
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("write distribution file %q: %w", path, err)
	}
	return nil
}

// formatBucketInterval mirrors the configured classifier: every controlled
// Bucket is [lower, upper) except the final one, while empirical-uniform Classes
// use one inclusive interval.
func formatBucketInterval(bucket optimizerv2.BucketProbabilityReport) string {
	closing := ")"
	if bucket.UpperInclusive {
		closing = "]"
	}
	return fmt.Sprintf("[%.9g, %.9g%s", bucket.Lower, bucket.Upper, closing)
}

func formatCollisionDraws(draws float64) string {
	if draws <= 0 {
		return "never"
	}
	return fmt.Sprintf("%.0f", draws)
}

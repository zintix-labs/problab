// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package v2

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// This harness delegates all solving to the checkout's engine. It does not
// contain a copy of either selection algorithm.
type minimumCVTraceSolver struct {
	t       *testing.T
	records []string
	counts  map[OptimizationStageID]int
	elapsed map[OptimizationStageID]time.Duration
}

func (s *minimumCVTraceSolver) Solve(ctx context.Context, p LinearProblem, o LinearObjective, opts SolveOptions) (SolveResult, error) {
	profile := os.Getenv("PROBLAB_MINCV_PROFILE") == "1"
	var before, after runtime.MemStats
	if profile {
		if s.counts[o.Name] == 0 || (s.counts[o.Name]+1)%50 == 0 {
			form, err := buildStandardForm(p, o, opts.FeasibilityTolerance)
			if err != nil {
				return SolveResult{}, err
			}
			_, rhs, _, columns := materializeStandardMatrix(form)
			s.t.Logf("INPUT stage=%s solve=%d semantic_rows=%d vars=%d standard_rows=%d columns=%d base_sha256=%x", o.Name, s.counts[o.Name]+1, len(p.Rows), len(p.Variables), len(rhs), columns, sha256.Sum256([]byte(fmt.Sprintf("%#v", p))))
		}
		runtime.ReadMemStats(&before)
	}
	start := time.Now()
	r, err := NewGonumSolver().Solve(ctx, p, o, opts)
	duration := time.Since(start)
	if profile {
		runtime.ReadMemStats(&after)
		s.t.Logf("SOLVE stage=%s solve=%d elapsed=%s allocated_bytes=%d allocations=%d", o.Name, s.counts[o.Name]+1, duration, after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs)
	}
	s.counts[o.Name]++
	s.elapsed[o.Name] += duration
	if o.Origin != ObjectiveCanonicalBucketProbability {
		b, _ := json.Marshal(struct {
			P       LinearProblem
			O       LinearObjective
			Options SolveOptions
			Values  []float64
		}{p, o, opts, r.Values})
		// LinearVariable contains infinite auxiliary bounds; JSON cannot encode
		// those. Use Go's deterministic slice/struct representation, not maps.
		if b == nil {
			b = []byte(fmt.Sprintf("%#v|%#v|%#v|%#v", p, o, opts, r.Values))
		}
		s.records = append(s.records, fmt.Sprintf("%x", sha256.Sum256(b)))
	}
	if s.counts[o.Name] == 1 || s.counts[o.Name]%50 == 0 {
		s.t.Logf("stage=%s solves=%d elapsed=%s rows=%d vars=%d", o.Name, s.counts[o.Name], s.elapsed[o.Name], len(p.Rows), len(p.Variables))
	}
	return r, err
}

func TestMinimumCVSelectionPreservesEarlierStages(t *testing.T) {
	mainClass := modelTestIntentClass(0, "main-siblings", 1, []float64{0, 1, 3}, []float64{0, 1, 9}, [][]int{{0, 1, 2}}, nil)
	mainClass.Design.Exp = 1
	mainClass.Design.Subjective.MainExperience.Probability = NumericRange{Min: 1, Max: 1}
	mainPrepared := modelTestPrepared(OverallIntent{CV: NumericRange{Min: 0, Max: 10}}, mainClass)
	mainPrepared.Plan.EngineOptions.MainGroupInternalVisibilityBisectionIterations = 40
	cases := []struct {
		name string
		p    PreparedProblem
	}{
		{"other", jointFairnessPreparedProblem()},
		{"main", mainPrepared},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			compiled := modelTestCompile(t, test.p)
			s := &minimumCVTraceSolver{t: t, counts: map[OptimizationStageID]int{}, elapsed: map[OptimizationStageID]time.Duration{}}
			r, err := NewIntentEngine(s).Solve(context.Background(), compiled)
			if err != nil || r.Status != StatusOptimal {
				t.Fatalf("%v %+v", err, r)
			}
			b, err := json.Marshal(s.records)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("first-four-trace=%x calls=%d primary=%v", sha256.Sum256(b), len(s.records), r.Primary)
			if dir := os.Getenv("PROBLAB_MINCV_CAPTURE"); dir != "" {
				if err := os.WriteFile(filepath.Join(dir, "first-four-"+test.name+".json"), b, 0600); err != nil {
					t.Fatal(err)
				}
				return
			}
			// Captured on the unmodified e05a4cd checkout, not regenerated from min-CV.
			want := map[string]map[string]string{
				"darwin/arm64": {"other": "875df0a9c2a561a8f2dd4015b50d5f65113d664e49788132c05d486d3f1a2b1c", "main": "89df49ec7b55f5bda346f963f0124fb8a02a58f7352d56a9a48a3df8577a33d3"},
				"linux/amd64":  {"other": "4b9a277748d85c333c41d3b2c11e4064327f7b1bb816d2532b9c08f7273fdb2b", "main": "89df49ec7b55f5bda346f963f0124fb8a02a58f7352d56a9a48a3df8577a33d3"},
			}[runtime.GOOS+"/"+runtime.GOARCH][test.name]
			if want == "" {
				t.Skipf("no pre-change trace for %s", runtime.GOARCH)
			}
			if fmt.Sprintf("%x", sha256.Sum256(b)) != want {
				t.Fatalf("first four stages changed: %x", sha256.Sum256(b))
			}
		})
	}
}

func minimumCV450BucketsFixture(t *testing.T) CompiledModel {
	t.Helper()
	classes := make([]PreparedClass, 100)
	for i := range classes {
		n := 4 + i%2
		means := make([]float64, n)
		seconds := make([]float64, n)
		for j := range means {
			means[j] = float64(j)
			seconds[j] = means[j] * means[j]
		}
		others := []int{3}
		if n == 5 {
			others = append(others, 4)
		}
		classes[i] = modelTestIntentClass(i, fmt.Sprintf("class-%03d", i), .01, means, seconds, [][]int{{0, 1}, {2}}, others)
		classes[i].Design.Exp = float64(n-1) / 2
		classes[i].Groups[0].PreferShare = 2.0 / 3
		classes[i].Groups[1].PreferShare = 1.0 / 3
	}
	return modelTestCompile(t, modelTestPrepared(OverallIntent{CV: NumericRange{Min: 0, Max: 10}}, classes...))
}

// The synthetic fixture uses +Inf for unconstrained RiskCap. Encode those
// fields as IEEE-754 bits in the fingerprint only; never alter the solver input.
func minimumCVFixtureDigest(p PreparedProblem) ([32]byte, error) {
	p.Classes = append([]PreparedClass(nil), p.Classes...)
	caps := make([][]uint64, len(p.Classes))
	for ci := range p.Classes {
		p.Classes[ci].Buckets = append([]PreparedBucket(nil), p.Classes[ci].Buckets...)
		caps[ci] = make([]uint64, len(p.Classes[ci].Buckets))
		for bi := range p.Classes[ci].Buckets {
			b := &p.Classes[ci].Buckets[bi]
			caps[ci][bi] = math.Float64bits(b.RiskCap)
			b.RiskCap = 0
		}
	}
	b, err := json.Marshal(struct {
		Prepared    PreparedProblem
		RiskCapBits [][]uint64
	}{p, caps})
	return sha256.Sum256(b), err
}

func TestMinimumCV450BucketsFixtureDigest(t *testing.T) {
	c := minimumCV450BucketsFixture(t)
	first, err := minimumCVFixtureDigest(c.Prepared)
	if err != nil {
		t.Fatal(err)
	}
	second, err := minimumCVFixtureDigest(minimumCV450BucketsFixture(t).Prepared)
	if err != nil || first != second || len(c.Primary) != 450 {
		t.Fatalf("unstable fixture digest: %x %x err=%v", first, second, err)
	}
	for _, class := range c.Prepared.Classes {
		for _, bucket := range class.Buckets {
			if !math.IsInf(bucket.RiskCap, 1) {
				t.Fatal("fingerprinting modified solver input")
			}
		}
	}
	c.Prepared.Classes[0].Buckets[0].RiskCap = 1
	changed, err := minimumCVFixtureDigest(c.Prepared)
	if err != nil || changed == first {
		t.Fatal("fingerprint lost RiskCap information")
	}
}

func TestMinimumCVSelection450BucketsProfile(t *testing.T) {
	if os.Getenv("PROBLAB_MINCV_PROFILE") != "1" {
		t.Skip("explicit 450-bucket cross-version profile")
	}
	compiled := minimumCV450BucketsFixture(t)
	fixture, err := minimumCVFixtureDigest(compiled.Prepared)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("fixture_sha256=%x primary=%d Go=%s platform=%s/%s", fixture, len(compiled.Primary), runtime.Version(), runtime.GOOS, runtime.GOARCH)
	s := &minimumCVTraceSolver{t: t, counts: map[OptimizationStageID]int{}, elapsed: map[OptimizationStageID]time.Duration{}}
	start := time.Now()
	r, err := NewIntentEngine(s).Solve(context.Background(), compiled)
	if err != nil || r.Status != StatusOptimal {
		t.Fatalf("%v status=%s diagnostics=%v", err, r.Status, r.Diagnostics)
	}
	for _, stage := range []OptimizationStageID{StageProveHardFeasibility, StageMinimizeMainProfileDeviation, StageMaximizeOtherBucketVisibility, StageMaximizeMainGroupInternalVisibility, StageSelectCanonicalBucketProbabilities} {
		t.Logf("RESULT stage=%s solves=%d elapsed=%s", stage, s.counts[stage], s.elapsed[stage])
	}
	t.Logf("total=%s", time.Since(start))
}

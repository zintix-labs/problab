// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package v2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

type minimumCVSolverFunc func(context.Context, LinearProblem, LinearObjective, SolveOptions) (SolveResult, error)

func (f minimumCVSolverFunc) Solve(c context.Context, p LinearProblem, o LinearObjective, s SolveOptions) (SolveResult, error) {
	return f(c, p, o, s)
}

func minimumCVToy(t *testing.T, n int, floor float64) CompiledModel {
	t.Helper()
	means := make([]float64, n)
	seconds := make([]float64, n)
	indexes := make([]int, n)
	for i := range means {
		means[i] = float64(i)
		indexes[i] = i
	}
	class := modelTestIntentClass(0, "toy", 1, means, seconds, [][]int{indexes}, nil)
	class.Design.Exp = float64(n-1) / 2
	class.Design.Median = ClosedInterval{0, float64(n)}
	for i := range class.Buckets {
		b, err := summarizeBucket(i, class.Design, 1, []CollectedSample{{Win: float64(i), Snapshot: []byte{byte(i + 1)}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if b.SecondMoment != b.Mean*b.Mean {
			t.Fatal("single-value bucket fixture")
		}
		b.MainGroup = 0
		class.Buckets[i] = b
	}
	compiled := modelTestCompile(t, modelTestPrepared(OverallIntent{CV: NumericRange{Min: 0, Max: 10}}, class))
	// Isolate final selection while retaining its real fixed-mean contract.
	rows := []LinearRow{}
	for _, r := range compiled.Hard.Rows {
		if r.Family == "normalization" || r.Family == "class_mean" {
			rows = append(rows, r)
		}
	}
	compiled.Hard.Rows = rows
	if floor > 0 {
		terms, _, err := buildSecondMomentExpression(compiled)
		if err != nil {
			t.Fatal(err)
		}
		if err := addRow(&compiled.Hard, LinearRow{ID: "cv-floor", Sense: SenseGE, RHS: floor, Terms: terms}); err != nil {
			t.Fatal(err)
		}
	}
	return compiled
}

func minimumCVSelect(t *testing.T, c CompiledModel) (SolveResult, CanonicalizationReport) {
	t.Helper()
	_, r, report, failure, err := NewIntentEngine(NewGonumSolver()).selectCanonicalBucketProbabilities(context.Background(), c, c.Hard, SolveResult{}, SolveOptions{FeasibilityTolerance: 1e-9, OptimalityTolerance: 1e-9}, nil)
	if err != nil || failure.Status.Valid() {
		t.Fatalf("selection: %v %+v", err, failure)
	}
	return r, report
}

func TestMinimumCVSelectionKnownOptimum(t *testing.T) {
	c := minimumCVToy(t, 3, 0)
	r, report := minimumCVSelect(t, c)
	if !reflect.DeepEqual(r.Values, []float64{0, 1, 0}) || report.Method != "min_cv" || report.Solves != 1 || !report.CVDefined || *report.CV != 0 || *report.SecondMoment != 1 {
		t.Fatalf("%+v %+v", r, report)
	}
}
func TestMinimumCVSelectionRespectsCVFloor(t *testing.T) {
	c := minimumCVToy(t, 3, 1.5)
	r, report := minimumCVSelect(t, c)
	for i, want := range []float64{.25, .5, .25} {
		assertNear(t, "probability", r.Values[i], want)
	}
	assertNear(t, "CV", *report.CV, math.Sqrt(.5))
}
func TestMinimumCVSelectionRespectsRefinementLocks(t *testing.T) {
	c := minimumCVToy(t, 3, 0)
	if err := addRow(&c.Hard, LinearRow{ID: "locked-visibility", Sense: SenseGE, RHS: .2, Terms: []LinearTerm{{Variable: c.Primary[0].ID, Coeff: 1}}}); err != nil {
		t.Fatal(err)
	}
	_, report := minimumCVSelect(t, c)
	assertNear(t, "second", *report.SecondMoment, 1.4)
}
func floatBits(values []float64) []uint64 {
	bits := make([]uint64, len(values))
	for i, v := range values {
		bits[i] = math.Float64bits(v)
	}
	return bits
}

func TestMinimumCVSelectionMultiOptimumRepeatable(t *testing.T) {
	c := minimumCVToy(t, 4, 3.5)
	terms, _, err := buildSecondMomentExpression(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range [][]float64{{.25, .25, .25, .25}, {.2, .4, .1, .3}} {
		if _, _, ok := replaySemanticSolution(c.Hard, p, 1e-9); !ok {
			t.Fatal("not a feasible optimum")
		}
		assertNear(t, "optimal second moment", evaluateTerms(terms, c.Hard.Variables, p), 3.5)
	}
	var bits []uint64
	var hash string
	for i := 0; i < 20; i++ {
		r, report := minimumCVSelect(t, c)
		assertNear(t, "oracle", *report.SecondMoment, 3.5)
		h, err := hashCanonicalJSON(r.Values)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			bits = floatBits(r.Values)
			hash = h
		} else if !reflect.DeepEqual(bits, floatBits(r.Values)) || hash != h {
			t.Fatal("multi-optimum selection changed")
		}
	}
}

func TestMinimumCVSelectionUsesOneSolve(t *testing.T) {
	c := minimumCVToy(t, 4, 3.5)
	before := cloneLinearProblem(c.Hard)
	calls := 0
	solver := minimumCVSolverFunc(func(ctx context.Context, p LinearProblem, o LinearObjective, opts SolveOptions) (SolveResult, error) {
		calls++
		terms, _, err := buildSecondMomentExpression(c)
		if err != nil {
			t.Fatal(err)
		}
		if o.Sense != Minimize || !reflect.DeepEqual(o.Terms, terms) || !reflect.DeepEqual(p, before) {
			t.Fatal("wrong selection model/objective")
		}
		return NewGonumSolver().Solve(ctx, p, o, opts)
	})
	p, _, report, failure, err := NewIntentEngine(solver).selectCanonicalBucketProbabilities(context.Background(), c, c.Hard, SolveResult{}, SolveOptions{FeasibilityTolerance: 1e-9, OptimalityTolerance: 1e-9}, nil)
	if err != nil || failure.Status.Valid() || calls != 1 || report.Solves != 1 || !reflect.DeepEqual(p, before) || !reflect.DeepEqual(c.Hard, before) {
		t.Fatalf("calls=%d failure=%+v err=%v", calls, failure, err)
	}
}

func TestMinimumCVStandardFormDeterministic(t *testing.T) {
	c := minimumCVToy(t, 4, 3.5)
	var baseline [3][]uint64
	for trial := 0; trial < 24; trial++ {
		// Permute map insertion in both the constraint and objective builders.
		m := map[VariableID]float64{}
		nm := map[VariableID]float64{}
		remaining := []int{0, 1, 2, 3}
		code := trial
		for len(remaining) > 0 {
			pick := code % len(remaining)
			code /= len(remaining)
			i := remaining[pick]
			remaining = append(remaining[:pick], remaining[pick+1:]...)
			id := c.Primary[i].ID
			m[id] = c.Prepared.Classes[0].Buckets[i].SecondMoment
			nm[id] = 1
		}
		p := cloneLinearProblem(c.Hard)
		p.Rows[0].Terms = sortedTerms(nm)
		terms := sortedTerms(m)
		shared, _, err := buildSecondMomentExpression(c)
		if err != nil || !reflect.DeepEqual(terms, shared) {
			t.Fatal("expression ordering changed")
		}
		form, err := buildStandardForm(p, LinearObjective{Sense: Minimize, Terms: terms}, 1e-9)
		if err != nil {
			t.Fatal(err)
		}
		data, rhs, obj, _ := materializeStandardMatrix(form)
		bits := [3][]uint64{floatBits(data), floatBits(rhs), floatBits(obj)}
		if trial == 0 {
			baseline = bits
		} else if !reflect.DeepEqual(bits, baseline) {
			t.Fatal("backend input bits changed")
		}
	}
}

func TestMinimumCVSelectionSubprocessRepeatable(t *testing.T) {
	if os.Getenv("PROBLAB_MINCV_CHILD") == "1" {
		c := minimumCVToy(t, 4, 3.5)
		r, _ := minimumCVSelect(t, c)
		pipeline := runCompleteProductionPipeline(t, Int64Seed(4127483647), "mincv-child")
		fmt.Printf("MINCV_RESULT %v %s\n", floatBits(r.Values), pipeline.Report.SolutionHash)
		return
	}
	var want string
	for i := 0; i < 3; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMinimumCVSelectionSubprocessRepeatable$", "-test.v")
		cmd.Env = append(os.Environ(), "PROBLAB_MINCV_CHILD=1")
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		got := ""
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "MINCV_RESULT ") {
				got = line
			}
		}
		if got == "" {
			t.Fatal("missing child result")
		}
		if i == 0 {
			want = got
		} else if got != want {
			t.Fatalf("%s != %s", got, want)
		}
	}
}

func TestMinimumCVRequiresFixedExpectation(t *testing.T) {
	mutations := []struct {
		name   string
		change func(*CompiledModel)
	}{
		{"mean-range", func(c *CompiledModel) { c.Hard.Rows[1].Sense = SenseGE }},
		{"missing-mean", func(c *CompiledModel) { c.Hard.Rows = c.Hard.Rows[:1] }},
		{"wrong-mean-coefficient", func(c *CompiledModel) { c.Hard.Rows[1].Terms[0].Coeff += .1 }},
		{"wrong-mean-rhs", func(c *CompiledModel) { c.Hard.Rows[1].RHS += .1 }},
		{"wrong-normalization", func(c *CompiledModel) { c.Hard.Rows[0].Terms[0].Coeff = 2 }},
		{"wrong-probability", func(c *CompiledModel) { c.Prepared.Classes[0].Probability = .9 }},
		{"variable-mean", func(c *CompiledModel) {
			c.Hard.Variables = append(c.Hard.Variables, LinearVariable{ID: "variable-mean", Upper: 2})
		}},
		{"wrong-primary", func(c *CompiledModel) { c.Primary[0].BucketIndex = 1 }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			c := minimumCVToy(t, 3, 0)
			test.change(&c)
			calls := 0
			solver := minimumCVSolverFunc(func(context.Context, LinearProblem, LinearObjective, SolveOptions) (SolveResult, error) {
				calls++
				return SolveResult{}, nil
			})
			_, _, _, failure, err := NewIntentEngine(solver).selectCanonicalBucketProbabilities(context.Background(), c, c.Hard, SolveResult{}, SolveOptions{FeasibilityTolerance: 1e-9, OptimalityTolerance: 1e-9}, nil)
			if err != nil || calls != 0 || failure.Status != StatusInternalError {
				t.Fatalf("calls=%d err=%v failure=%+v", calls, err, failure)
			}
		})
	}
	// A fixed empirical Class must agree with actual sample statistics.
	c := minimumCVToy(t, 3, 0)
	c.Prepared.Classes[0].Probability = .5
	c.Prepared.Classes = append(c.Prepared.Classes, modelTestFixedClass(1, "fixed", .5, 1, 1))
	if err := validateFixedExpectation(c, c.Hard, 1e-9); err == nil {
		t.Fatal("accepted empirical sample/declared mean mismatch")
	}
	// Changing only the final base must not be hidden by the original hard rows.
	c = minimumCVToy(t, 3, 0)
	base := cloneLinearProblem(c.Hard)
	base.Rows[1].RHS = 1.1
	if err := validateFixedExpectation(c, base, 1e-9); err == nil {
		t.Fatal("accepted modified base")
	}
}

func TestMinimumCVSelectionFailureAndCancellation(t *testing.T) {
	stop := errors.New("backend IO")
	tests := []struct {
		name   string
		result SolveResult
		err    error
	}{
		{"infeasible", SolveResult{Status: SolveInfeasible}, nil},
		{"unbounded", SolveResult{Status: SolveUnbounded}, nil},
		{"numerical", SolveResult{Status: SolveNumericalFailure}, nil},
		{"wrong-mean", SolveResult{Status: SolveOptimal, Values: []float64{1, 0, 0}}, nil},
		{"nan", SolveResult{Status: SolveOptimal, Values: []float64{0, math.NaN(), 0}}, nil},
		{"malformed", SolveResult{Status: SolveOptimal}, nil},
		{"error", SolveResult{}, stop},
		{"cancel", SolveResult{}, context.Canceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := minimumCVToy(t, 3, 0)
			calls := 0
			var events []OptimizationStageEvent
			solver := minimumCVSolverFunc(func(context.Context, LinearProblem, LinearObjective, SolveOptions) (SolveResult, error) {
				calls++
				return test.result, test.err
			})
			_, _, report, failure, err := NewIntentEngine(solver).selectCanonicalBucketProbabilities(context.Background(), c, c.Hard, SolveResult{Status: SolveOptimal, Values: []float64{0, 1, 0}}, SolveOptions{FeasibilityTolerance: 1e-9, OptimalityTolerance: 1e-9}, func(e OptimizationStageEvent) { events = append(events, e) })
			if calls != 1 || report.SecondMoment != nil || len(events) != 2 || events[1].State != "failed" {
				t.Fatalf("calls=%d events=%v report=%+v", calls, events, report)
			}
			if test.err != nil {
				if !errors.Is(err, test.err) {
					t.Fatalf("error %v", err)
				}
			} else if err != nil || !failure.Status.Valid() || failure.Status == StatusInfeasibleModel || failure.Status == StatusOptimal {
				t.Fatalf("failure=%+v err=%v", failure, err)
			}
		})
	}
}

func TestMinimumCVZeroAndFixedCases(t *testing.T) {
	// Zero mean with nonempty primary: exactly one zero-objective solve.
	c := minimumCVToy(t, 1, 0)
	_, r := minimumCVSelect(t, c)
	if r.Solves != 1 || r.CVDefined || r.CV != nil || *r.SecondMoment != 0 {
		t.Fatalf("%+v", r)
	}
	fixed := modelTestFixedClass(0, "fixed", 1, 0, 0)
	c = CompiledModel{Prepared: modelTestPrepared(OverallIntent{}, fixed)}
	_, r = minimumCVSelect(t, c)
	if r.Solves != 0 || r.CV != nil {
		t.Fatalf("%+v", r)
	}
	// Deliberately impossible second moment must not become abs(variance).
	c = minimumCVToy(t, 3, 0)
	for i := range c.Prepared.Classes[0].Buckets {
		c.Prepared.Classes[0].Buckets[i].SecondMoment = 0
	}
	_, _, _, failure, err := NewIntentEngine(NewGonumSolver()).selectCanonicalBucketProbabilities(context.Background(), c, c.Hard, SolveResult{}, SolveOptions{FeasibilityTolerance: 1e-9, OptimalityTolerance: 1e-9}, nil)
	if err != nil || !failure.Status.Valid() || failure.Status == StatusOptimal {
		t.Fatal("invalid variance accepted")
	}
}

func TestSecondMomentExpressionMatchesHardCVRows(t *testing.T) {
	controlled := modelTestIntentClass(0, "controlled", .5, []float64{1, 2}, []float64{1.25, 4}, [][]int{{0, 1}}, nil)
	controlled.Design.Exp = 1
	controlled.Buckets[0].Samples = []CollectedSample{{Win: .5, Snapshot: []byte{1}}, {Win: 1.5, Snapshot: []byte{2}}}
	fixed := modelTestFixedClass(1, "fixed", .5, 2, 4)
	fixed.Buckets[0].Samples[0].Win = 2
	c := modelTestCompile(t, modelTestPrepared(OverallIntent{CV: NumericRange{Min: 0, Max: 10}}, controlled, fixed))
	terms, constant, err := buildSecondMomentExpression(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []RowID{"global:cv:min", "global:cv:max"} {
		if !reflect.DeepEqual(terms, modelTestRow(t, c.Hard, id).Terms) {
			t.Fatal("different CV coefficients")
		}
	}
	if constant != 2 || terms[0].Coeff != .625 {
		t.Fatal("wrong second moment weighting")
	}
	r, report := minimumCVSelect(t, c)
	if r.Values[0] != 1 || *report.SecondMoment != 2.625 {
		t.Fatalf("%v %+v", r.Values, report)
	}
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "\"method\":\"min_cv\"") || !report.CVDefined {
		t.Fatal(string(b))
	}
	c.Prepared.Classes[0].Buckets[0].SecondMoment = math.Inf(1)
	if _, _, err := buildSecondMomentExpression(c); err == nil {
		t.Fatal("nonfinite coefficient accepted")
	}
}

func TestMinimumCVSelectionReport(t *testing.T) {
	c := minimumCVToy(t, 4, 3.5)
	result, report := minimumCVSelect(t, c)
	if report.Metric != "unconditional_second_moment" || report.Direction != "minimize" || report.PrimaryVariables != 4 {
		t.Fatalf("%+v", report)
	}
	var sum, mean, second float64
	for i, p := range result.Values {
		sum += p
		mean += p * float64(i)
		second += p * float64(i*i)
	}
	assertNear(t, "sum", sum, 1)
	assertNear(t, "mean", mean, c.Prepared.ExpectedRTP())
	assertNear(t, "second", *report.SecondMoment, second)
	assertNear(t, "CV", *report.CV, math.Sqrt(second-mean*mean)/mean)
	_, zero := minimumCVSelect(t, minimumCVToy(t, 1, 0))
	b, err := json.Marshal(zero)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "\"cv\":") || !strings.Contains(string(b), "\"cv_defined\":false") {
		t.Fatal(string(b))
	}
}

func TestMinimumCVVersionAndConfigCompatibility(t *testing.T) {
	if engineImplementationVersion != "intent-lp-v2.9.0" {
		t.Fatal(engineImplementationVersion)
	}
	if _, err := ParseConfig([]byte(validConfigYAML)); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ old, new string }{
		{"evaluator: none", "method: min_cv\n      evaluator: none"},
		{"evaluator: none", "evaluator: min_cv"},
		{"max_candidates: 1", "max_candidates: 2"},
	} {
		if _, err := ParseConfig([]byte(strings.Replace(validConfigYAML, entry.old, entry.new, 1))); err == nil {
			t.Fatalf("accepted %s", entry.new)
		}
	}
}

package v2

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

// Exercise the configured denominator through preparation, CV-coupled real
// solves, and reconstructed alias marginals with two nontrivial Classes.
func TestClassWeightBaseScaledIntentPreservesUnconditionalProbabilities(t *testing.T) {
	config, err := ParseConfig([]byte(validConfigYAML))
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	intent := MathIntent{Overall: OverallIntent{CV: NumericRange{
		Min: math.Sqrt(3/(1.3*1.3) - 1), Max: math.Sqrt(3/(1.3*1.3) - 1),
	}}}
	for i, weight := range []int{700000, 300000} {
		mean := float64(i + 1)
		intent.Classes = append(intent.Classes, ClassIntent{
			Name: fmt.Sprintf("class-%d", i), Weight: weight,
			Collect: CollectIntent{Samples: 3, WinRange: ClosedInterval{0, 2 * mean}},
			Design: ClassDesign{Exp: mean, Median: ClosedInterval{0, 2 * mean},
				Subjective: SubjectiveIntent{Intent: &enabled, Buckets: []float64{0, 0.5 * mean, 1.5 * mean, 2 * mean},
					MainExperience: &MainExperience{Groups: []ClosedInterval{{0, 2 * mean}},
						Probability: NumericRange{Min: 1, Max: 1}, Prefer: []float64{1}}},
			},
		})
	}
	var baseline []float64
	var baselineRows []LinearRow
	for _, base := range []ClassWeightDenominator{1000000, 1000000000} {
		scaled := cloneMathIntent(intent)
		scaled.Overall.ClassWeightBase = &base
		for i := range scaled.Classes {
			scaled.Classes[i].Weight *= int(base) / 1000000
		}
		config.Intents["high-win-v2"] = scaled
		plan, err := config.ResolvePlan("demo-high-win-v2")
		if err != nil {
			t.Fatal(err)
		}
		prepared := PreparedProblem{Plan: plan, BetMode: 0, BetUnit: 1}
		for i, classIntent := range plan.Intent.Classes {
			var samples []CollectedSample
			for j := 0; j < 3; j++ {
				samples = append(samples, CollectedSample{ClassID: classIntent.Name,
					Win: float64(j) * classIntent.Design.Exp, Snapshot: []byte{byte(i*3 + j)}, Sequence: uint64(j)})
			}
			class, diagnostic, err := prepareClass(plan, i, classIntent, samples)
			if err != nil || diagnostic.StopsRun() {
				t.Fatalf("prepare: %v %v", diagnostic, err)
			}
			prepared.Classes = append(prepared.Classes, class)
		}
		compiled, diagnostics, err := CompileHardModel(prepared)
		if err != nil || diagnostics.StopsRun() {
			t.Fatalf("compile: %v %v", diagnostics, err)
		}
		solution, err := NewIntentEngine(NewGonumSolver()).Solve(context.Background(), compiled)
		if err != nil || solution.Status != StatusOptimal {
			t.Fatalf("solve: %v %+v", err, solution.Diagnostics)
		}
		samples, err := ExpandSolution(compiled, solution, 0, 1)
		if err != nil {
			t.Fatal(err)
		}
		mode, err := materializeModeWithNormalizationTolerance(0, 1, samples, plan.EngineOptions.FeasibilityTolerance)
		if err != nil {
			t.Fatal(err)
		}
		if report := VerifyMaterialized(compiled, solution, mode); !report.Pass {
			t.Fatalf("verification: %+v", report)
		}
		if baseline == nil {
			baseline = mode.EffectiveProbabilities
			baselineRows = compiled.Hard.Rows
		} else {
			if !reflect.DeepEqual(baselineRows, compiled.Hard.Rows) {
				t.Fatal("rescaling changed hard rows including global CV")
			}
			if len(baseline) != len(mode.EffectiveProbabilities) {
				t.Fatal("sample count changed")
			}
			for i, p := range mode.EffectiveProbabilities {
				if math.Abs(p-baseline[i]) > artifactProbabilityTolerance {
					t.Fatalf("sample %d probability %.17g differs from %.17g", i, p, baseline[i])
				}
			}
		}
	}
}

func TestClassWeightBaseConfigBoundaries(t *testing.T) {
	for _, value := range []string{"100000.5", "1000000.0", "1e6", `"1000000"`, "true", "[]", "{}", "18446744073709551616"} {
		raw := strings.Replace(validConfigYAML, "overall:\n", "overall:\n      class_weight_base: "+value+"\n", 1)
		if _, err := ParseConfig([]byte(raw)); err == nil {
			t.Fatalf("accepted non-integer or overflowing base %s", value)
		}
	}
	for _, base := range []int{0, -1, 99999, 100000, 1000000, 100000000, 1000000000, 1000000001} {
		t.Run(fmt.Sprint(base), func(t *testing.T) {
			raw := strings.Replace(validConfigYAML, "overall:\n", fmt.Sprintf("overall:\n      class_weight_base: %d\n", base), 1)
			if base >= MinClassWeightBase && base <= MaxClassWeightBase {
				raw = strings.Replace(raw, "weight: 997000", fmt.Sprintf("weight: %d", base-base/1000*3), 1)
				raw = strings.Replace(raw, "weight: 3000", fmt.Sprintf("weight: %d", base/1000*3), 1)
			}
			config, err := ParseConfig([]byte(raw))
			if base < MinClassWeightBase || base > MaxClassWeightBase {
				if err == nil || !strings.Contains(err.Error(), ".overall.class_weight_base") {
					t.Fatalf("invalid base error=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			intent := config.Intents["high-win-v2"]
			if math.Abs(intent.ExpectedRTP()-0.96) > 1e-15 {
				t.Fatalf("RTP=%g", intent.ExpectedRTP())
			}
			intent.Classes[0].Weight++
			if err := validateMathIntent("intent", intent); err == nil {
				t.Fatal("accepted excess weight")
			}
			intent.Classes[0].Weight -= 2
			if err := validateMathIntent("intent", intent); err == nil {
				t.Fatal("accepted missing weight")
			}
			intent.Classes[0].Weight = int(^uint(0) >> 1)
			if err := validateMathIntent("intent", intent); err == nil {
				t.Fatal("accepted overflowing sum")
			}
		})
	}
}

func TestClassWeightBaseDefaultResolutionAndOwnership(t *testing.T) {
	legacy, err := ParseConfig([]byte(validConfigYAML))
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := ParseConfig([]byte(strings.Replace(validConfigYAML, "overall:\n", "overall:\n      class_weight_base: 1000000\n", 1)))
	if err != nil {
		t.Fatal(err)
	}
	a, err := legacy.ResolvePlan("demo-high-win-v2")
	if err != nil {
		t.Fatal(err)
	}
	b, err := explicit.ResolvePlan("demo-high-win-v2")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("omitted and explicit defaults differ")
	}
	ah, _ := hashCanonicalJSON(a)
	bh, _ := hashCanonicalJSON(b)
	if ah != bh {
		t.Fatal("default config hashes differ")
	}
	raw, err := json.Marshal(newRunReport(a, RunOverrides{}))
	if err != nil || !strings.Contains(string(raw), `"class_weight_base":1000000`) {
		t.Fatalf("missing effective base: %s (%v)", raw, err)
	}
	cloned := cloneConfig(explicit)
	*explicit.Intents["high-win-v2"].Overall.ClassWeightBase = 1000000000
	if *b.Intent.Overall.ClassWeightBase != DefaultClassWeightBase || *cloned.Intents["high-win-v2"].Overall.ClassWeightBase != DefaultClassWeightBase {
		t.Fatal("base pointer aliases config")
	}
	if legacy.Intents["high-win-v2"].Overall.ClassWeightBase != nil {
		t.Fatal("resolve mutated legacy config")
	}
}

func TestClassWeightBaseRareClassPreparationAndAlias(t *testing.T) {
	base := ClassWeightDenominator(MaxClassWeightBase)
	disabled := false
	intent := ClassIntent{Name: "rare", Weight: 1, Design: ClassDesign{
		Exp: 2, Median: ClosedInterval{2, 2}, Subjective: SubjectiveIntent{Intent: &disabled},
		Risk: &RiskIntent{Rounds: 100, Collision: CollisionIntent{Max: 0.25}},
	}}
	plan := ResolvedPlan{Intent: MathIntent{Overall: OverallIntent{ClassWeightBase: &base}}, EngineOptions: DefaultEngineOptions()}
	samples := []CollectedSample{{ClassID: "rare", Win: 2, Snapshot: []byte{1}}, {ClassID: "rare", Win: 2, Snapshot: []byte{2}, Sequence: 1}}
	class, diagnostic, err := prepareClass(plan, 0, intent, samples)
	if err != nil || diagnostic.StopsRun() {
		t.Fatalf("prepare=%v %v", diagnostic, err)
	}
	if class.Probability != 1e-9 {
		t.Fatalf("probability=%g", class.Probability)
	}
	wantCap := math.Sqrt(2*(-math.Log1p(-0.25))/(100*99/2)) / 1e-9
	if class.Buckets[0].RiskCap != wantCap {
		t.Fatalf("risk cap=%g want=%g", class.Buckets[0].RiskCap, wantCap)
	}
	mode, err := MaterializeMode(0, 1, []MaterializedSample{
		{ClassID: "rare", Win: 2, Snapshot: []byte{1}, Probability: 0.5e-9},
		{ClassID: "rare", Win: 2, Snapshot: []byte{2}, Probability: 0.5e-9},
		{ClassID: "other", Win: 0, Snapshot: []byte{3}, Probability: 1 - 1e-9},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range mode.EffectiveProbabilities[:2] {
		if p <= 0 || math.Abs(p/0.5e-9-1) > 1e-12 {
			t.Fatalf("rare probability lost: %.17g", p)
		}
	}
}

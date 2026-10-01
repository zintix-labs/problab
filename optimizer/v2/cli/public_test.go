// Copyright 2026 Zintix Labs
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

package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/dto"
	v2 "github.com/zintix-labs/problab/optimizer/v2"
	optimizercli "github.com/zintix-labs/problab/optimizer/v2/cli"
	"github.com/zintix-labs/problab/sdk/tag"
	"github.com/zintix-labs/problab/spec"
	"gopkg.in/yaml.v3"
)

func config(directory string) []byte {
	return []byte(fmt.Sprintf(`version: 2
plans:
  - id: cli_fixture
    target: {game: 1, bet_modes: [0]}
    engine: intent_lp_v2
    intent: fixture
    seed: 77
    collection: {workers: 1, batch_size: 64, max_spins: 64}
    candidate_selection: {evaluator: none, max_candidates: 1}
    output: {directory: %q, format: [rgs-collected]}
intents:
  fixture:
    overall: {cv: {min: 0, max: 100}}
    classes:
      - name: all
        weight: 1000000
        collect: {samples: 64, win_range: [0, 1000000000]}
        design:
          exp: 1
          median: [0, 1000000000]
          subjective: {intent: false}
engine_options:
  distribution_collision_probability: 0.3
  feasibility_tolerance: 1.0e-9
  optimality_tolerance: 1.0e-9
  quantile_epsilon: 1.0e-9
  profile_tolerance: 1.0e-8
  visibility_tolerance: 1.0e-8
  profile_bisection_iterations: 12
  other_visibility_bisection_iterations: 12
  main_group_internal_visibility_bisection_iterations: 12
`, directory))
}

func TestPublicRunConverterAndDirectCoreParity(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer lab.Close()
	var original []byte
	for _, tc := range []struct {
		name    string
		convert dto.ResultConverter
	}{
		{"nil", nil}, {"identity", dto.IdentityConverter}, {"wrapper", func(r dto.SpinResult) (json.RawMessage, error) { return dto.IdentityConverter(r) }},
		{"custom", func(dto.SpinResult) (json.RawMessage, error) { return json.RawMessage(`{"custom":true}`), nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			raw := config(dir)
			if code, err := optimizercli.Run(context.Background(), optimizercli.Options{Config: raw, Lab: lab, ResultConverter: tc.convert}); code != 0 || err != nil {
				t.Fatalf("code=%d err=%v", code, err)
			}
			read := func(pattern string) []byte {
				t.Helper()
				paths, _ := filepath.Glob(pattern)
				if len(paths) != 1 {
					t.Fatalf("paths=%v", paths)
				}
				f, err := os.Open(paths[0])
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				z, err := zstd.NewReader(f)
				if err != nil {
					t.Fatal(err)
				}
				defer z.Close()
				b, err := io.ReadAll(z)
				if err != nil {
					t.Fatal(err)
				}
				return b
			}
			got := read(filepath.Join(dir, "rgs/game_1/mode_0/export_*/collected/results.jsonl.zst"))
			if tc.name == "nil" {
				original = got
			} else if tc.name != "custom" && !bytes.Equal(original, got) {
				t.Fatal("identity output changed")
			}
			if tc.name == "custom" && !bytes.Equal(got, bytes.Repeat([]byte("{\"custom\":true}\n"), 64)) {
				t.Fatal("converter not applied")
			}
			coreDir := t.TempDir()
			cfg, err := v2.ParseConfig(config(coreDir))
			if err != nil {
				t.Fatal(err)
			}
			tuner, err := v2.NewTuner(cfg, lab, v2.WithResultConverter(tc.convert))
			if err != nil {
				t.Fatal(err)
			}
			result, err := tuner.Run(context.Background(), v2.RunRequest{PlanID: "cli_fixture"})
			if err != nil || !result.Succeeded() {
				t.Fatalf("%s %v", result.Status, err)
			}
			wantKind := "custom"
			if tc.name == "nil" || tc.name == "identity" {
				wantKind = "identity"
			}
			if result.Report.RGSExports[0].Converter != wantKind {
				t.Fatal("converter provenance")
			}
			if want := read(filepath.Join(coreDir, "rgs/game_1/mode_0/export_*/collected/results.jsonl.zst")); !bytes.Equal(got, want) {
				t.Fatal("CLI/direct core output differs")
			}
		})
	}
}

func TestPublicRunMissingTagsStopsBeforeCollection(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer lab.Close()
	dir := t.TempDir()
	raw := strings.Replace(string(config(dir)), "collect: {samples: 64, win_range: [0, 1000000000]}", "collect: {samples: 64, win_range: [0, 1000000000], tags: {matches: [bg]}}", 1)
	var out bytes.Buffer
	code, err := optimizercli.Run(context.Background(), optimizercli.Options{Config: []byte(raw), Lab: lab, Output: &out})
	if code != 2 || err != nil {
		t.Fatalf("%d %v", code, err)
	}
	for _, want := range []string{"cli_fixture", "game 1", "Options.CollectionTags", "INFEASIBLE_CONFIG"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("files=%v err=%v", files, err)
	}
}

func TestAllPlansTagPreflightAndCoreValidation(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer lab.Close()
	for _, mismatch := range []bool{false, true} {
		for _, tc := range []struct {
			name      string
			tags      map[spec.GID]map[string]tag.IsTag
			preflight bool
		}{
			{"nil", nil, true}, {"absent", map[spec.GID]map[string]tag.IsTag{}, true},
			{"nil inner", map[spec.GID]map[string]tag.IsTag{0: nil}, true}, {"empty inner", map[spec.GID]map[string]tag.IsTag{0: {}}, true},
			{"other game", map[spec.GID]map[string]tag.IsTag{1: {"needed": nil}}, true},
			{"unknown name", map[spec.GID]map[string]tag.IsTag{0: {"other": nil}}, false},
			{"nil predicate", map[spec.GID]map[string]tag.IsTag{0: {"needed": nil}}, false},
		} {
			t.Run(fmt.Sprintf("%t/%s", mismatch, tc.name), func(t *testing.T) {
				dir := t.TempDir()
				cfg, err := v2.ParseConfig(config(dir))
				if err != nil {
					t.Fatal(err)
				}
				intent := cfg.Intents["fixture"]
				intent.Classes = append([]v2.ClassIntent(nil), intent.Classes...)
				if mismatch {
					intent.Classes[0].Collect.Tags.Mismatches = []string{"needed"}
				} else {
					intent.Classes[0].Collect.Tags.Matches = []string{"needed"}
				}
				cfg.Intents["tagged"] = intent
				p := cfg.Plans[0]
				p.ID = "second"
				p.Intent = "tagged"
				p.Target.Game = 0
				cfg.Plans = append(cfg.Plans, p)
				raw, err := yaml.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				code, err := optimizercli.Run(context.Background(), optimizercli.Options{Config: raw, Lab: lab, Output: &out, CollectionTags: tc.tags})
				if code == 0 {
					t.Fatal("invalid tag setup accepted")
				}
				hint := strings.Contains(out.String(), "Options.CollectionTags")
				if hint != tc.preflight {
					t.Fatalf("preflight=%t code=%d err=%v output=%s", hint, code, err, out.String())
				}
				if tc.preflight {
					if code != 2 || err != nil || !strings.Contains(out.String(), "second") {
						t.Fatalf("%d %v %s", code, err, out.String())
					}
					files, err := os.ReadDir(dir)
					if err != nil || len(files) != 0 {
						t.Fatalf("first plan started: %v %v", files, err)
					}
				}
			})
		}
	}
}

func TestPublicRunCancellationDuringExport(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer lab.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	code, err := optimizercli.Run(ctx, optimizercli.Options{Config: config(t.TempDir()), Lab: lab, ResultConverter: func(r dto.SpinResult) (json.RawMessage, error) { calls++; cancel(); return dto.IdentityConverter(r) }})
	if code != 1 || !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("code=%d err=%v calls=%d", code, err, calls)
	}
	// Export cancellation must not close the borrowed runtime.
	code, err = optimizercli.Run(context.Background(), optimizercli.Options{Config: config(t.TempDir()), Lab: lab})
	if code != 0 || err != nil {
		t.Fatalf("reuse code=%d err=%v", code, err)
	}
}

func TestPublicNativeAndOptimizedParity(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer lab.Close()
	cfg, err := v2.ParseConfig(config(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := cfg.ResolvePlan("cli_fixture")
	if err != nil {
		t.Fatal(err)
	}
	collected, diagnostics, err := v2.NewCollector(lab).Collect(context.Background(), plan, 0)
	if err != nil || diagnostics.StopsRun() {
		t.Fatalf("collect: %v %v", err, diagnostics)
	}
	intent := cfg.Intents["fixture"]
	mean := 0.0
	for _, s := range collected.Classes[0].Samples {
		mean += s.Win
	}
	mean /= float64(len(collected.Classes[0].Samples))
	intent.Classes[0].Design.Exp = mean
	cfg.Intents["fixture"] = intent
	cfg.Plans[0].Output.Format = []v2.OutputFormat{v2.OutputOptimalGacha, v2.OutputOptimalArtifactV1, v2.OutputRGSOptimized}
	coreDir := t.TempDir()
	cfg.Plans[0].Output.Directory = coreDir
	tuner, err := v2.NewTuner(cfg, lab)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tuner.Run(context.Background(), v2.RunRequest{PlanID: "cli_fixture"})
	if err != nil || !result.Succeeded() {
		t.Fatalf("direct: %s %v %v", result.Status, err, result.Diagnostics)
	}
	cliDir := t.TempDir()
	cfg.Plans[0].Output.Directory = cliDir
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code, err := optimizercli.Run(context.Background(), optimizercli.Options{Config: raw, Lab: lab, Output: &out})
	if code != 0 || err != nil {
		t.Fatalf("CLI: %d %v %s", code, err, out.String())
	}
	compared := 0
	err = filepath.WalkDir(coreDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".bin") {
			return nil
		}
		rel, err := filepath.Rel(coreDir, path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(rel, "collected/") {
			return nil
		} // bank timestamp is run-local
		a, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(filepath.Join(cliDir, rel))
		if err != nil {
			return err
		}
		if !bytes.Equal(a, b) {
			t.Fatalf("native bytes changed: %s", rel)
		}
		compared++
		return nil
	})
	if err != nil || compared == 0 {
		t.Fatalf("compared=%d err=%v", compared, err)
	}
	if _, err := os.Stat(filepath.Join(cliDir, "distribution_mode_0.csv")); err != nil {
		t.Fatal(err)
	}
	// A successful optimized-only Run with text disabled still writes point CSV.
	cfg.Plans[0].Output.Format = []v2.OutputFormat{v2.OutputRGSOptimized}
	cfg.Plans[0].Output.Directory = t.TempDir()
	raw, err = yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if code, err := optimizercli.Run(context.Background(), optimizercli.Options{Config: raw, Lab: lab}); code != 0 || err != nil {
		t.Fatalf("%d %v", code, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Plans[0].Output.Directory, "distribution_points_mode_0.csv")); err != nil {
		t.Fatal(err)
	}
}

func TestPublicRunLifecycleAndInputs(t *testing.T) {
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer lab.Close()
	var out bytes.Buffer
	opts := optimizercli.Options{Lab: lab, Output: &out, Config: config(t.TempDir())}
	if _, err := v2.ParseConfig(opts.Config); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct {
		ctx  context.Context
		opts optimizercli.Options
	}{{nil, opts}, {context.Background(), optimizercli.Options{}}} {
		if code, err := optimizercli.Run(bad.ctx, bad.opts); code != 1 || err == nil {
			t.Fatalf("code=%d err=%v", code, err)
		}
	}
	for _, raw := range []string{"", "version: [", "version: 2\nunknown: true"} {
		opts.Config = []byte(raw)
		if code, err := optimizercli.Run(context.Background(), opts); code != 2 || err != nil {
			t.Fatalf("code=%d err=%v output=%s", code, err, out.String())
		}
	}
	if !strings.Contains(out.String(), "provided configuration") {
		t.Fatal("missing default source")
	}
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		want := context.Canceled
		if deadline {
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			want = context.DeadlineExceeded
		} else {
			cancel()
		}
		code, err := optimizercli.Run(ctx, opts)
		cancel()
		if code != 1 || !errors.Is(err, want) {
			t.Fatalf("code=%d err=%v", code, err)
		}
	}
	// Reusing the same Lab after errors/cancellation and successful calls proves
	// ownership is retained by the caller. Nil output must still publish files.
	for i := 0; i < 2; i++ {
		dir := t.TempDir()
		opts.Config = config(dir)
		opts.Output = nil
		if i == 1 {
			var f *os.File
			opts.Output = f
		}
		if code, err := optimizercli.Run(context.Background(), opts); code != 0 || err != nil {
			t.Fatalf("code=%d err=%v", code, err)
		}
		files, err := filepath.Glob(filepath.Join(dir, "rgs/game_1/mode_0/export_*/collected/results.jsonl.zst"))
		if err != nil || len(files) != 1 {
			t.Fatalf("files=%v err=%v", files, err)
		}
		if files, _ := filepath.Glob(filepath.Join(dir, "distribution*.csv")); len(files) != 0 {
			t.Fatal("collected-only CSV", files)
		}
	}
}

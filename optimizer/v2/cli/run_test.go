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

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	v2 "github.com/zintix-labs/problab/optimizer/v2"
	"github.com/zintix-labs/problab/sdk/tag"
	"github.com/zintix-labs/problab/spec"
)

func TestPlanExitPrecedenceAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		statuses []v2.Status
		fail     bool
		cancel   bool
		want     int
	}{
		{"success", []v2.Status{v2.StatusExported, v2.StatusExported}, false, false, 0},
		{"failure then success", []v2.Status{v2.StatusInfeasibleConfig, v2.StatusExported}, false, false, 2},
		{"success then failure", []v2.Status{v2.StatusExported, v2.StatusInfeasibleConfig}, false, false, 2},
		{"operational", []v2.Status{v2.StatusInfeasibleConfig, v2.StatusExported, v2.StatusExported}, true, false, 1},
		{"cancel", []v2.Status{v2.StatusInfeasibleConfig, v2.StatusExported}, false, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg := v2.Config{Plans: make([]v2.RunPlan, len(tc.statuses))}
			calls := 0
			boom := errors.New("I/O failure")
			code, err := runPlans(ctx, cfg, io.Discard, func(context.Context, v2.RunRequest) (v2.RunResult, error) {
				i := calls
				calls++
				if tc.cancel {
					cancel()
				}
				if tc.fail && i == 1 {
					return v2.RunResult{}, boom
				}
				return v2.RunResult{Status: tc.statuses[i]}, nil
			})
			if code != tc.want {
				t.Fatalf("code=%d err=%v", code, err)
			}
			if tc.fail && (!errors.Is(err, boom) || calls != 2) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if tc.cancel && (!errors.Is(err, context.Canceled) || calls != 1) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestDistributionOutputIndependentOfTextAndWarningNonfatal(t *testing.T) {
	dir := t.TempDir()
	cfg := v2.Config{Plans: []v2.RunPlan{{Output: v2.OutputOptions{Directory: dir}}}}
	result := v2.RunResult{Status: v2.StatusOptimal, Report: v2.RunReport{Modes: []v2.ModeRunReport{{Distribution: v2.BucketDistributionReport{Classes: []v2.ClassDistributionReport{{Class: "test"}}}}}}}
	run := func(context.Context, v2.RunRequest) (v2.RunResult, error) { return result, nil }
	if code, err := runPlans(context.Background(), cfg, io.Discard, run); code != 0 || err != nil {
		t.Fatalf("%d %v", code, err)
	}
	path := filepath.Join(dir, "distribution_mode_0.csv")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	cfg.Plans[0].Output.Directory = path // a file, not a directory
	var out bytes.Buffer
	if code, err := runPlans(context.Background(), cfg, &out, run); code != 0 || err != nil {
		t.Fatalf("%d %v", code, err)
	}
	if !strings.Contains(out.String(), "[warn] distribution report:") {
		t.Fatal(out.String())
	}
}

func TestTagInjectionChecksOnlyUsedIntentAndPresence(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		c := v2.ClassIntent{}
		if mismatch {
			c.Collect.Tags.Mismatches = []string{"needed"}
		} else {
			c.Collect.Tags.Matches = []string{"needed"}
		}
		cfg := v2.Config{Plans: []v2.RunPlan{{ID: "second", Intent: "used", Target: v2.Target{Game: 1}}}, Intents: map[string]v2.MathIntent{"used": {Classes: []v2.ClassIntent{c}}}}
		for _, tags := range []map[spec.GID]map[string]tag.IsTag{nil, {}, {1: nil}, {1: {}}, {2: {"other": nil}}} {
			if err := validateTagInjection(cfg, tags); err == nil {
				t.Fatal("missing catalog accepted")
			}
		}
		if err := validateTagInjection(cfg, map[spec.GID]map[string]tag.IsTag{1: {"other": nil}}); err != nil {
			t.Fatal("CLI must delegate name/predicate validation", err)
		}
		cfg.Plans[0].Intent = "unused"
		if err := validateTagInjection(cfg, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReporterConcurrentAndNonTTY(t *testing.T) {
	var absent *cliProgressReporter
	absent.Report(v2.StageEvent{})
	if newCLIProgressReporter(struct{ io.Writer }{os.Stderr}).interactive {
		t.Fatal("wrapped writer considered TTY")
	}
	var out bytes.Buffer
	r := newCLIProgressReporter(&out)
	if r.interactive {
		t.Fatal("buffer considered TTY")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Report(v2.StageEvent{Stage: "load-config", State: "completed", BetMode: -1})
		}()
	}
	wg.Wait()
	for _, path := range []string{os.DevNull, t.TempDir() + "/output"} {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if newCLIProgressReporter(f).interactive {
			t.Fatal("non-terminal considered TTY")
		}
		f.Close()
	}
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	defer wr.Close()
	if newCLIProgressReporter(wr).interactive {
		t.Fatal("pipe considered TTY")
	}
}

func TestReporterRealTTY(t *testing.T) {
	f, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		if !newCLIProgressReporter(os.Stdout).interactive {
			t.Skip("no terminal in this environment")
		}
		return
	}
	defer f.Close()
	if !newCLIProgressReporter(f).interactive {
		t.Fatal("controlling terminal not recognized")
	}
}

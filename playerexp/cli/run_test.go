// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/demo/demo_strategies"
	"github.com/zintix-labs/problab/playerexp"
	"github.com/zintix-labs/problab/sdk/buf"
)

func TestProgressTerminalAndMilestones(t *testing.T) {
	for _, tty := range []bool{false, true} {
		t.Run(map[bool]string{false: "lines", true: "tty"}[tty], func(t *testing.T) {
			var b bytes.Buffer
			p := newProgress(&b)
			if p.interactive {
				t.Fatal("arbitrary writer assumed terminal")
			}
			p.interactive = tty
			now := time.Unix(1, 0)
			p.now = func() time.Time { return now }
			emit := func(kind string, n int) {
				p.Report(playerexp.ProgressEvent{Kind: kind, GroupName: "long name", Completed: n, Total: 100})
			}
			emit("start", 0)
			emit("progress", 1)
			now = now.Add(100 * time.Millisecond)
			emit("progress", 37)
			emit("progress", 38)
			emit("failed", 38)
			s := b.String()
			if !strings.Contains(s, "37/100") || !strings.Contains(s, "failed 38/100") || strings.Contains(s, "100/100") {
				t.Fatal(s)
			}
			if strings.Count(s, "players") != 3 {
				t.Fatalf("throttle %q", s)
			}
			if tty {
				if strings.Count(s, "\r\x1b[2K") != 3 || !strings.HasSuffix(s, "\n") {
					t.Fatal(s)
				}
			} else if strings.ContainsAny(s, "\r\x1b") {
				t.Fatal(s)
			}
			b.Reset()
			p.Report(playerexp.ProgressEvent{Kind: "start", GroupName: "x", Total: 1})
			p.Report(playerexp.ProgressEvent{Kind: "completed", GroupName: "x", Completed: 1, Total: 1})
			if !strings.Contains(b.String(), "completed 1/1") {
				t.Fatal(b.String())
			}
		})
	}
	p := newProgress(nil)
	p.Report(playerexp.ProgressEvent{Kind: "start"})
	f, e := os.CreateTemp(t.TempDir(), "redirect")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = f.Close() }()
	if newProgress(f).interactive {
		t.Fatal("ordinary file detected as terminal")
	}
}

type cancelFactory struct {
	created int
	cancel  context.CancelFunc
}

func (f *cancelFactory) NewStrategy(int) playerexp.Strategy {
	f.created++
	return &cancelStrategy{cancel: f.cancel, second: f.created == 2}
}

type cancelStrategy struct {
	cancel context.CancelFunc
	second bool
}

func (s *cancelStrategy) FirstSpin(playerexp.PlayerState) playerexp.Decision {
	return playerexp.Decision{KeepPlay: true, Bet: playerexp.Bet{BetMode: 0, BetMult: 1}}
}
func (s *cancelStrategy) Next(playerexp.PlayerState, *buf.SpinResult) playerexp.Decision {
	if s.second {
		s.cancel()
	}
	return playerexp.Decision{}
}
func TestCLIPartialCancellationPublishesActualCounts(t *testing.T) {
	lab, e := demo.NewProbLab()
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = lab.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := playerexp.NewStrategyRegistry()
	if e = r.Register(0, "monotone", &cancelFactory{cancel: cancel}); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	var output bytes.Buffer
	code, e := Run(ctx, Options{Config: []byte(config), Lab: lab, Strategies: r, OutputDir: root, Output: &output})
	if code != 1 || !errors.Is(e, context.Canceled) || !strings.Contains(output.String(), "canceled 1/3") {
		t.Fatal(code, e, output.String())
	}
	files, _ := filepath.Glob(filepath.Join(root, "run-*", "summary.csv"))
	if len(files) != 1 {
		t.Fatal(files)
	}
	data, e := os.ReadFile(files[0])
	if e != nil {
		t.Fatal(e)
	}
	for _, value := range []string{"CANCELED,group,started_players,2", "CANCELED,group,completed_players,1", "CANCELED,group,incomplete_players,1", "CANCELED,group,not_started_players,1"} {
		if !strings.Contains(string(data), value) {
			t.Fatal("missing", value)
		}
	}
}

const config = `game_id: 0
seed: 77
analysis:
  - name: short
    player: {players: 3, init_bet: 300, strategy: monotone}
    win_buckets: [0, 1, 2]
`

type brokenWriter struct{ err error }

func (w brokenWriter) Write([]byte) (int, error) { return 0, w.err }
func TestCLINilOutputPartialAndErrors(t *testing.T) {
	lab, e := demo.NewProbLab()
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = lab.Close() }()
	r, e := demo_strategies.Catalog.BuildRegistry()
	if e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"success", "canceled", "io", "combined"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			o := Options{Config: []byte(config), Lab: lab, Strategies: r, OutputDir: root}
			if mode == "canceled" || mode == "combined" {
				cancel()
			}
			sentinel := errors.New("output failure")
			if mode == "io" || mode == "combined" {
				o.Output = brokenWriter{sentinel}
			}
			code, e := Run(ctx, o)
			if mode == "success" {
				if e != nil || code != 0 {
					t.Fatal(code, e)
				}
			} else if e == nil || code != 1 {
				t.Fatal(code, e)
			}
			if (mode == "canceled" || mode == "combined") && !errors.Is(e, context.Canceled) {
				t.Fatal(e)
			}
			if (mode == "io" || mode == "combined") && !errors.Is(e, sentinel) {
				t.Fatal(e)
			}
			files, _ := filepath.Glob(filepath.Join(root, "run-*", "*.csv"))
			if len(files) != 5 {
				t.Fatal(files)
			}
			jsonFiles, err := filepath.Glob(filepath.Join(root, "run-*", "report.json"))
			if err != nil || len(jsonFiles) != 1 {
				t.Fatal(jsonFiles, err)
			}
			data, e := os.ReadFile(filepath.Join(filepath.Dir(files[0]), "summary.csv"))
			if e != nil {
				t.Fatal(e)
			}
			if mode == "io" && !strings.Contains(string(data), "COMPLETED") {
				t.Fatal("text failure changed player status")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A file cannot act as an output directory. Preserve cancellation plus I/O.
	file := filepath.Join(t.TempDir(), "file")
	if e = os.WriteFile(file, nil, 0600); e != nil {
		t.Fatal(e)
	}
	code, e := Run(ctx, Options{Config: []byte(config), Lab: lab, Strategies: r, OutputDir: file})
	var pe *os.PathError
	if code != 1 || !errors.Is(e, context.Canceled) || !errors.As(e, &pe) {
		t.Fatal(code, e)
	}
}

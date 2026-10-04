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

// Package cli composes playerexp with terminal progress and CSV publication.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/playerexp"
	"github.com/zintix-labs/problab/sdk/tag"
	"github.com/zintix-labs/problab/spec"
)

// Options exposes only application composition. Output=nil suppresses text,
// not CSV. Empty OutputDir selects build/exp. Lab remains caller-owned.
type Options struct {
	Config       []byte
	ConfigSource string
	Lab          *problab.Problab
	Strategies   *playerexp.StrategyRegistry
	Tags         map[spec.GID]map[string]tag.IsTag
	Output       io.Writer
	OutputDir    string
}

// Run borrows Lab; the command owns signals and Lab.Close. Nil Output suppresses
// text only. Canceled/failed runs still publish explicitly partial CSV reports.
func Run(ctx context.Context, o Options) (int, error) {
	c, err := playerexp.ParseConfig(o.Config)
	if err != nil {
		return 1, fmt.Errorf("%s: %w", o.ConfigSource, err)
	}
	p := newProgress(o.Output)
	r, runErr := playerexp.Run(ctx, c, playerexp.Options{Lab: o.Lab, Strategies: o.Strategies, Tags: o.Tags, Reporter: p})
	path, writeErr := playerexp.WriteCSV(o.OutputDir, r)
	if writeErr == nil && o.Output != nil {
		_, e := fmt.Fprintf(o.Output, "Player experience %s; CSV + report.json: %s\n", r.Status, path)
		p.err = errors.Join(p.err, e)
	}
	err = errors.Join(runErr, writeErr, p.err)
	if err != nil {
		return 1, err
	}
	return 0, nil
}

type progress struct {
	output      io.Writer
	interactive bool
	now         func() time.Time
	last        time.Time
	milestone   int
	err         error
}

func newProgress(w io.Writer) *progress {
	p := &progress{output: w, now: time.Now}
	if f, ok := w.(*os.File); ok && f != nil {
		p.interactive = isatty.IsTerminal(f.Fd())
	}
	return p
}
func (p *progress) Report(e playerexp.ProgressEvent) {
	if p.output == nil || p.err != nil {
		return
	}
	if e.Kind == "start" {
		p.last = time.Time{}
		p.milestone = 0
	}
	percent := 0.0
	if e.Total > 0 {
		percent = 100 * float64(e.Completed) / float64(e.Total)
	}
	now := p.now()
	milestone := int(percent) / 10
	if e.Kind == "progress" {
		if p.interactive {
			if !p.last.IsZero() && now.Sub(p.last) < 100*time.Millisecond {
				return
			}
		} else {
			if milestone <= p.milestone {
				return
			}
		}
	}
	p.last = now
	p.milestone = milestone
	prefix, suffix := "", "\n"
	if p.interactive {
		prefix = "\r\x1b[2K"
		if e.Kind == "progress" || e.Kind == "start" {
			suffix = ""
		}
	}
	_, p.err = fmt.Fprintf(p.output, "%s[%s] %s %d/%d players (%.2f%%)%s", prefix, e.GroupName, e.Kind, e.Completed, e.Total, percent, suffix)
}

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

package playerexp

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var csvNames = []string{"summary.csv", "win_buckets.csv", "player_rtp.csv", "events.csv", "exits.csv"}
var csvHeaders = [][]string{
	{"section", "key", "value"},
	{"bucket_index", "min", "max", "include_min", "include_max", "count", "denominator", "probability", "observed_max"},
	{"metric", "parameter", "n", "count", "estimate", "estimate_defined", "ci_low", "ci_high", "ci_available", "ci_reason", "ci_method", "confidence"},
	{"event_index", "event_name", "min", "max", "include_min", "include_max", "frequency", "count", "denominator", "probability", "hit_count", "ci_low", "ci_high", "ci_available", "ci_reason", "ci_method", "confidence"},
	{"reason", "is_subreason", "count", "denominator", "probability", "ci_low", "ci_high", "ci_available", "ci_reason", "ci_method", "confidence"},
}

func f64(v float64) string  { return strconv.FormatFloat(v, 'g', -1, 64) }
func u64(v uint64) string   { return strconv.FormatUint(v, 10) }
func integer(v int) string  { return strconv.Itoa(v) }
func boolean(v bool) string { return strconv.FormatBool(v) }
func upper(v *float64) string {
	if v == nil {
		return ""
	}
	return f64(*v)
}
func estimate(e Estimate) string {
	if !e.Defined {
		return ""
	}
	return f64(e.Value)
}
func ratio(k, n uint64) string {
	if n == 0 {
		return ""
	}
	return f64(float64(k) / float64(n))
}
func interval(ci Interval, confidence string) []string {
	lo, hi := "", ""
	if ci.Available {
		lo = f64(ci.Lo)
		hi = f64(ci.Hi)
	}
	return []string{lo, hi, boolean(ci.Available), ci.Reason, ci.Method, confidence}
}

// WriteCSV publishes all five tables and report.json in a new run directory. Failed
// or canceled reports can be published; caller cancellation does not discard
// already committed players. This function never overwrites a previous run.
func WriteCSV(root string, r Report) (string, error) { return writeCSV(root, r, osCSVFS{}) }

type csvFS interface {
	MkdirAll(string, os.FileMode) error
	MkdirTemp(string, string) (string, error)
	Create(string) (io.WriteCloser, error)
	Rename(string, string) error
	RemoveAll(string) error
}
type osCSVFS struct{}

func (osCSVFS) MkdirAll(p string, m os.FileMode) error { return os.MkdirAll(p, m) }
func (osCSVFS) MkdirTemp(p, s string) (string, error)  { return os.MkdirTemp(p, s) }
func (osCSVFS) Create(p string) (io.WriteCloser, error) {
	return os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
}
func (osCSVFS) Rename(a, b string) error {
	if _, err := os.Lstat(b); !os.IsNotExist(err) {
		return fmt.Errorf("publication destination exists or cannot be checked: %s", b)
	}
	return os.Rename(a, b)
}
func (osCSVFS) RemoveAll(p string) error { return os.RemoveAll(p) }
func writeCSV(root string, r Report, fs csvFS) (path string, err error) {
	if root == "" {
		root = "build/exp"
	}
	if err = fs.MkdirAll(root, 0755); err != nil {
		return "", err
	}
	stage, err := fs.MkdirTemp(root, ".run-staging-")
	if err != nil {
		return "", err
	}
	defer func() {
		if stage != "" {
			err = errors.Join(err, fs.RemoveAll(stage))
		}
	}()
	for i, name := range csvNames {
		file, e := fs.Create(filepath.Join(stage, name))
		if e != nil {
			return "", e
		}
		e = writeTable(file, r, i)
		e = errors.Join(e, file.Close())
		if e != nil {
			return "", fmt.Errorf("write %s: %w", name, e)
		}
	}
	file, e := fs.Create(filepath.Join(stage, "report.json"))
	if e != nil {
		return "", fmt.Errorf("create report.json: %w", e)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	e = encoder.Encode(JSONReport{SchemaVersion: "playerexp-report-v1", Report: r})
	e = errors.Join(e, file.Close())
	if e != nil {
		return "", fmt.Errorf("write report.json: %w", e)
	}
	path = filepath.Join(root, "run-"+strings.TrimPrefix(filepath.Base(stage), ".run-staging-"))
	if err = fs.Rename(stage, path); err != nil {
		return "", err
	}
	stage = ""
	return path, nil
}

func writeTable(w io.Writer, r Report, table int) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(append([]string{"group_index", "group_name", "group_status"}, csvHeaders[table]...)); err != nil {
		return err
	}
	var writeErr error
	row := func(prefix []string, cols ...string) {
		if writeErr == nil {
			writeErr = cw.Write(append(append([]string(nil), prefix...), cols...))
		}
	}
	if table == 0 {
		p := []string{"", "", ""}
		for _, kv := range [][2]string{{"schema_version", "player-analysis-v1"}, {"game_id", strconv.FormatUint(uint64(r.Config.GameID), 10)}, {"seed_kind", string(r.Config.Seed.Kind())}, {"seed", r.Config.Seed.String()}, {"status", string(r.Status)}, {"max_spins_per_player", integer(MaxSpinsPerPlayer)}, {"error", r.Error}} {
			row(p, "run", kv[0], kv[1])
		}
		for i, m := range r.Modes {
			row(p, "mode", fmt.Sprintf("mode.%d.bet_unit", i), integer(m.BetUnit))
			row(p, "mode", fmt.Sprintf("mode.%d.source", i), m.Source)
		}
		// These are descriptive spin statistics, not independent-player CIs.
		row(p, "run", "win_bucket_ci_reason", "not_estimated")
		row(p, "run", "pooled_rtp_ci_reason", "not_estimated")
	}
	for i, g := range r.Groups {
		p := []string{integer(i), g.Config.Name, string(g.Status)}
		confidence := "0.95"
		if g.Config.CI == "confidence_99" {
			confidence = "0.99"
		}
		if table == 0 {
			for _, kv := range [][2]string{{"strategy", g.Config.Player.Strategy}, {"init_bet", integer(g.Config.Player.InitBet)}, {"initial_balance", integer(g.InitialBalance)}, {"ci", g.Config.CI}, {"target_players", integer(g.Config.Player.Players)}, {"started_players", integer(g.Started)}, {"completed_players", integer(g.Completed)}, {"incomplete_players", integer(g.Started - g.Completed)}, {"not_started_players", integer(g.Config.Player.Players - g.Started)}, {"complete_spins", u64(g.CompleteSpins)}, {"total_bet", u64(g.TotalBet)}, {"total_win", u64(g.TotalWin)}, {"pooled_rtp", estimate(g.PooledRTP)}, {"pooled_rtp_defined", boolean(g.PooledRTP.Defined)}, {"undefined_rtp_players", u64(g.UndefinedRTPPlayers)}, {"unmatched", u64(g.Unmatched)}, {"stop_reason", string(g.Status)}, {"error", g.Error}} {
				row(p, "group", kv[0], kv[1])
			}
			for j, b := range g.Config.WinBuckets {
				row(p, "config", fmt.Sprintf("win_buckets.%d", j), f64(b))
			}
			for j, e := range g.Config.Events {
				base := fmt.Sprintf("events.%d.", j)
				for _, kv := range [][2]string{{"name", e.Name}, {"min", f64(e.WinRange.Min)}, {"max", upper(e.WinRange.Max)}, {"upper_unbounded", boolean(e.WinRange.Max == nil)}} {
					row(p, "config", base+kv[0], kv[1])
				}
				for k, t := range e.Tags {
					row(p, "config", fmt.Sprintf("%stags.%d", base, k), t)
				}
			}
			continue
		}
		if g.Status == NotStarted {
			continue
		}
		switch table {
		case 1:
			for j, b := range g.Buckets {
				row(p, integer(j), f64(b.Min), upper(b.Max), boolean(b.IncludeMin), boolean(b.IncludeMax), u64(b.Count), u64(g.CompleteSpins), ratio(b.Count, g.CompleteSpins), estimate(b.ObservedMax))
			}
		case 2:
			for _, v := range g.RTP {
				count := ""
				if v.Metric == "threshold" {
					count = u64(v.Count)
				}
				cols := []string{v.Metric, f64(v.Parameter), u64(v.N), count, estimate(v.Estimate), boolean(v.Defined)}
				row(p, append(cols, interval(v.CI, confidence)...)...)
			}
		case 3:
			for j, e := range g.Events {
				cfg := g.Config.Events[j]
				lo, hi, _ := rangeBuckets(g.Config.WinBuckets, cfg.WinRange)
				for k, name := range []string{"zero", "one", "two", "more"} {
					cols := []string{integer(j), cfg.Name, f64(cfg.WinRange.Min), upper(cfg.WinRange.Max), boolean(g.Buckets[lo].IncludeMin), boolean(g.Buckets[hi-1].IncludeMax), name, u64(e.Counts[k]), integer(g.Completed), estimate(e.Stats[k].Estimate), u64(e.Hits)}
					row(p, append(cols, interval(e.Stats[k].CI, confidence)...)...)
				}
			}
		case 4:
			for j, name := range []string{"bust", "satisfied", "dissatisfied", "others", "max_spin_reached"} {
				k, st := g.MaxSpinReached, g.MaxSpinStat
				if j < 4 {
					k = g.Exits[j]
					st = g.ExitStats[j]
				}
				cols := []string{name, boolean(j == 4), u64(k), integer(g.Completed), estimate(st.Estimate)}
				row(p, append(cols, interval(st.CI, confidence)...)...)
			}
		}
	}
	cw.Flush()
	return errors.Join(writeErr, cw.Error())
}

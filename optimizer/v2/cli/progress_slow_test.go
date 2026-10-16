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
	"bytes"
	"strings"
	"testing"
	"time"

	optimizerv2 "github.com/zintix-labs/problab/optimizer/v2"
)

func slowEvents(save string) []optimizerv2.StageEvent {
	base := optimizerv2.StageEvent{Stage: "solve[mode=0]", Substage: optimizerv2.StageMaximizeMainGroupInternalVisibility,
		SolveIndex: 47, Class: 12, ClassCount: 100, Variables: 36, Rows: 82}
	at := func(state string, elapsed time.Duration, path, message string) optimizerv2.StageEvent {
		event := base
		event.State, event.Duration, event.Path, event.Message = state, elapsed, path, message
		return event
	}
	events := []optimizerv2.StageEvent{
		{Stage: "solve[mode=0]", Substage: optimizerv2.StageMaximizeMainGroupInternalVisibility, State: "started"},
		at(optimizerv2.SolveEventStarted, 0, "", ""),
		at(optimizerv2.SolveEventSlow, 5*time.Second, "", ""),
	}
	if save == "ok" {
		events = append(events, at(optimizerv2.SolveEventSaved, 5*time.Second, "/tmp/slow.json", ""))
	} else {
		events = append(events, at(optimizerv2.SolveEventSaveFailed, 5*time.Second, "", "disk full"))
	}
	events = append(events,
		at(optimizerv2.SolveEventRunning, 30*time.Second, "/tmp/slow.json", ""),
		at(optimizerv2.SolveEventRunning, 66*time.Second, "/tmp/slow.json", ""),
		at(optimizerv2.SolveEventFinished, 70*time.Second, "/tmp/slow.json", ""),
		optimizerv2.StageEvent{Stage: "solve[mode=0]", Substage: optimizerv2.StageMaximizeMainGroupInternalVisibility, State: "completed", Duration: 71 * time.Second},
	)
	return events
}

// A10, terminal: the temporary row sits beneath the step line, refreshes in
// place, is erased when the LP finishes, and the step line is restored.
func TestCLISlowSolveTerminalRowIsTemporary(t *testing.T) {
	var output bytes.Buffer
	reporter := newCLIProgressReporter(&output)
	reporter.interactive = true
	for _, event := range slowEvents("ok") {
		reporter.Report(event)
	}
	text := output.String()
	for _, want := range []string{
		"  step 1 Maximizing minimum relative visibility within Main Group buckets (mode 0) ... \n",
		"\r\x1b[2K    still solving 00:05 | Main visibility | Class 12/100 | 36 vars / 82 rows | LP #47",
		"saved /tmp/slow.json",
		"\r\x1b[2K\x1b[1A\r\x1b[2K  step 1 Maximizing minimum relative visibility within Main Group buckets (mode 0) ... ",
		"success (1m11s) [slow LPs: 1, longest 1m10s]\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
	if strings.Contains(text, "%") || strings.Contains(text, "remaining") {
		t.Fatalf("slow row must not invent progress: %q", text)
	}
}

// A save failure stays on screen after the temporary row is cleared.
func TestCLISlowSolveTerminalKeepsSaveFailure(t *testing.T) {
	var output bytes.Buffer
	reporter := newCLIProgressReporter(&output)
	reporter.interactive = true
	for _, event := range slowEvents("fail") {
		reporter.Report(event)
	}
	text := output.String()
	if !strings.Contains(text, "    could not save slow-solve input: disk full\n") {
		t.Fatalf("save failure not kept: %q", text)
	}
	if strings.Contains(text, "\x1b[1A") {
		t.Fatalf("cleanup must not move back over the permanent failure line: %q", text)
	}
}

// A10, redirected output: no carriage returns; threshold, save, 60-second
// cadence and finish lines only.
func TestCLISlowSolveRedirectedLog(t *testing.T) {
	var output bytes.Buffer
	reporter := newCLIProgressReporter(&output)
	for _, event := range slowEvents("ok") {
		reporter.Report(event)
	}
	text := output.String()
	if strings.Contains(text, "\r") {
		t.Fatalf("redirected output must not use carriage returns: %q", text)
	}
	want := []string{
		"    still solving 00:05 | Main visibility | Class 12/100 | 36 vars / 82 rows | LP #47\n",
		"    saved slow-solve input: /tmp/slow.json\n",
		"    still solving 01:06 | Main visibility",
		"    finished 01:10 | Main visibility",
	}
	for _, line := range want {
		if !strings.Contains(text, line) {
			t.Fatalf("missing %q in %q", line, text)
		}
	}
	if strings.Contains(text, "still solving 00:30") {
		t.Fatalf("heartbeat logged before the 60-second cadence: %q", text)
	}
}

// Fast LPs produce no output at all.
func TestCLIFastSolveIsSilent(t *testing.T) {
	var output bytes.Buffer
	reporter := newCLIProgressReporter(&output)
	base := optimizerv2.StageEvent{Stage: "solve[mode=0]", Substage: optimizerv2.StageProveHardFeasibility, SolveIndex: 1}
	started, finished := base, base
	started.State, finished.State = optimizerv2.SolveEventStarted, optimizerv2.SolveEventFinished
	reporter.Report(started)
	reporter.Report(finished)
	if output.Len() != 0 {
		t.Fatalf("fast solve printed %q", output.String())
	}
}

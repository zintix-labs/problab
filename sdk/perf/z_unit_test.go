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

package perf

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

func TestCloseProfileReportsErrorWithoutReplacingPanic(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "closed-profile")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	sentinel := &struct{ message string }{"workload failed"}
	var caught any
	func() {
		defer func() { caught = recover() }()
		defer closeProfile(f)
		panic(sentinel)
	}()
	if caught != sentinel {
		t.Fatalf("close replaced workload panic: %v", caught)
	}
	if !strings.Contains(output.String(), "failed to close profile "+f.Name()) {
		t.Fatalf("missing close diagnostic: %q", output.String())
	}
}

func TestRunPProfModes(t *testing.T) {
	exe := func() {}

	RunPProf(exe, "cpu")
	if _, err := os.Stat(pprofDir + "/cpu.pprof"); err != nil {
		t.Fatalf("cpu profile missing: %v", err)
	}

	RunPProf(exe, "heap")
	if _, err := os.Stat(pprofDir + "/heap.pprof"); err != nil {
		t.Fatalf("heap profile missing: %v", err)
	}

	RunPProf(exe, "allocs")
	if _, err := os.Stat(pprofDir + "/allocs.pprof"); err != nil {
		t.Fatalf("allocs profile missing: %v", err)
	}
}

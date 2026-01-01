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

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// runTest executes a quick test pass:
// 1. go clean -testcache
// 2. go test ./... (filtered output: ok / FAIL only)
func runTest() {
	PrintGreen("running tests")

	// --- Step 1: Clean Cache ---
	// -> go clean -testcache
	cleanCmd := exec.Command("go", "clean", "-testcache")
	if err := cleanCmd.Run(); err != nil {
		PrintRed(err.Error())
		// You may choose to exit here if cleaning the test cache fails.
	}

	// --- Step 2: execute tests and output result ---
	// -> go test ./... -cover -count=1
	cmd := exec.Command("go", "test", "./...", "-cover", "-count=1")
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		panic(err)
	}

	// use : stderr
	cmd.Stderr = cmd.Stdout

	// start
	if err := cmd.Start(); err != nil {
		PrintRed(fmt.Sprintf("Error starting go test: %v", err))
		os.Exit(1)
	}

	// --- Step 3: grep -E '^(ok|FAIL)' ---
	scanner := bufio.NewScanner(stdoutPipe)
	for scanner.Scan() {
		line := scanner.Text()

		// only print ok/FAIL
		if strings.HasPrefix(line, "ok") {
			PrintGreen(line)
		} else if strings.HasPrefix(line, "FAIL") {
			PrintRed(line)
		} else if strings.Contains(line, "build failed") || strings.Contains(line, "setup failed") {
			PrintRed(line)
		}
	}

	if err := cmd.Wait(); err != nil {
		// (exit code != 0)
		PrintRed("\nTests Finished with Errors\n")
		os.Exit(1)
	}
}

// runTestAll executes all tests with coverage reporting.
//
// Equivalent Makefile target:
//
//	test-all:
//
//	  go clean -testcache && go test -cover ./...
//
// Behavior:
//  1. Clear test cache (exit on failure)
//  2. Run all package tests and print coverage results
func runTestAll() {
	PrintGreen("running tests (all with coverage)")

	// Step 1: go clean -testcache
	cleanCmd := exec.Command("go", "clean", "-testcache")
	cleanCmd.Stdout = os.Stdout
	cleanCmd.Stderr = os.Stderr
	if err := cleanCmd.Run(); err != nil {
		PrintRed(fmt.Sprintf("go clean -testcache failed: %v", err))
		os.Exit(1)
	}

	// Step 2: go test -cover ./...
	testCmd := exec.Command("go", "test", "./...", "-cover")
	testCmd.Stdout = os.Stdout
	testCmd.Stderr = os.Stderr

	if err := testCmd.Run(); err != nil {
		PrintRed("\nTests (with coverage) finished with errors\n")
		os.Exit(1)
	}
}

// runTestDetail runs verbose tests with filtered output.
//
// Equivalent Makefile target:
//
//	test-detail:
//
//	  go clean -testcache
//	  SHELL=/bin/bash; set -o pipefail; \
//	    go test ./... -v -count=1 2>&1 | \
//	      grep -v '\[no test files\]'
//
// Behavior:
//  1. Clear test cache (exit on failure)
//  2. Run tests in verbose mode
//  3. Filter out "[no test files]" lines
func runTestDetail() {
	PrintGreen("running tests (detail)")

	// Step 1: go clean -testcache
	cleanCmd := exec.Command("go", "clean", "-testcache")
	cleanCmd.Stdout = os.Stdout
	cleanCmd.Stderr = os.Stderr
	if err := cleanCmd.Run(); err != nil {
		PrintRed(fmt.Sprintf("go clean -testcache failed: %v", err))
		os.Exit(1)
	}

	// Step 2: go test ./... -v -count=1
	cmd := exec.Command("go", "test", "./...", "-v", "-count=1")

	// Merge stdout and stderr (equivalent to "2>&1")
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		PrintRed(fmt.Sprintf("failed to get stdout pipe: %v", err))
		os.Exit(1)
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		PrintRed(fmt.Sprintf("Error starting go test: %v", err))
		os.Exit(1)
	}

	scanner := bufio.NewScanner(stdoutPipe)
	for scanner.Scan() {
		line := scanner.Text()

		// Equivalent to: grep -v '\[no test files\]'
		if strings.Contains(line, "[no test files]") {
			continue
		}

		// Optional coloring logic:
		// ok   xxx => green
		// FAIL xxx => red
		if strings.HasPrefix(line, "ok") {
			PrintGreen(line)
		} else if strings.HasPrefix(line, "FAIL") {
			PrintRed(line)
		} else {
			// General logs printed as is
			fmt.Println(line)
		}
	}

	if err := scanner.Err(); err != nil {
		PrintRed(fmt.Sprintf("scanner error: %v", err))
		// Typically IO issues; decide whether to exit based on context
	}

	// Wait for go test to finish and check exit code
	if err := cmd.Wait(); err != nil {
		PrintRed("\nTests (detail) finished with errors\n")
		os.Exit(1)
	}
}

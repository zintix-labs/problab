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
	"fmt"
	"os"
)

func main() {
	exeCmd()
}

func exeCmd() {
	// If no command is provided, print usage and exit.
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run scripts/ops.go [command]")
		os.Exit(1)
	}

	task := os.Args[1] // First argument (os.Args[0] is the executable itself)
	selectTask(task)   // Dispatch task execution
}

func selectTask(task string) {
	switch task {
	case "test":
		runTest()
	case "test-all":
		runTestAll()
	case "test-detail":
		runTestDetail()
	default:
		PrintYellow(fmt.Sprintf("Unknown task: %s\n", task))
		os.Exit(1)
	}
}

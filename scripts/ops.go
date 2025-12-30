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
	// 如果沒有送任何參數進來，我們告訴用戶需要帶上 task
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run scripts/ops.go [command]")
		os.Exit(1)
	}

	task := os.Args[1] // 取第一個參數 (os.Args[0] 是執行檔本身)
	selectTask(task)   // 路由執行
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

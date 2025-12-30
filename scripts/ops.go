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

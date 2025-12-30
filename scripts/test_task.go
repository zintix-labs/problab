package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// runTest 重現了原本 Makefile 的邏輯：
// 1. go clean -testcache
// 2. go test ... 2>&1 | grep -E '^(ok|FAIL)'
func runTest() {
	PrintGreen("running tests")

	// --- 步驟 1: 清除 Cache ---
	// 對應: @go clean -testcache
	cleanCmd := exec.Command("go", "clean", "-testcache")
	if err := cleanCmd.Run(); err != nil {
		PrintRed(err.Error())
		// 這裡可以選擇要不要 exit，通常 clean 失敗不一定要中斷
	}

	// --- 步驟 2: 執行測試並過濾輸出 ---
	// 對應: go test ./... -cover -count=1
	cmd := exec.Command("go", "test", "./...", "-cover", "-count=1")

	// 技巧：獲取 stdout 的 pipe，以便我們像 grep 一樣一行行讀取
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		panic(err)
	}

	// 對應 Shell 的 "2>&1"：將 Stderr 也導向同一個 Pipe
	// 這樣如果是編譯錯誤 (通常在 Stderr)，我們也能讀到
	cmd.Stderr = cmd.Stdout

	// 啟動指令 (Start 不會等待執行完成，Run 才會)
	if err := cmd.Start(); err != nil {
		PrintRed(fmt.Sprintf("Error starting go test: %v", err))
		os.Exit(1)
	}

	// --- 步驟 3: 模擬 grep -E '^(ok|FAIL)' ---
	scanner := bufio.NewScanner(stdoutPipe)
	for scanner.Scan() {
		line := scanner.Text()

		// 邏輯判斷：只印出開頭是 "ok" 或 "FAIL" 的行
		// 注意：如果發生編譯錯誤，輸出通常不會以 ok/FAIL 開頭，
		// 為了避免完全看不到錯誤訊息，建議您可以保留編譯錯誤的顯示，
		// 但如果您堅持要跟原本 grep 行為一模一樣，就只留這兩個條件。
		if strings.HasPrefix(line, "ok") {
			PrintGreen(line)
		} else if strings.HasPrefix(line, "FAIL") {
			PrintRed(line)
		} else if strings.Contains(line, "build failed") || strings.Contains(line, "setup failed") {
			// 捕捉嚴重錯誤關鍵字，不然 grep 過濾太乾淨會看不出為什麼沒反應
			PrintRed(line)
		}
	}

	// 等待指令結束並檢查 Exit Code
	if err := cmd.Wait(); err != nil {
		// 這裡捕捉測試失敗 (exit code != 0)
		PrintRed("\nTests Finished with Errors\n")
		os.Exit(1) // 告訴 Makefile 失敗了
	}
}

// runTestAll 對應 Makefile:
//
// test-all:
//
//	go clean -testcache && go test -cover ./...
//
// 行為：
//  1. 清除 test cache（失敗就結束）
//  2. 跑全部套件的測試並顯示 cover 結果
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

// runTestDetail 對應 Makefile:
//
// test-detail:
//
//	go clean -testcache
//	SHELL=/bin/bash; set -o pipefail; \
//	  go test ./... -v -count=1 2>&1 | \
//	    grep -v '\[no test files\]'
//
// 行為：
//  1. 清除 test cache（失敗就結束）
//  2. verbose 測試，顯示所有 log，但過濾掉 "[no test files]" 那些行
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

	// 把 stdout/stderr 合併，模擬 "2>&1"
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

		// 等同 grep -v '\[no test files\]'
		if strings.Contains(line, "[no test files]") {
			continue
		}

		// 其他行照常打印，你可以再加顏色邏輯：
		// ok   xxx => 綠
		// FAIL xxx => 紅
		if strings.HasPrefix(line, "ok") {
			PrintGreen(line)
		} else if strings.HasPrefix(line, "FAIL") {
			PrintRed(line)
		} else {
			// 一般 log 就直接印
			fmt.Println(line)
		}
	}

	if err := scanner.Err(); err != nil {
		PrintRed(fmt.Sprintf("scanner error: %v", err))
		// 通常這種屬於 IO 問題，視情況要不要 exit
	}

	// 等待 go test 結束，檢查 exit code
	if err := cmd.Wait(); err != nil {
		PrintRed("\nTests (detail) finished with errors\n")
		os.Exit(1)
	}
}

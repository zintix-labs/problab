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

import "fmt"

// Helper
// 這邊定義了一些格式化顏色輸出，方便腳本中需要打印顏色的情況

// ANSI 顏色代碼 (Windows 10+ 的 cmd/powershell 皆支援)
type ANSI_COLOR string

const (
	ColorBlue    ANSI_COLOR = "\033[34m"
	ColorYellow  ANSI_COLOR = "\033[33m"
	ColorGreen   ANSI_COLOR = "\033[32m"
	ColorRed     ANSI_COLOR = "\033[31m"
	ColorWhite   ANSI_COLOR = "\033[97m" // 顯式白
	ColorDefault ANSI_COLOR = ""         // 使用終端預設色
	ColorReset              = "\033[0m"  // 不給選
)

func fmtColor(color ANSI_COLOR, msg string) {
	fmt.Printf("%s%s%s\n", color, msg, ColorReset)
}

func PrintDefault(msg string) { fmtColor(ColorDefault, msg) }
func PrintWhite(msg string)   { fmtColor(ColorWhite, msg) }
func PrintRed(msg string)     { fmtColor(ColorRed, msg) }
func PrintGreen(msg string)   { fmtColor(ColorGreen, msg) }
func PrintYellow(msg string)  { fmtColor(ColorYellow, msg) }
func PrintBlue(msg string)    { fmtColor(ColorBlue, msg) }

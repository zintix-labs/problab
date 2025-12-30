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

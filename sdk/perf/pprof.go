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
	"os"
	"runtime"
	"runtime/pprof"
)

const pprofDir = "build/profiling" // pprof檔案寫入路徑

// Run 根據 cfg.ProfileType 決定執行哪種 Profiling
func RunPProf(exe func(), mode string) {

	// 確保目錄存在
	_ = os.MkdirAll(pprofDir, 0o755)

	switch mode {
	case "":
		exe()
	case "cpu":
		PProfCPU(exe)
	case "heap":
		PProfHeap(exe)
	case "allocs":
		PProfAllocs(exe)
	default:
		exe()
	}
}

// PProfCPU 函數會依據flag開關，決定是否開/關對於送入函數的 CPU profiling
//
// 可以作性能分析，也可以拿來做構建時給pgo的優化blueprint
//
// Usage like:
//
//	go run ./cmd/run -pprof
//	go run ./cmd/run -p
func PProfCPU(exe func()) {

	// 確保目錄存在
	_ = os.MkdirAll(pprofDir, 0o755)

	filePath := pprofDir + "/cpu.pprof"
	f, err := os.Create(filePath)
	if err != nil {
		panic("failed to create cpu.pprof : " + err.Error())
	}
	defer f.Close()
	if err := pprof.StartCPUProfile(f); err != nil {
		panic("failed to start pprof : " + err.Error())
	}
	defer pprof.StopCPUProfile()

	exe()
}

// PProfHeap 會在 exe() 執行完後，寫出一次 Heap Snapshot（in-use memory）。
// 注意：Heap Profile 與 CPU Profile 是不同的，CPU 檔不包含記憶體配置資訊。
// 通常在寫出 Heap Profile 前呼叫一次 runtime.GC()，以獲得較準確的 Live Objects 視圖。
// 輸出檔：build/profiling/heap.pprof
func PProfHeap(exe func()) {
	// 先執行目標邏輯，再拍一次快照
	exe()

	_ = os.MkdirAll(pprofDir, 0o755)

	// 盡量讓快照貼近最新狀態
	runtime.GC()

	filePath := pprofDir + "/heap.pprof"
	f, err := os.Create(filePath)
	if err != nil {
		panic("failed to create heap.pprof : " + err.Error())
	}
	defer f.Close()

	if err := pprof.WriteHeapProfile(f); err != nil {
		panic("failed to write heap profile : " + err.Error())
	}

}

// PProfAllocs 會在 exe() 後寫出「累積配置」(allocs) Profile，
// 可用於追蹤整體分配熱點（需要搭配 -alloc_space / -alloc_objects 指標查看）。
// 輸出檔：build/profiling/allocs.pprof
func PProfAllocs(exe func()) {
	exe()

	_ = os.MkdirAll(pprofDir, 0o755)

	filePath := pprofDir + "/allocs.pprof"
	f, err := os.Create(filePath)
	if err != nil {
		panic("failed to create allocs.pprof : " + err.Error())
	}
	defer f.Close()

	if prof := pprof.Lookup("allocs"); prof != nil {
		if err := prof.WriteTo(f, 0); err != nil {
			panic("failed to write allocs profile : " + err.Error())
		}
	}

}

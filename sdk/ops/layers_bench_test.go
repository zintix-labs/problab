package ops

import (
	"testing"

	"github.com/zintix-labs/problab/sdk/layer"
	"github.com/zintix-labs/problab/spec"
)

// 每輪恢復相同的 6x5 盤面與輪帶位置，避免量到已填滿的穩態盤面。
// 初始化成本包含在結果內；以相同 fixture 比較修改前後，不作遊戲整局耗時推論。
func BenchmarkLayersCycle(b *testing.B) {
	for _, trimmed := range []bool{false, true} {
		name := "rectangular"
		if trimmed {
			name = "trimmed"
		}
		for _, withLayers := range []bool{false, true} {
			mode := "none"
			if withLayers {
				mode = "two"
			}
			b.Run(name+"/"+mode, func(b *testing.B) {
				const cols, rows = 6, 5
				initial := make([]int16, cols*rows)
				for i := range initial {
					initial[i] = int16(i%5 + 1)
				}
				if trimmed {
					initial[0], initial[1], initial[5], initial[6] = -1, -1, -1, -1
				}
				screen := make([]int16, len(initial))
				fill, pos := make([]int, cols), make([]int, cols)
				hits := []int16{0, 2, 7, 10, 13, 16, 19, 22, 25, 28}
				reels := &spec.ReelSet{Reels: make([]spec.Reel, cols)}
				for i := range reels.Reels {
					reels.Reels[i].ReelSymbols = []int16{1, 2, 3, 4, 5, 6, 7}
				}
				a, bits := layer.New(len(initial), 0), layer.New(len(initial), false)
				var layers []layer.SyncOps
				if withLayers {
					layers = []layer.SyncOps{a, bits}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					copy(screen, initial)
					clear(pos)
					if withLayers {
						for j := range a.State {
							a.State[j] = j + 1
							bits.State[j] = j%2 == 0
						}
					}
					Clear(screen, hits, layers...)
					Gravity(screen, cols, rows, fill, layers...)
					FillScreen(screen, reels, fill, pos, cols, layers...)
				}
			})
		}
	}
}

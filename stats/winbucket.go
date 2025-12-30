package stats

const (
	maxLutMult int = 2000
	maxMult    int = 10000
)

// WinBuckets
//
// 用來快速定位得分 ->  DistRecord 位置 O(1)
//
// 請勿修改預設值
//   - win區間: 贏倍區間 [0,0], (0,1), [1,2), [2,5), ..., [2000,10000), [10000, +inf)
type WinBuckets struct {
	winBucket    []int
	winBucketStr []string
	winBucketMap map[int]*WinBucket
}

type WinBucket struct {
	maxCheckWin      int
	lutMaxWin        int
	winBucketByScore []int
	winBucketLUT     []int
	justOverIdx      int
	maxIdx           int
}

// Buckets
//
// 用來快速定位得分 ->  DistRecord 位置 O(1)
//
// 請勿修改預設值
//   - win區間: 贏倍區間 [0,0], (0,1), [1,2), [2,5), ..., [2000,10000), [10000, +inf)
var Buckets *WinBuckets = &WinBuckets{
	winBucket:    []int{0, 1, 2, 5, 10, 20, 50, 100, 300, 500, 1000, 2000, 10000},
	winBucketStr: []string{"[0,0]", "(0,1)", "[1,2)", "[2,5)", "[5,10)", "[10,20)", "[20,50)", "[50,100)", "[100,300)", "[300,500)", "[500,1000)", "[1000,2000)", "[2000,10000)", "[10000,+inf)"},
	winBucketMap: make(map[int]*WinBucket),
}

func (b *WinBuckets) WinBucketStr() []string {
	return b.winBucketStr
}

func (b *WinBuckets) GetBucketByBetUnit(bu int) *WinBucket {
	result, exist := b.winBucketMap[bu]
	if !exist {
		result = b.buldBucket(bu)
	}
	return result
}

func (b *WinBuckets) buldBucket(bu int) *WinBucket {
	// 我們只建到 2000 倍
	maxLut := bu * maxLutMult
	maxcheckwin := bu * maxMult

	// 把「倍數邊界」轉成「贏分邊界」
	winGp := make([]int, len(b.winBucket))
	for i, v := range b.winBucket {
		winGp[i] = bu * v
	}

	// 建立LUT反查表
	lut := make([]int, maxLut) // lut[win] = idx

	// 由 (0,1) 這個區間開始
	idx := 1
	last := len(winGp) - 1

	lut[0] = 0
	for i := 1; i < maxLut; i++ {
		// 僅在還有更高邊界時才前進 idx，避免越界讀取
		for idx < last && i >= winGp[idx] {
			idx++
		}
		lut[i] = idx
	}

	result := &WinBucket{
		maxCheckWin:      maxcheckwin,
		lutMaxWin:        maxLut,
		winBucketByScore: winGp,
		winBucketLUT:     lut,
		justOverIdx:      len(winGp) - 1,
		maxIdx:           len(winGp),
	}

	b.winBucketMap[bu] = result
	return result
}

func (wb *WinBucket) Index(win int) int {
	if win >= wb.lutMaxWin {
		if win >= wb.maxCheckWin {
			return wb.maxIdx
		}
		return wb.justOverIdx
	}
	return wb.winBucketLUT[win]
}

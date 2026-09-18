package game_tags

import (
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/tag"
	"testing"
)

func TestDemo0CollectionTags(t *testing.T) {
	r, err := tag.NewRegistry(GameTags[0])
	if err != nil {
		t.Fatal(err)
	}
	bs, err := r.BitSet("bg", "fg")
	if err != nil {
		t.Fatal(err)
	}
	for _, modes := range []int{1, 2} {
		for _, freeWin := range []int{0, 150, 179, 180, 181, 300} {
			sr := &buf.SpinResult{GameModeCount: modes, Bet: 30, TotalWin: freeWin + 90, GameModeList: []*buf.GameModeResult{{TotalWin: 90}}}
			var want uint64
			if modes == 1 {
				want = 1
			} else if freeWin >= 180 {
				want = 2
			}
			if got := bs.Tagging(sr); got != want {
				t.Fatalf("modes=%d freeWin=%d got=%d want=%d", modes, freeWin, got, want)
			}
		}
	}
}

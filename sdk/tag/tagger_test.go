// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package tag

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/spec"
)

func yes(*buf.SpinResult) bool { return true }
func no(*buf.SpinResult) bool  { return false }

// nTags 建立 n 個 tag：t000、t001…；第 i 個 tag 的結果由 hit(i) 決定。
func nTags(n int, hit func(i int) bool) (GameTagCatalog, []string) {
	defs := map[string]IsTag{}
	names := make([]string, n)
	for i := range n {
		names[i] = fmt.Sprintf("t%03d", i)
		v := hit(i)
		defs[names[i]] = func(*buf.SpinResult) bool { return v }
	}
	return GameTagCatalog{1: defs}, names
}

func requireCompileError(t *testing.T, err error, op Op, kind ErrorKind, name string, index int, text string) {
	t.Helper()
	var ce *CompileError
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a *CompileError", err)
	}
	if ce.Op != op || ce.Kind != kind || ce.Name != name || ce.Index != index {
		t.Fatalf("error=%+v, want op=%d kind=%d name=%q index=%d", ce, op, kind, name, index)
	}
	if err.Error() != text {
		t.Fatalf("error text=%q, want %q", err.Error(), text)
	}
}

func TestCompileOrderAndOwnership(t *testing.T) {
	defs := map[string]IsTag{"c": yes, "a": no, "b": yes}
	catalog := GameTagCatalog{7: defs}

	all, err := catalog.Compile(7)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(all.names, []string{"a", "b", "c"}) {
		t.Fatalf("Compile order=%v, want lexical", all.names)
	}

	by, err := catalog.CompileBy(7, "c", "a", "c", "b", "a")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(by.names, []string{"c", "a", "b"}) {
		t.Fatalf("CompileBy order=%v, want first occurrence", by.names)
	}

	// 編譯後修改目錄不影響 Tagger。
	defs["a"] = yes
	delete(defs, "b")
	bm := by.NewMasks()
	by.Tag(bm, nil)
	if got := by.ToNames(bm); !reflect.DeepEqual(got, []string{"c", "b"}) {
		t.Fatalf("Tagger changed after catalog mutation: %v", got)
	}

	empty, err := catalog.CompileBy(7)
	if err != nil || empty.Len() != 0 || len(empty.NewMasks()) != 0 {
		t.Fatalf("CompileBy without names: tagger=%+v err=%v", empty, err)
	}
	none, err := GameTagCatalog{8: {}}.Compile(8)
	if err != nil || none.Len() != 0 {
		t.Fatalf("Compile of an empty game: tagger=%+v err=%v", none, err)
	}
}

func TestCompileErrors(t *testing.T) {
	catalog := GameTagCatalog{1: {"a": yes, "b": nil, "": yes}}

	_, err := catalog.Compile(2)
	requireCompileError(t, err, OpCompile, GameNotFound, "", 0, "tags for game 2 not found")
	_, err = catalog.CompileBy(2, "a")
	requireCompileError(t, err, OpCompile, GameNotFound, "", 0, "tags for game 2 not found")

	// Compile 依字典序回報第一個不合法的定義：空名稱排在最前面。
	_, err = catalog.Compile(1)
	requireCompileError(t, err, OpCompile, InvalidDefinition, "", 0, "tag name is empty")
	_, err = GameTagCatalog{1: {"a": yes, "b": nil}}.Compile(1)
	requireCompileError(t, err, OpCompile, InvalidDefinition, "b", 0, `tag "b" predicate is nil`)

	// CompileBy 依參數順序回報第一個問題，Index 是名稱在參數中的位置。
	_, err = catalog.CompileBy(1, "a", "a", "x", "b")
	requireCompileError(t, err, OpCompile, NameNotFound, "x", 2, "tag 2 not found: x")
	_, err = catalog.CompileBy(1, "a", "b", "x")
	requireCompileError(t, err, OpCompile, InvalidDefinition, "b", 0, `tag "b" predicate is nil`)
	_, err = catalog.CompileBy(1, "")
	requireCompileError(t, err, OpCompile, InvalidDefinition, "", 0, "tag name is empty")
	// 只驗證被指定的名稱。
	if _, err := catalog.CompileBy(1, "a"); err != nil {
		t.Fatalf("CompileBy validated an unreferenced definition: %v", err)
	}
	// 全空白的名稱不被 trim，視為一般名稱。
	if _, err := (GameTagCatalog{1: {" ": yes}}).CompileBy(1, " "); err != nil {
		t.Fatalf("blank name rejected: %v", err)
	}
}

func TestMaskWordsAndUnusedBits(t *testing.T) {
	for _, n := range []int{0, 1, 63, 64, 65, 128, 129} {
		catalog, names := nTags(n, func(int) bool { return true })
		tg, err := catalog.CompileBy(1, names...)
		if err != nil {
			t.Fatal(err)
		}
		wantWords := (n + 63) / 64
		bm := tg.NewMasks()
		if len(bm) != wantWords || tg.Len() != n {
			t.Fatalf("n=%d: words=%d len=%d", n, len(bm), tg.Len())
		}
		tg.Tag(bm, nil)
		all, err := tg.Want(names...)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range []BitMasks{bm, all} {
			if n%64 != 0 && m[len(m)-1]>>(n%64) != 0 {
				t.Fatalf("n=%d: unused bits set in last word %x", n, m[len(m)-1])
			}
		}
		if !tg.Equal(bm, all) {
			t.Fatalf("n=%d: all-true tagging differs from Want(all)", n)
		}
	}
}

// TestTagAgainstOracle 以逐一呼叫 IsTag 作為對照，驗證跨段的每個位元。
func TestTagAgainstOracle(t *testing.T) {
	const n = 130
	hit := func(i int) bool { return i%3 == 0 || i == 64 || i == 127 || i == 128 }
	catalog, names := nTags(n, hit)
	tg, err := catalog.CompileBy(1, names...)
	if err != nil {
		t.Fatal(err)
	}
	bm := tg.NewMasks()
	tg.Tag(bm, nil)
	for i := range n {
		if got := bm[i>>6]&(1<<(i&63)) != 0; got != hit(i) {
			t.Fatalf("bit %d=%t, want %t", i, got, hit(i))
		}
		one, err := tg.Want(names[i])
		if err != nil {
			t.Fatal(err)
		}
		if tg.Match(bm, one) != hit(i) || tg.None(bm, one) == hit(i) {
			t.Fatalf("tag %d: Match/None disagree with the oracle", i)
		}
	}
	if !reflect.DeepEqual(tg.ToNames(BitMasks{0, 0, 0}), []string{}) {
		t.Fatal("ToNames of an empty mask")
	}
	for _, i := range []int{63, 64, 127, 128} {
		one, _ := tg.Want(names[i])
		if got := tg.ToNames(one); !reflect.DeepEqual(got, []string{names[i]}) {
			t.Fatalf("ToNames(bit %d)=%v", i, got)
		}
	}
}

func TestTagClearsAndAddAccumulates(t *testing.T) {
	var state bool
	catalog := GameTagCatalog{1: {
		"flip":   func(*buf.SpinResult) bool { return state },
		"always": yes,
	}}
	tg, err := catalog.CompileBy(1, "flip", "always")
	if err != nil {
		t.Fatal(err)
	}
	flip, _ := tg.Want("flip")
	bm := tg.NewMasks()
	state = true
	tg.Tag(bm, nil)
	state = false
	tg.Add(bm, nil)
	if !tg.Match(bm, flip) {
		t.Fatal("Add cleared an earlier bit")
	}
	tg.Tag(bm, nil)
	if tg.Match(bm, flip) {
		t.Fatal("Tag kept the previous round's bit")
	}
}

func TestMatchEqualNoneSemantics(t *testing.T) {
	catalog, names := nTags(70, func(i int) bool { return i == 0 || i == 66 })
	tg, err := catalog.CompileBy(1, names...)
	if err != nil {
		t.Fatal(err)
	}
	bm := tg.NewMasks()
	tg.Tag(bm, nil)
	zero, err := tg.Want()
	if err != nil || len(zero) != 2 {
		t.Fatalf("Want() = %v %v", zero, err)
	}
	both, _ := tg.Want(names[0], names[66])
	first, _ := tg.Want(names[0])
	other, _ := tg.Want(names[1], names[65])
	mixed, _ := tg.Want(names[0], names[1])
	dup, _ := tg.Want(names[0], names[0])

	cases := []struct {
		name               string
		want               BitMasks
		match, equal, none bool
	}{
		{"zero", zero, true, false, true},
		{"both", both, true, true, false},
		{"first", first, true, false, false},
		{"other", other, false, false, true},
		{"mixed", mixed, false, false, false},
		{"duplicate", dup, true, false, false},
	}
	for _, c := range cases {
		if tg.Match(bm, c.want) != c.match || tg.Equal(bm, c.want) != c.equal || tg.None(bm, c.want) != c.none {
			t.Fatalf("%s: match=%t equal=%t none=%t", c.name, tg.Match(bm, c.want), tg.Equal(bm, c.want), tg.None(bm, c.want))
		}
	}
	clear(bm)
	if !tg.Equal(bm, zero) {
		t.Fatal("all-zero mask must equal the zero condition")
	}

	// 沒有任何 tag 的 Tagger：段數為 0，三個比對都為真。
	empty, _ := catalog.CompileBy(1)
	ebm, ewant := empty.NewMasks(), BitMasks{}
	if !empty.Match(ebm, ewant) || !empty.Equal(ebm, ewant) || !empty.None(ebm, ewant) || len(empty.ToNames(ebm)) != 0 {
		t.Fatal("empty Tagger comparisons")
	}
}

func TestWantErrors(t *testing.T) {
	tg, err := GameTagCatalog{3: {"a": yes}}.CompileBy(3, "a")
	if err != nil {
		t.Fatal(err)
	}
	_, err = tg.Want("a", "missing")
	requireCompileError(t, err, OpWant, NameNotFound, "missing", 1, "tag not found in tagger: missing")
	var ce *CompileError
	if errors.As(err, &ce); ce.GID != spec.GID(3) {
		t.Fatalf("Want error GID=%d", ce.GID)
	}
}

func TestMasksAreIndependent(t *testing.T) {
	tg, err := GameTagCatalog{1: {"a": yes}}.CompileBy(1, "a")
	if err != nil {
		t.Fatal(err)
	}
	a, b := tg.NewMasks(), tg.NewMasks()
	want, _ := tg.Want("a")
	tg.Tag(a, nil)
	if b[0] != 0 {
		t.Fatal("NewMasks shares storage")
	}
	clear(a)
	if want[0] == 0 {
		t.Fatal("working mask shares storage with the condition")
	}
}

// TestSharedTaggerConcurrentUse 多個 goroutine 共用一個 Tagger、各自持有工作遮罩；
// 以 -race 執行時不得有資料競爭。
func TestSharedTaggerConcurrentUse(t *testing.T) {
	catalog, names := nTags(100, func(i int) bool { return i%2 == 0 })
	tg, err := catalog.CompileBy(1, names...)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := tg.Want(names[0], names[98])
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			bm := tg.NewMasks()
			for range 200 {
				tg.Tag(bm, nil)
				if !tg.Match(bm, want) {
					t.Error("concurrent Match failed")
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestHotPathDoesNotAllocate(t *testing.T) {
	catalog, names := nTags(100, func(i int) bool { return i%2 == 0 })
	tg, err := catalog.CompileBy(1, names...)
	if err != nil {
		t.Fatal(err)
	}
	bm := tg.NewMasks()
	want, _ := tg.Want(names[0])
	sr := &buf.SpinResult{}
	allocs := testing.AllocsPerRun(100, func() {
		tg.Tag(bm, sr)
		tg.Add(bm, sr)
		_ = tg.Match(bm, want)
		_ = tg.Equal(bm, want)
		_ = tg.None(bm, want)
	})
	if allocs != 0 {
		t.Fatalf("hot path allocates %.1f times per run", allocs)
	}
}

// BenchmarkTaggerTag measures evaluating every compiled tag for one result.
// 判斷式固定為交替的 yes／no 兩個函式，讓不同版本的量測使用相同的工作負載。
func BenchmarkTaggerTag(b *testing.B) {
	for _, n := range []int{3, 64, 65} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			defs := map[string]IsTag{}
			names := make([]string, n)
			for i := range n {
				names[i] = fmt.Sprintf("t%03d", i)
				if i%2 == 0 {
					defs[names[i]] = yes
				} else {
					defs[names[i]] = no
				}
			}
			catalog := GameTagCatalog{1: defs}
			tg, err := catalog.CompileBy(1, names...)
			if err != nil {
				b.Fatal(err)
			}
			bm := tg.NewMasks()
			sr := &buf.SpinResult{}
			b.ReportAllocs()
			for b.Loop() {
				tg.Tag(bm, sr)
			}
		})
	}
}

// BenchmarkTaggerMatch measures comparing a working mask with a condition.
func BenchmarkTaggerMatch(b *testing.B) {
	for _, n := range []int{3, 65} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			catalog, names := nTags(n, func(int) bool { return true })
			tg, err := catalog.CompileBy(1, names...)
			if err != nil {
				b.Fatal(err)
			}
			bm := tg.NewMasks()
			tg.Tag(bm, nil)
			want, _ := tg.Want(names[0], names[n-1])
			var sink bool
			b.ReportAllocs()
			for b.Loop() {
				sink = tg.Match(bm, want) != sink
			}
		})
	}
}

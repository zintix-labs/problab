// Copyright 2026 Zintix Labs
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

package optimalrt

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/zintix-labs/problab/sdk/sampler"
)

func testGachaPair(n int) GachaPair {
	weights := make([]float64, n)
	bank := make([]byte, 0, n*testSeedLen)
	for i := range weights {
		weights[i] = float64(i + 1)
		bank = append(bank, bytes.Repeat([]byte{byte(i + 1)}, testSeedLen)...)
	}
	return GachaPair{Picker: sampler.BuildAliasTableF64(weights), SeedLen: testSeedLen, SeedBank: bank}
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names
}

// 發布目錄以內容身分命名、只有兩個檔案，可由載入器載入，內容與輸入相同。
func TestPublishGachaPairWritesLoadablePair(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "game_1", "mode_0")
	pair := testGachaPair(4)
	published, err := PublishGachaPair(parent, pair)
	if err != nil {
		t.Fatal(err)
	}
	if len(published.ID) != 64 || published.Dir != filepath.Join(parent, published.ID[:16]) || published.Reused ||
		published.Gacha != filepath.Join(published.Dir, GachaFile) || published.SeedBank != filepath.Join(published.Dir, SeedBankFile) {
		t.Fatalf("published %+v", published)
	}
	if got := dirEntries(t, parent); len(got) != 1 {
		t.Fatalf("parent entries %v", got)
	}
	info, err := os.Stat(published.Dir)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("directory mode %v: %v", info.Mode(), err)
	}
	source, err := newDirSource(published.Dir)
	if err != nil {
		t.Fatal(err)
	}
	m, closeFn, _, err := loadGachaPair(source, GachaFile, SeedBankFile, testSeedLen, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeFn() }()
	if !bytes.Equal(m.bank, pair.SeedBank) || m.picker.Len() != 4 {
		t.Fatal("loaded pair differs from the input")
	}
}

// 內容身分只看未壓縮的 gacha JSON 與 seed bank，與 zstd 的編碼設定無關。
func TestGachaPairIDIgnoresCompression(t *testing.T) {
	pair := testGachaPair(3)
	raw, err := encodeGachaPair(pair)
	if err != nil {
		t.Fatal(err)
	}
	published, err := PublishGachaPair(t.TempDir(), pair)
	if err != nil {
		t.Fatal(err)
	}
	if published.ID != gachaPairID(raw, pair.SeedBank) {
		t.Fatal("ID is not derived from the uncompressed content")
	}
	// 以另一種壓縮設定寫出同一份內容，重用驗證仍然通過。
	other := t.TempDir()
	target := filepath.Join(other, published.ID[:16])
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, GachaFile), enc.EncodeAll(raw, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = enc.Close()
	if err := os.WriteFile(filepath.Join(target, SeedBankFile), pair.SeedBank, 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := PublishGachaPair(other, pair)
	if err != nil || !again.Reused || again.ID != published.ID {
		t.Fatalf("reuse across compression settings: %+v %v", again, err)
	}
}

// 同一份內容重複發布時重用既有目錄；目錄名相同但內容不同時回傳錯誤，且不覆寫。
func TestPublishGachaPairReuseAndConflict(t *testing.T) {
	parent := t.TempDir()
	pair := testGachaPair(3)
	first, err := PublishGachaPair(parent, pair)
	if err != nil {
		t.Fatal(err)
	}
	again, err := PublishGachaPair(parent, pair)
	if err != nil || !again.Reused || again.Dir != first.Dir {
		t.Fatalf("reuse: %+v %v", again, err)
	}

	tampered := append([]byte(nil), pair.SeedBank...)
	tampered[0] ^= 0xff
	if err := os.WriteFile(first.SeedBank, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishGachaPair(parent, pair); err == nil || !strings.Contains(err.Error(), "different content") {
		t.Fatalf("conflict error %v", err)
	}
	if got, _ := os.ReadFile(first.SeedBank); !bytes.Equal(got, tampered) {
		t.Fatal("a conflicting directory was rewritten")
	}
	if err := os.WriteFile(filepath.Join(first.Dir, "extra"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first.SeedBank, pair.SeedBank, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishGachaPair(parent, pair); err == nil {
		t.Fatal("a directory with an extra file was reused")
	}
}

// 輸入不合法時不寫出任何東西。
func TestPublishGachaPairRejectsInvalidInput(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "out")
	short := testGachaPair(3)
	short.SeedBank = short.SeedBank[1:]
	noSeedLen := testGachaPair(3)
	noSeedLen.SeedLen = 0
	broken := testGachaPair(3)
	broken.Picker = &sampler.AliasTableF64{Size: 2, Prob: []float64{1}, Aliases: []int{0}}
	for name, pair := range map[string]GachaPair{"short bank": short, "no seed_len": noSeedLen, "bad picker": broken} {
		if _, err := PublishGachaPair(parent, pair); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Fatalf("invalid input created output: %v", err)
	}
}

// 同時發布同一份內容：都成功，只留下一個目錄，不留暫存目錄。
func TestPublishGachaPairConcurrentSameContent(t *testing.T) {
	parent := t.TempDir()
	pair := testGachaPair(5)
	const writers = 8
	results := make([]PublishedPair, writers)
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = PublishGachaPair(parent, pair)
		}()
	}
	wg.Wait()
	for i := range writers {
		if errs[i] != nil || results[i].Dir != results[0].Dir {
			t.Fatalf("writer %d: %+v %v", i, results[i], errs[i])
		}
	}
	if got := dirEntries(t, parent); len(got) != 1 {
		t.Fatalf("parent entries %v", got)
	}
}

// 寫出失敗時刪除暫存目錄。
func TestPublishGachaPairFailureRemovesStaging(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs a directory the test cannot write to")
	}
	parent := t.TempDir()
	// 讓目標名稱成為一般檔案：改名到它會失敗，且它不是可重用的發布目錄。
	pair := testGachaPair(3)
	raw, err := encodeGachaPair(pair)
	if err != nil {
		t.Fatal(err)
	}
	id := gachaPairID(raw, pair.SeedBank)
	if err := os.WriteFile(filepath.Join(parent, id[:16]), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishGachaPair(parent, pair); err == nil {
		t.Fatal("publishing over a file succeeded")
	}
	if got := dirEntries(t, parent); len(got) != 1 || got[0] != id[:16] {
		t.Fatalf("parent entries %v", got)
	}

	// 父目錄不可寫：建立暫存目錄就失敗，什麼都不留下。
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := PublishGachaPair(locked, pair); err == nil {
		t.Fatal("publishing into a read-only directory succeeded")
	}
	if got := dirEntries(t, locked); len(got) != 0 {
		t.Fatalf("entries left behind: %v", got)
	}
}

// 未壓縮的 gacha JSON 與載入器的型別一致：載入器能解析，欄位不多不少。
func TestEncodeGachaPairMatchesLoaderShape(t *testing.T) {
	raw, err := encodeGachaPair(testGachaPair(2))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 || fields["picker"] == nil || fields["seed_len"] == nil {
		t.Fatalf("gacha JSON fields %v", fields)
	}
}

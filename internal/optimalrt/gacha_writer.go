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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
	"github.com/zintix-labs/problab/sdk/sampler"
)

const (
	// GachaFile 與 SeedBankFile 是發布目錄中的兩個檔名。
	GachaFile    = "gacha.json.zst"
	SeedBankFile = "seed_bank.bin"

	gachaPairSchema = "problab.gacha-pair/v1\n"
	pairDirLen      = 16
)

// GachaPair 是要發布的一對 gacha 與 seed bank。Picker 與 SeedBank 由呼叫端擁有，寫出器不修改它們。
type GachaPair struct {
	Picker   *sampler.AliasTableF64
	SeedLen  int
	SeedBank []byte
}

// PublishedPair 描述一個已發布（或重用）的發布目錄。
type PublishedPair struct {
	ID       string // 內容身分：64 個十六進位字元
	Dir      string // <parent>/<ID 的前 16 個字元>
	Gacha    string // Dir/gacha.json.zst
	SeedBank string // Dir/seed_bank.bin
	Reused   bool   // 目錄已存在且內容相同，沒有重新寫出
}

// PublishGachaPair 在 parent 底下發布一對，回傳發布目錄。parent 由呼叫端決定，一對屬於哪個
// bet mode（或之後的哪個入口）都表現在 parent 上，寫出器不解讀它。
//
// 發布目錄以內容身分命名，發布後不再改寫：同一份內容重複發布時重用既有目錄，內容不同卻撞名
// 時回傳錯誤。寫出先在 parent 中的暫存目錄完成，以載入器驗證後整個改名成發布目錄，所以讀者
// 不會看到只寫了一半的一對；任何失敗都會刪除暫存目錄，不影響其他發布目錄。
func PublishGachaPair(parent string, pair GachaPair) (PublishedPair, error) {
	gachaJSON, err := encodeGachaPair(pair)
	if err != nil {
		return PublishedPair{}, fmt.Errorf("publish gacha pair: %w", err)
	}
	id := gachaPairID(gachaJSON, pair.SeedBank)
	target := filepath.Join(parent, id[:pairDirLen])
	published := PublishedPair{
		ID: id, Dir: target,
		Gacha: filepath.Join(target, GachaFile), SeedBank: filepath.Join(target, SeedBankFile),
	}
	if _, err := os.Lstat(target); err == nil {
		return reusePair(published, gachaJSON, pair.SeedBank)
	} else if !os.IsNotExist(err) {
		return PublishedPair{}, fmt.Errorf("publish gacha pair: inspect %s: %w", target, err)
	}

	if err := os.MkdirAll(parent, 0o755); err != nil {
		return PublishedPair{}, fmt.Errorf("publish gacha pair: create %s: %w", parent, err)
	}
	staging, err := os.MkdirTemp(parent, ".pair-")
	if err != nil {
		return PublishedPair{}, fmt.Errorf("publish gacha pair: create staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if err := stagePair(staging, gachaJSON, pair); err != nil {
		return PublishedPair{}, fmt.Errorf("publish gacha pair: %w", err)
	}
	if err := os.Rename(staging, target); err != nil {
		// 另一個寫出者可能同時發布了同一份內容。
		if _, statErr := os.Lstat(target); statErr == nil {
			return reusePair(published, gachaJSON, pair.SeedBank)
		}
		return PublishedPair{}, fmt.Errorf("publish gacha pair: install %s: %w", target, err)
	}
	return published, nil
}

// encodeGachaPair 驗證輸入並回傳未壓縮的 gacha JSON（與載入器同一個型別）。
func encodeGachaPair(pair GachaPair) ([]byte, error) {
	g := legacyGacha{Picker: pair.Picker, SeedLen: pair.SeedLen}
	if err := validateLegacyGacha(&g); err != nil {
		return nil, err
	}
	if g.Picker.Size > math.MaxInt/g.SeedLen || len(pair.SeedBank) != g.Picker.Size*g.SeedLen {
		return nil, fmt.Errorf("seed bank size mismatch: got=%d want=%d×%d", len(pair.SeedBank), g.Picker.Size, g.SeedLen)
	}
	return json.Marshal(g)
}

// gachaPairID 以未壓縮的 gacha JSON 與 seed bank 計算內容身分，不受 zstd 編碼設定影響。
func gachaPairID(gachaJSON, bank []byte) string {
	h := sha256.New()
	_, _ = io.WriteString(h, gachaPairSchema)
	_, _ = fmt.Fprintf(h, "%d\n", len(gachaJSON))
	_, _ = h.Write(gachaJSON)
	_, _ = fmt.Fprintf(h, "%d\n", len(bank))
	_, _ = h.Write(bank)
	return hex.EncodeToString(h.Sum(nil))
}

// stagePair 在暫存目錄寫出兩個檔案，並以載入器讀回驗證。
func stagePair(staging string, gachaJSON []byte, pair GachaPair) error {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return fmt.Errorf("create zstd encoder: %w", err)
	}
	compressed := enc.EncodeAll(gachaJSON, nil)
	if err := enc.Close(); err != nil {
		return fmt.Errorf("close zstd encoder: %w", err)
	}
	if err := writeSynced(filepath.Join(staging, GachaFile), compressed); err != nil {
		return fmt.Errorf("write %s: %w", GachaFile, err)
	}
	if err := writeSynced(filepath.Join(staging, SeedBankFile), pair.SeedBank); err != nil {
		return fmt.Errorf("write %s: %w", SeedBankFile, err)
	}
	if err := verifyPair(staging, gachaJSON, pair.SeedBank); err != nil {
		return fmt.Errorf("verify staged pair: %w", err)
	}
	source, err := newDirSource(staging)
	if err != nil {
		return err
	}
	_, closeFn, _, err := loadGachaPair(source, GachaFile, SeedBankFile, pair.SeedLen, nil)
	if err != nil {
		return fmt.Errorf("load staged pair: %w", err)
	}
	if err := closeFn(); err != nil {
		return fmt.Errorf("release staged pair: %w", err)
	}
	// MkdirTemp 建立的目錄只有擁有者可讀；發布目錄與其他輸出一樣對所有人可讀。
	return os.Chmod(staging, 0o755)
}

func writeSynced(name string, data []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func reusePair(published PublishedPair, gachaJSON, bank []byte) (PublishedPair, error) {
	if err := verifyPair(published.Dir, gachaJSON, bank); err != nil {
		return PublishedPair{}, fmt.Errorf("publish gacha pair: %s exists with different content: %w", published.Dir, err)
	}
	published.Reused = true
	return published, nil
}

// verifyPair 檢查目錄中恰好有兩個檔案，且 gacha 解壓後的 JSON 與 seed bank 都和預期逐位元組相同。
func verifyPair(dir string, gachaJSON, bank []byte) error {
	if err := verifyFileSet(dir, map[string]struct{}{GachaFile: {}, SeedBankFile: {}}); err != nil {
		return err
	}
	compressed, err := os.ReadFile(filepath.Join(dir, GachaFile))
	if err != nil {
		return err
	}
	dec, err := zstd.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return fmt.Errorf("open %s: %w", GachaFile, err)
	}
	raw, err := io.ReadAll(dec)
	dec.Close()
	if err != nil {
		return fmt.Errorf("decompress %s: %w", GachaFile, err)
	}
	if !bytes.Equal(raw, gachaJSON) {
		return fmt.Errorf("%s content differs", GachaFile)
	}
	stored, err := os.ReadFile(filepath.Join(dir, SeedBankFile))
	if err != nil {
		return err
	}
	if !bytes.Equal(stored, bank) {
		return fmt.Errorf("%s content differs", SeedBankFile)
	}
	return nil
}

// verifyFileSet 檢查 dir 中恰好有 expected 列出的一般檔案，沒有子目錄或其他項目。
func verifyFileSet(dir string, expected map[string]struct{}) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	seen := 0
	for _, entry := range entries {
		if _, ok := expected[entry.Name()]; !ok {
			return fmt.Errorf("unexpected entry %q", entry.Name())
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("%q is not a regular file", entry.Name())
		}
		seen++
	}
	if seen != len(expected) {
		return fmt.Errorf("missing files: found %d of %d", seen, len(expected))
	}
	return nil
}

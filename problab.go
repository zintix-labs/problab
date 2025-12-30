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

// Package problab 提供 Problab 引擎的「組裝入口（assembler）」與「運行入口（runtime entry）」。
//
// 你可以把 Problab 視為一個「可被後端/模擬器使用的 runtime」，它負責把下列三個必需的地基組裝在一起，並提供建立 Machine 的入口：
//  1. Catalog：遊戲目錄（Single Source of Truth / SSOT），定義有哪些遊戲、各自對應的設定檔名稱（ConfigName）。
//  2. LogicRegistry：邏輯註冊表，提供「如何依據設定（LogicKey）建出遊戲邏輯」的 builders。
//  3. CoreFactory：亂數核心工廠（PRNG factory），保證可重現（reproducible）與可審計（auditable）。
//
// 設計重點：
//   - Problab 本身不綁定任何「檔案路徑」概念：設定檔來源一律以 fs.FS 的形式注入。
//   - Problab 會持有一份 Catalog（你要跑哪一批遊戲/設定檔）與一份 LogicRegistry（你支援哪些遊戲邏輯）。
//   - Machine 是對外提供 Spin 的最小單位；遊戲邏輯開發者（數學家）主要操作的是 sdk 內的型別與資料結構。
//
// 典型使用情境：
//   - 後端服務（HTTP / gRPC）：由 Problab 建立 Machine，Machine 對外提供 Spin。
//   - 模擬器（sim）：由 Problab 建立多台 Machine 進行大量模擬。
//
// 注意：此套引擎目前以 Slot 領域為中心（Spin -> Result），不是泛用遊戲框架。
package problab

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io/fs"
	"math"
	"math/big"
	"path/filepath"
	"strings"

	"github.com/zintix-labs/problab/catalog"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/spec"
)

// Configs 用來把一或多個設定檔來源（fs.FS）打包成 New() 需要的參數。
//
// 為什麼是 fs.FS：
//   - 你可以用 go:embed 把 configs 直接編進 binary（部署最穩定，不依賴工作目錄）。
//   - 也可以用 os.DirFS 在本機開發時讀取目錄。
//   - 甚至可以用自製的 MultiFS 來合併多個來源。
//
// Problab 不解析「路徑」：它只依賴 fs.FS + ConfigName（檔名）來取得設定內容。
func Configs(cfgs ...fs.FS) []fs.FS {
	return cfgs
}

// Logics 用來把一或多個邏輯註冊表（LogicRegistry）打包成 New() 需要的參數。
//
// 一個 LogicRegistry 代表「一個邏輯模組」提供的 builders 集合。
// 例如：
//   - basegame 模組提供 basegame 的 builder
//   - freegame 模組提供 freegame 的 builder
//
// New() 會把多個 registries 合併成單一 registry；若出現重複 LogicKey，會以 error 直接失敗（避免行為不確定）。
func Logics(regs ...*slot.LogicRegistry) []*slot.LogicRegistry {
	return regs
}

// Problab 是「組裝器（assembler）」與「運行入口（runtime entry）」：
//
// 它把三個必需的地基組合起來：
//  1. Catalog：遊戲目錄（Single Source of Truth / SSOT），定義有哪些遊戲、各自對應的設定檔名稱。
//  2. LogicRegistry：邏輯註冊表，提供「如何依據設定（LogicKey）建出遊戲邏輯」的 builders。
//  3. CoreFactory：亂數核心工廠（PRNG factory），保證可重現（reproducible）與可審計（auditable）。
//
// Problab 本身不綁定任何「檔案路徑」概念：設定檔來源一律由 fs.FS 提供。
//
// 使用流程通常分成兩階段：
//   - 註冊/組裝階段（registration/build）：建立 catalog、合併 registries、檢查重複與缺漏。
//   - 執行階段（runtime）：依據遊戲 ID 產生 Machine，並在 Machine 上執行 Spin。
//
// 重要設計原則：
//   - Catalog 的 ID 唯一性只保證在「同一個 Problab instance」內（不同 Problab 之間不做全域保證）。
//   - 你要跑哪一批遊戲、哪一套設定檔、哪一批邏輯，必須由 New() 的參數明確決定。
//   - runtime 一旦開始（例如已建立 Machine 並對外服務），不建議再變更 Catalog/Registry（避免非預期行為）。
//
// 實務例子（概念示意，細節依你的實作為準）：
//
//	// 1) 準備 configs（通常是 go:embed 或 DirFS）
//	// 2) 準備一或多個邏輯模組的 LogicRegistry
//	// 3) 組裝 Problab，取得可建立 Machine 的入口
//	//	lab, _ := problab.New(cf, problab.Configs(cfgFS), problab.Logics(reg1, reg2))
//	//	m, _ := lab.NewMachine(1001, false)
//	//	// m.Spin(...) -> 取得結果（通常再轉成 DTO 回傳）
type Problab struct {
	cat *catalog.Catalog
	reg *slot.LogicRegistry
	cf  core.CoreFactory
	sum []catalog.Summary
}

// New 建立一個 Problab instance。
//
// 這是「組裝階段（registration/build）」的入口：
//   - 會建立 Catalog（通常同時做檔名存在性/重複性檢查，避免 runtime 才爆）。
//   - 會合併多個 LogicRegistry 成為單一 registry（重複 LogicKey 直接視為錯誤）。
//   - 會保存 CoreFactory，確保由這個 Problab 建出來的 Machine 在 RNG 行為上具有一致性。
//
// 參數要求（是合約的一部分）：
//   - cf 不能為 nil：沒有 RNG 工廠就無法建立可重現/可審計的核心。
//   - cfgs 至少一個：沒有設定檔來源，Catalog 無法解析 GameSetting。
//   - logics 至少一個：沒有邏輯 builders，就算解析出設定也無法建出可執行的遊戲邏輯。
//
// 回傳的 Problab 會持有：cat（目錄）、reg（合併後 registry）、cf（RNG 工廠）。
func New(cf core.CoreFactory, cfgs []fs.FS, logics []*slot.LogicRegistry) (*Problab, error) {
	if cf == nil {
		return nil, errs.NewFatal("core factory required")
	}
	if len(cfgs) == 0 {
		return nil, errs.NewFatal("configs required")
	}
	if len(logics) == 0 {
		return nil, errs.NewFatal("logic registry required")
	}
	cata, err := catalog.New(cfgs...)
	if err != nil {
		return nil, err
	}
	reg, err := slot.MergeLogicRegistry(logics...)
	if err != nil {
		return nil, err
	}
	lab := &Problab{
		cat: cata,
		reg: reg,
		cf:  cf,
	}
	return lab, nil
}

// NewAuto 建立一個直接進入執行階段的 Problab instance。
//
// 回傳的 Problab 會持有：cat（目錄）、reg（合併後 registry）、cf（RNG 工廠）。
func NewAuto(cf core.CoreFactory, cfgs []fs.FS, logics []*slot.LogicRegistry) (*Problab, error) {
	lab, err := New(cf, cfgs, logics)
	if err != nil {
		return nil, err
	}
	if err := lab.RegisterAll(); err != nil {
		return nil, err
	}
	lab.Freeze()
	return lab, nil
}

func (p *Problab) Register(ents ...catalog.Entry) error {
	return p.cat.Register(ents...)
}

// RegisterAll
//
// 會掃描 catalog 持有的設定檔來源（fs.FS），把所有可辨識的設定檔（.yaml/.yml/.json）嘗試解析成
// *spec.GameSetting，並用設定檔內宣告的 GameID/GameName 產生對應的 catalog.Entry 來批次註冊。
//
// 行為特性（重要）：
//  1. Fail-fast：任何一個檔案讀取/解析/基本檢查失敗，都會立刻回傳 error（不會忽略、也不會繼續掃完）。
//  2. 原子性：只有當「全部檔案」都成功解析並通過基本檢查時，才會呼叫 Register(...) 一次性寫入。
//     因此不會出現只註冊了一半、導致 catalog 處於半完成狀態的情況。
//  3. 穩定性：會依檔名排序後再處理，確保行為 determinism（方便重現與除錯）。
//
// 注意：
//   - RegisterAll 只負責「把設定檔宣告的遊戲資訊放進 Catalog」。
//
// 遊戲邏輯（LogicBuilder / LogicRegistry）是否支援該 LogicKey，屬於後續 Problab 組裝/建機台時的責任。
func (p *Problab) RegisterAll() error {
	cfgs := p.cat.Cfg()
	sources := cfgs.Sources()
	if len(sources) == 0 {
		return errs.NewFatal("configs required")
	}

	entries := make([]catalog.Entry, 0, 64)
	seenID := map[spec.GID]string{}
	seenName := map[string]string{}

	for _, src := range sources {
		walkErr := fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path == "." {
					return nil
				}
				return errs.NewFatal(fmt.Sprintf("configs must be flat (no subdir): %q", path))
			}

			base := filepath.Base(path)
			if strings.Contains(path, "/") && path != base {
				return errs.NewFatal(fmt.Sprintf("configs must be flat (nested path): %q", path))
			}
			if strings.HasPrefix(base, ".") {
				return nil
			}

			ext := strings.ToLower(filepath.Ext(base))
			if ext != ".yaml" && ext != ".yml" && ext != ".json" {
				return nil
			}

			raw, rerr := fs.ReadFile(src, path)
			if rerr != nil {
				return errs.NewFatal(fmt.Sprintf("read config failed: %s", base))
			}

			var (
				gs   *spec.GameSetting
				gerr error
			)
			switch ext {
			case ".yaml", ".yml":
				gs, gerr = spec.GetGameSettingByYAML(raw)
			case ".json":
				gs, gerr = spec.GetGameSettingByJSON(raw)
			default:
				return errs.NewFatal(fmt.Sprintf("unsupported config format: %q", base))
			}
			if gerr != nil {
				return errs.NewFatal(fmt.Sprintf("parse gamesetting failed: %s", base))
			}

			name := strings.TrimSpace(gs.GameName)
			if name == "" {
				return errs.NewFatal(fmt.Sprintf("game name required: %s", base))
			}

			id := spec.GID(gs.GameID)
			if prev, ok := seenID[id]; ok {
				return errs.NewFatal(fmt.Sprintf("duplicate game id: %d (config=%s and %s)", id, prev, base))
			}
			if _, ok := p.cat.GetByID(id); ok {
				return errs.NewFatal(fmt.Sprintf("game id already registered: %d (config=%s)", id, base))
			}
			seenID[id] = base

			nameKey := strings.ToLower(name)
			if prev, ok := seenName[nameKey]; ok {
				return errs.NewFatal(fmt.Sprintf("duplicate game name: %s (config=%s and %s)", nameKey, prev, base))
			}
			if _, ok := p.cat.GetByName(name); ok {
				return errs.NewFatal(fmt.Sprintf("game name already registered: %s (config=%s)", name, base))
			}
			seenName[nameKey] = base

			if gs.LogicKey == "" {
				return errs.NewFatal(fmt.Sprintf("logic key required: %s", base))
			}
			if !p.reg.IsExist(gs.LogicKey) {
				return errs.NewFatal(fmt.Sprintf("logic not registered: logic_key=%s (config=%s)", gs.LogicKey, base))
			}

			entries = append(entries, catalog.Entry{
				GID:        id,
				Name:       name,
				ConfigName: base,
			})
			return nil
		})
		if walkErr != nil {
			return walkErr
		}
	}

	if len(entries) == 0 {
		return errs.NewFatal("no config files found to register")
	}

	return p.cat.Register(entries...)
}

func (p *Problab) Freeze() {
	p.cat.Freeze()
}

func (p *Problab) EntryById(id spec.GID) (catalog.Entry, bool) {
	return p.cat.GetByID(id)
}

func (p *Problab) EntryByName(name string) (catalog.Entry, bool) {
	return p.cat.GetByName(name)
}

func (p *Problab) IDs() []spec.GID {
	return p.cat.IDs()
}

func (p *Problab) All() []catalog.Entry {
	return p.cat.All()
}

func (p *Problab) Summary() ([]catalog.Summary, error) {
	if !p.cat.IsFrozen() {
		return nil, errs.NewFatal("catalog is not frozen yet")
	}
	if p.sum != nil {
		return p.sum, nil
	}
	ids := p.cat.IDs()
	cs := make([]catalog.Summary, 0, len(ids))
	for _, id := range ids {
		gs, err := p.cat.GameSettingById(id)
		if err != nil {
			return nil, errs.NewFatal("parse game setting failed")
		}
		s := catalog.Summary{
			GID:      id,
			Name:     gs.GameName,
			Logic:    gs.LogicKey,
			BetUnits: gs.BetUnits,
		}
		cs = append(cs, s)
	}
	p.sum = cs
	return p.sum, nil
}

// NewMachine 依據 Catalog 內的遊戲 ID 建立一台 Machine。
//
// 行為：
//  1. 由 Catalog 取得對應的 GameSetting（通常來自 fs.FS 內的 YAML/JSON）。
//  2. 以 CoreFactory 產生 RNG 核心（seed 由 crypto/rand 產生）。
//  3. 透過 LogicRegistry 依據 GameSetting 內的 LogicKey 建出可執行的遊戲邏輯。
//
// isSim 用於區分「模擬/分析」與「對外服務」的執行模式（例如：某些dto深拷貝行為可能只在 prod 開啟以增加 sim 的性能）。
//
// 注意：seed 會被記錄在 Machine 內（initseed），用於追溯/重現；真正的可審計能力以 Core 的 Snapshot/Restore 合約為準。
func (p *Problab) NewMachine(id spec.GID, isSim bool) (*Machine, error) {
	if !p.cat.IsFrozen() {
		return nil, errs.NewFatal("catalog is not frozen yet")
	}
	gs, err := p.cat.GameSettingById(id)
	if err != nil {
		return nil, err
	}
	return newMachine(gs, p.reg, p.cf, isSim)
}

// NewMachineWithSeed 與 NewMachine 相同，但由呼叫端指定初始 seed。
//
// 使用情境：
//   - 可重現的測試：同一份設定 + 同一個 seed，應產生一致的隨機序列（取決於 Core 實作）。
//
// 注意：seed 只是「出生入口」。若要在任意時間點完整重現，請使用 Core 的 Snapshot/Restore（以 []byte 交換狀態）。
func (p *Problab) NewMachineWithSeed(id spec.GID, seed int64, isSim bool) (*Machine, error) {
	if !p.cat.IsFrozen() {
		return nil, errs.NewFatal("catalog is not frozen yet")
	}
	gs, err := p.cat.GameSettingById(id)
	if err != nil {
		return nil, err
	}
	return newMachineWithSeed(gs, p.reg, p.cf, seed, isSim)
}

func (p *Problab) NewMachineByJSON(raw []byte, seed int64) (*Machine, error) {
	if !p.cat.IsFrozen() {
		return nil, errs.NewFatal("catalog is not frozen yet")
	}
	cfg, err := spec.GetGameSettingByJSON(raw)
	if err != nil {
		return nil, err
	}
	if err := p.validCfg(cfg); err != nil {
		return nil, err
	}
	return newMachineWithSeed(cfg, p.reg, p.cf, seed, true)
}

func (p *Problab) NewMachineByYAML(raw []byte, seed int64) (*Machine, error) {
	if !p.cat.IsFrozen() {
		return nil, errs.NewFatal("catalog is not frozen yet")
	}
	cfg, err := spec.GetGameSettingByYAML(raw)
	if err != nil {
		return nil, err
	}
	if err := p.validCfg(cfg); err != nil {
		return nil, err
	}
	return newMachineWithSeed(cfg, p.reg, p.cf, seed, true)
}

func (p *Problab) validCfg(cfg *spec.GameSetting) error {
	ent, ok := p.cat.GetByID(spec.GID(cfg.GameID))
	if !ok {
		return errs.NewWarn("gid not exist")
	}
	ent2, ok := p.cat.GetByName(cfg.GameName)
	if !ok {
		return errs.NewWarn("game name not exist")
	}
	if ent.GID != ent2.GID {
		return errs.NewWarn("game id is not matched game name")
	}
	if !p.reg.IsExist(cfg.LogicKey) {
		return errs.NewWarn("game logic not exist")
	}
	return nil
}

func (p *Problab) NewSimulator(id spec.GID) (*Simulator, error) {
	if !p.cat.IsFrozen() {
		return nil, errs.NewFatal("catalog is not frozen yet")
	}
	gs, err := p.cat.GameSettingById(id)
	if err != nil {
		return nil, err
	}
	return newSimulator(gs, p.reg, p.cf)
}

func (p *Problab) NewSimulatorWithSeed(id spec.GID, seed int64) (*Simulator, error) {
	if !p.cat.IsFrozen() {
		return nil, errs.NewFatal("catalog is not frozen yet")
	}
	gs, err := p.cat.GameSettingById(id)
	if err != nil {
		return nil, err
	}
	return newSimulatorWithSeed(gs, p.reg, p.cf, seed)
}

func (p *Problab) NewSimulatorByJSON(raw []byte, seed int64) (*Simulator, error) {
	if !p.cat.IsFrozen() {
		return nil, errs.NewFatal("catalog is not frozen yet")
	}
	cfg, err := spec.GetGameSettingByJSON(raw)
	if err != nil {
		return nil, err
	}
	if err := p.validCfg(cfg); err != nil {
		return nil, err
	}
	return newSimulatorWithSeed(cfg, p.reg, p.cf, seed)
}

func (p *Problab) NewSimulatorByYAML(raw []byte, seed int64) (*Simulator, error) {
	if !p.cat.IsFrozen() {
		return nil, errs.NewFatal("catalog is not frozen yet")
	}
	cfg, err := spec.GetGameSettingByYAML(raw)
	if err != nil {
		return nil, err
	}
	if err := p.validCfg(cfg); err != nil {
		return nil, err
	}
	return newSimulatorWithSeed(cfg, p.reg, p.cf, seed)
}

func (p *Problab) BuildRuntime(poolSize int) (*SlotRuntime, error) {
	// 1. 進入 runtime 前，catalog 必須 Freeze
	p.Freeze()

	ids := p.cat.IDs()
	if len(ids) == 0 {
		return nil, errs.NewFatal("no games registered")
	}

	rt := &SlotRuntime{
		pb:       p,
		pools:    make(map[spec.GID]*MachinePool, len(ids)),
		ids:      ids,
		done:     make(chan struct{}),
		poolSize: max(1, poolSize),
	}
	rt.reason.Store("")

	// 2. 先全建好（fail-fast + cleanup）
	for _, id := range ids {
		gs, err := p.cat.GameSettingById(id)
		if err != nil {
			return nil, err
		}

		seed, _ := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
		mp, err := newMachinePool(rt.poolSize, gs, p.reg, p.cf, seed.Int64())
		if err != nil {
			return nil, err
		}
		rt.pools[id] = mp
	}
	return rt, nil
}

// NewDevSimulator
//
// 注意只能由Problab起
// 只提供給Dev模式使用的模擬器，重點是保持單機台模式所以保持可重現性
func (p *Problab) NewDevSimulator(gid spec.GID, seed int64) (*DevSimulator, error) {
	sim, err := p.NewSimulatorWithSeed(gid, seed)
	if err != nil {
		return nil, err
	}
	m, err := p.NewMachineWithSeed(gid, seed, false)
	if err != nil {
		return nil, err
	}
	simBe, err := sim.mBuf[0].SnapshotCore()
	if err != nil {
		return nil, err
	}
	mBe, err := m.SnapshotCore()
	if err != nil {
		return nil, err
	}
	simBe64 := base64.StdEncoding.EncodeToString(simBe)
	mBe64 := base64.StdEncoding.EncodeToString(mBe)
	if mBe64 != simBe64 {
		return nil, errs.NewFatal("seeds are not equal")
	}
	dev := &DevSimulator{
		sim:      sim,
		m:        m,
		before:   mBe,
		before64: mBe64,
	}
	return dev, nil
}

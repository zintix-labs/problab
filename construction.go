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

package problab

import (
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/internal/optimalrt"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/spec"
)

// Seed 是建構 Machine／Simulator 時的根 seed material。
//
// 只能由 SeedBytes、SeedInt64、SeedString 產生；零值 Seed{} 表示「未提供」，
// 此時由 PRNG Factory 的 GenerateSeed 產生 seed。明確提供的空 material（例如
// SeedBytes(nil) 或 SeedString("")）是合法的 deterministic seed，與「未提供」不同。
//
// seed 是交給 Factory New(seed) 推導 PRNG 狀態的根材料，不是 PRNG 快照；
// 需要從精確狀態開始時，請改用 Machine.RestoreCore。
type Seed struct {
	provided bool
	material []byte
}

// SeedBytes 以原始位元組作為 seed material。呼叫時即複製 b。
func SeedBytes(b []byte) Seed {
	return Seed{provided: true, material: append([]byte(nil), b...)}
}

// SeedInt64 以 core.EncodeInt64Seed 的標準編碼作為 seed material。
func SeedInt64(n int64) Seed {
	return Seed{provided: true, material: core.EncodeInt64Seed(n)}
}

// SeedString 以字串未經修改的 UTF-8 位元組作為 seed material。
func SeedString(s string) Seed {
	return Seed{provided: true, material: []byte(s)}
}

type settingFormat uint8

const (
	settingCatalog settingFormat = iota
	settingJSON
	settingYAML
)

// SettingSource 描述遊戲設定的來源。零值表示使用 catalog 中該遊戲的設定；
// 自訂設定只能以 SettingFromJSON 或 SettingFromYAML 提供原始 bytes，
// 一律由 Problab 解析並完成設定驗證，不接受手工建立的 *spec.GameSetting。
type SettingSource struct {
	format settingFormat
	raw    []byte
}

// SettingFromJSON 以 JSON 原始設定作為來源。呼叫時即複製 raw。
func SettingFromJSON(raw []byte) SettingSource {
	return SettingSource{format: settingJSON, raw: append([]byte(nil), raw...)}
}

// SettingFromYAML 以 YAML 原始設定作為來源。呼叫時即複製 raw。
func SettingFromYAML(raw []byte) SettingSource {
	return SettingSource{format: settingYAML, raw: append([]byte(nil), raw...)}
}

// MachineConfig 是 NewMachine 與 NewSimulator 的建構設定。每個概念只有一個欄位，
// 零值即預設：未提供 seed、使用 catalog 設定、依設定使用 optimal。
type MachineConfig struct {
	Seed    Seed
	Setting SettingSource
	// Unoptimized 為 true 時忽略設定中的 optimal_setting，直接執行遊戲邏輯，
	// 不從已發布的 Optimal Artifact 抽 seed。optimizer 收集必須使用此選項。
	Unoptimized bool
}

// resolvedBuild 是新入口在建出 Machine 之前完成解析的結果。
type resolvedBuild struct {
	gs      *spec.GameSetting
	optimal *optimalrt.Artifact
	seed    []byte
}

// resolveBuild 依固定順序解析設定、optimal 與 seed：凍結檢查 → 取得設定（catalog，
// 或解析自訂設定並驗證）→ Unoptimized 或 resolveOptimal → 未提供 seed 時才產生 seed。
// 順序決定 entropy 讀取量，以及錯誤發生時是否已消耗 entropy，必須與舊入口一致。
func (p *Problab) resolveBuild(id spec.GID, cfg MachineConfig, product string) (resolvedBuild, error) {
	if !p.cat.IsFrozen() {
		return resolvedBuild{}, errs.NewFatal("catalog is not frozen yet")
	}
	var (
		gs  *spec.GameSetting
		err error
	)
	switch cfg.Setting.format {
	case settingCatalog:
		gs, err = p.cat.GameSettingById(id)
		if err != nil {
			return resolvedBuild{}, err
		}
	case settingJSON, settingYAML:
		if cfg.Setting.format == settingJSON {
			gs, err = spec.GetGameSettingByJSON(cfg.Setting.raw)
		} else {
			gs, err = spec.GetGameSettingByYAML(cfg.Setting.raw)
		}
		if err != nil {
			return resolvedBuild{}, err
		}
		if err := p.validCfg(gs); err != nil {
			return resolvedBuild{}, err
		}
		if gs.GameID != id {
			return resolvedBuild{}, errs.Warnf("setting game_id %d does not match requested game id %d", gs.GameID, id)
		}
	default:
		return resolvedBuild{}, errs.NewFatal("unknown setting source")
	}

	var optimal *optimalrt.Artifact
	if cfg.Unoptimized {
		// 複製 setting 並清空 optimal 設定，不動 catalog。
		raw := *gs
		raw.OptimalSetting = spec.OptimalSetting{}
		gs = &raw
	} else {
		optimal, err = p.resolveOptimal(gs)
		if err != nil {
			return resolvedBuild{}, err
		}
	}

	seed := cfg.Seed.material
	if !cfg.Seed.provided {
		seed, err = p.generateSeed()
		if err != nil {
			return resolvedBuild{}, errs.Wrap(err, "generate "+product+" seed")
		}
	}
	return resolvedBuild{gs: gs, optimal: optimal, seed: seed}, nil
}

// NewMachine 建立一台 deterministic、保留 Extend 的 Machine。
//
// 需要 Extend 的分析（tag、完整歷程、playerexp、optimizer）都使用 Machine；只需要
// 統計數字時請改用 NewSimulator。正式服務的 production Machine 只由 BuildRuntime
// 建立，公開 API 不提供單台 production Machine。
func (p *Problab) NewMachine(id spec.GID, cfg MachineConfig) (*Machine, error) {
	b, err := p.resolveBuild(id, cfg, "Machine")
	if err != nil {
		return nil, err
	}
	return newMachine(b.gs, p.reg, p.cf, b.seed, false, b.optimal, rngDeterministic)
}

// NewSimulator 建立一個 deterministic、省略 Extend 的 Simulator。Simulator 只產生
// 統計數字；需要 Extend 時請改用 NewMachine。
func (p *Problab) NewSimulator(id spec.GID, cfg MachineConfig) (*Simulator, error) {
	b, err := p.resolveBuild(id, cfg, "Simulator")
	if err != nil {
		return nil, err
	}
	return newSimulator(b.gs, p.reg, p.cf, b.seed, b.optimal)
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

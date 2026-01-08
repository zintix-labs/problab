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

package optimizer

import (
	"io/fs"
	"runtime"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/sampler"
	"github.com/zintix-labs/problab/spec"
	"gopkg.in/yaml.v3"
)

const biasWeight int = 1_000_000
const accuracy uint = uint(1) << 50
const maxTry int = 100_000
const mercy int = 100

// Sample 一個樣本點的資訊
//
// 注意：本優化器以「贏分 (Win)」作為第一性資料（整數、可精準比較/分類）。
// Multiplier 若需要，可由 Win/Bet 在執行期生成（避免浮點誤差污染分類與統計）。
type Sample struct {
	// 所屬的群組名稱 每個 sample 只會屬於一個群組
	CName string `parquet:"name=name, type=BYTE_ARRAY, convertedtype=UTF8"`
	// 下注分（credits）。若你的收集池是固定 bet，也可以在上層固定填同一個值。
	Bet float64 `parquet:"name=bet, type=FLOAT64"`
	// 贏分（credits）。在 Bet 對應的下注下，本局真實贏分。
	Win float64 `parquet:"name=win, type=FLOAT64"`
	// 精準回放用：核心快照（用於 replay / debug / deterministic reproduction）
	CoreSnap []byte `parquet:"name=snap, type=BYTE_ARRAY"`
}

type Tuner struct {
	cfg     *OptimizerSetting
	Classes []*Class
}

func New(cfg fs.FS, name string) (*Tuner, error) {
	raw, err := fs.ReadFile(cfg, name)
	if err != nil {
		return nil, err
	}
	opt, err := getOptimizerSettingByYaml(raw)
	if err != nil {
		return nil, err
	}
	tuner := &Tuner{
		cfg:     opt,
		Classes: make([]*Class, len(opt.Classes)),
	}
	for i := range len(opt.Classes) {
		c, err := newClass(opt.Classes[i])
		if err != nil {
			return nil, err
		}
		tuner.Classes[i] = c
	}
	return tuner, nil
}

func (t *Tuner) collect(gid spec.GID, betmode int, lab *problab.Problab) error {
	cpu := runtime.NumCPU()
	if cpu%2 == 1 {
		cpu++
	}
	// worker := cpu / 2

	// mBuf = make([]*problab.Machine, worker)

	// 在這裡收集[]Sample
	return nil
}

func (t *Tuner) Run() error {
	// 執行優化
	// 1. collect
	// 2. for Class
	// in class
	//  1) class 生成Basis(用shape產出足夠pos/neg)
	//  2) fitExp
	//  3) quality eval
	//  循環直到收滿
	// 3. 組合評分
	// 4. 結果存儲
	return nil
}

type Class struct {
	name  string
	cfg   *ClassSetting
	samps []Sample
	wins  []float64
	gener ShapeGenerator
	// fitExp func(*Basis, *core.Core) *Shape
	fail   int
	skew   []float64
	seeds  []byte
	shapes []*Shape // 最終結果
	isOK   bool
}

func newClass(cs *ClassSetting) (*Class, error) {
	if err := cs.validate(); err != nil {
		return nil, err
	}
	g, err := GetShapeGenerator(cs.ShapeCfg.Method, cs)
	if err != nil {
		return nil, err
	}
	c := &Class{
		name:  cs.CName,
		cfg:   cs,
		samps: nil,
		wins:  nil,
		gener: g,
		skew:  cs.QualityEval.MeanMedianRatio[:],
		isOK:  false,
	}
	return c, nil
}

// ----------------------------

type OptimizerSetting struct {
	Classes []*ClassSetting `yaml:"class_settings"`
}

// ClassSetting 一個分類
type ClassSetting struct {
	// 識別
	CName string `yaml:"class_name"`

	// 篩選規則
	MatchTags []string `yaml:"match_tags"` // 1. 特徵批配 ex: Trigger
	MinWin    int64    `yaml:"min_win"`    // 2. 最低贏分
	MaxWin    int64    `yaml:"max_win"`    // 3. 最高贏分
	Collect   int      `yaml:"collect"`    // 4. 目標收集數量

	// 觸發頻率：平均觸發局數。例如 119.5 代表平均 119.5 局觸發一次
	// <=0 代表「剩餘機率池」（只允許一個 class 使用此設定）
	HitFreq float64 `yaml:"hit_frequency"`

	// 本類目標期望贏分(尚未包含觸發率)。允許小數以描述期望值。
	ExpWin float64 `yaml:"exp_win"`

	Basis uint `yaml:"basis"`
	// 型態設定
	ShapeCfg *ShapeCfg `yaml:"shape_cfg"`

	// fit rtp + Normalization 使用的方法
	MatchExp *MatchExp `yaml:"match_exp"`

	// 品質評估
	QualityEval *QualityEvaluate `yaml:"quality_evaluate"`
}

type ShapeCfg struct {
	Method   string    `yaml:"method"`
	Gaussian *Gaussian `yaml:"gaussian"`
	Gamma    *Gamma    `yaml:"gamma"`
}

type Gaussian struct {
	KRange [2]int `yaml:"k_range"`

	MuCenter float64 `yaml:"mu_center"`
	MuStd    float64 `yaml:"mu_std"`

	StdRange [2]float64 `yaml:"std_range"`
	AmpRange [2]float64 `yaml:"amp_range"`
	// 可選：人為製造一個小峰值（極端值附近的微量質量），用於提升尾部體驗。
	// 若未設定或 mass_range 都是 0，則不啟用。
	// SpikeCfg 用於在分布上加入一個「微量峰值」（point-mass peak）。
	// 最終會以 convex blend 方式套用：w' = (1-mass)*w + mass*spikePoint
	//
	// 這裡刻意不暴露 range/style 等選項，以保持設定乾淨：
	// 啟用後，系統會自動在 wins 的「高尾端」隨機選一個點加上 mass。
	//
	// MassRange 建議很小，例如 [0.0005, 0.005] (0.05%~0.5%)。
	Spike *SpikeCfg `yaml:"spike"`

	Biases []Bias `yaml:"biases"`
}

type Bias struct {
	Range [2]float64 `yaml:"range"`
	Prob  int        `yaml:"prob"` // 基底100萬
}

type SpikeCfg struct {
	MassRange [2]float64 `yaml:"mass_range"`
}

type Gamma struct {
	KRange [2]int `yaml:"k_range"`

	MuCenter float64 `yaml:"mu_center"`
	MuStd    float64 `yaml:"mu_std"`

	StdRange [2]float64 `yaml:"std_range"`
	AmpRange [2]float64 `yaml:"amp_range"`
	// 可選：人為製造一個小峰值（極端值附近的微量質量），用於提升尾部體驗。
	// 若未設定或 mass_range 都是 0，則不啟用。
	// SpikeCfg 用於在分布上加入一個「微量峰值」（point-mass peak）。
	// 最終會以 convex blend 方式套用：w' = (1-mass)*w + mass*spikePoint
	//
	// 這裡刻意不暴露 range/style 等選項，以保持設定乾淨：
	// 啟用後，系統會自動在 wins 的「高尾端」隨機選一個點加上 mass。
	//
	// MassRange 建議很小，例如 [0.00002, 0.00005] (0.002%~0.005%)。
	Spike *SpikeCfg `yaml:"spike"`

	Biases []Bias `yaml:"biases"`
}

type MatchExp struct {
	Method string `yaml:"method"`
}

type QualityEvaluate struct {
	MeanMedianRatio [2]float64 `yaml:"mean_median_ratio"`
}

func getOptimizerSettingByYaml(data []byte) (*OptimizerSetting, error) {
	os := &OptimizerSetting{}
	if err := yaml.Unmarshal(data, os); err != nil {
		return nil, errs.Wrap(err, "failed to unmarshall yaml")
	}

	if len(os.Classes) == 0 {
		return nil, errs.NewWarn("optimizer setting is required")
	}

	for _, c := range os.Classes {
		if err := c.validate(); err != nil {
			return nil, errs.Warnf("class %s init error: %w", c.CName, err)
		}
	}

	return os, nil
}

// validate 檢查 Class 設定是否合理。
// 注意："<=0 代表剩餘池" 的唯一性需要由上層（整體配置）檢查，單一 Class 無法自我判斷。
func (c *ClassSetting) validate() error {
	if c.CName == "" {
		return errs.NewWarn("class: cid is required")
	}
	if c.MinWin < 0 {
		return errs.Warnf("class %s: min_win must be >= 0", c.CName)
	}
	if c.MaxWin < c.MinWin {
		return errs.Warnf("class %s: max_win must be >= min_win", c.CName)
	}
	if c.ExpWin < float64(c.MinWin) {
		return errs.Warnf("class %s: target_win must be >= min_win", c.CName)
	}
	if c.ExpWin > float64(c.MaxWin) {
		return errs.Warnf("class %s: target_win must be <= max_win", c.CName)
	}
	if c.Collect < 1 {
		return errs.Warnf("class %s: collect must be at least 1", c.CName)
	}
	// HitFreq <= 0 means remainder bucket (checked at higher level)
	if 0 < c.HitFreq && c.HitFreq <= 1 {
		return errs.Warnf("class %s: must not be between 0 and 1")
	}
	if c.Basis <= 0 {
		return errs.Warnf("class %s: basis must be at least 1", c.CName)
	}
	// --- ShapeCfg validation ---
	if c.ShapeCfg == nil {
		return errs.Warnf("class %s: shape_cfg is required", c.CName)
	}
	if c.ShapeCfg.Method == "" {
		return errs.Warnf("class %s: shape_cfg.method is required", c.CName)
	}
	switch c.ShapeCfg.Method {
	case "gaussian":
		if c.ShapeCfg.Gaussian == nil {
			return errs.Warnf("class %s: shape_cfg.gaussian is required for method gaussian", c.CName)
		}
	case "gamma":
		if c.ShapeCfg.Gamma == nil {
			return errs.Warnf("class %s: shape_cfg.gamma is required for method gamma", c.CName)
		}
	case "uniform":
		// no additional requirement
	default:
		return errs.Warnf("class %s: shape_cfg.method %s not supported", c.CName, c.ShapeCfg.Method)
	}
	return nil
}

// ---------------------------------

// Gacha 籤桶/抽卡
type Gacha struct {
	// 把各池按照比例混合後(各池內部權重*對應機率)計算出要取用第幾個種子的AliasTable
	GroupPicker *sampler.AliasTable
	SeedLen     int // 抽到對應第幾個種子，就要 * SeedLen 取[n*SeedLen:(n+1)*SeedLen]
}

// Validate 檢查 Gacha 設定是否合理。
func (g Gacha) Validate() error {
	if g.GroupPicker == nil {
		return errs.Warnf("gacha: GroupPicker is required")
	}
	if g.SeedLen <= 0 {
		return errs.Warnf("gacha: SeedLen must be > 0")
	}
	return nil
}

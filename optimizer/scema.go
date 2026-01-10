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
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/cheggaaa/pb/v3"
	"github.com/klauspost/compress/zstd"
	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/sampler"
	"github.com/zintix-labs/problab/spec"
	"gonum.org/v1/gonum/stat"
	"gopkg.in/yaml.v3"
)

const baseWeight int = 1_000_000
const accuracy uint = uint(1) << 52
const maxTry int = 10_000
const mercy int = 100
const maxMine int = 1_000_000_000
const epsilon float64 = 1e-12

// Sample 一個樣本點的資訊
//
// 注意：本優化器以「贏分 (Win)」作為第一性資料（整數、可精準比較/分類）。
// Multiplier 若需要，可由 Win/Bet 在執行期生成（避免浮點誤差污染分類與統計）。
type Sample struct {
	// 所屬的群組名稱 每個 sample 只會屬於一個群組
	CName string `parquet:"name=name, type=BYTE_ARRAY, convertedtype=UTF8"`
	// 單位化贏分（credits）。(bet=1)
	Win float64 `parquet:"name=win, type=FLOAT64"`
	// 精準回放用：核心快照（用於 replay / debug / deterministic reproduction）
	CoreSnap []byte `parquet:"name=snap, type=BYTE_ARRAY"`
}

type Tuner struct {
	cfg     *OptimizerSetting
	Classes []*Class
	tager   *Tagers
	tagBuf  []string
	seeds   *problab.SeedMaker
	rtp     float64
	std     float64
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
	if opt.TargetRTP < 0 {
		return nil, errs.Warnf("target_rtp must be non-negative float number")
	}
	if opt.TargetStd <= 0 {
		return nil, errs.Warnf("std must be postive float number")
	}
	tuner := &Tuner{
		cfg:     opt,
		Classes: make([]*Class, len(opt.Classes)),
		rtp:     opt.TargetRTP,
		std:     opt.TargetStd,
	}
	// p 是「剩餘機率池」（以 baseWeight=1_000_000 為分母的整數域）。
	// 目標：所有 class 的 prob 最終加總必須剛好等於 baseWeight，確保後續 class 抽樣沒有「落空區間」。
	// 規則：
	//   - 一般 class：prob > 0，會直接從 p 扣除。
	//   - remainder class：prob <= 0（配置上通常填 0），最多允許一個；最後會把剩餘的 p 全部補給它。
	//   - 若設定檔沒有 remainder class，本建構流程不會自動補齊（會留下 p>0），屬於配置錯誤；
	//     若你希望「必定補齊到 1,000,000」，請在設定檔明確提供一個 hit_prob<=0 的 remainder class。
	p := baseWeight
	foundzero := false
	pos := 0
	tag := make([]string, 0, 10)
	for i := range len(opt.Classes) {
		c, err := newClass(opt.Classes[i])
		if err != nil {
			return nil, err
		}
		// remainder class：以 hit_prob<=0 表示（配置上通常寫 0）。
		// 只允許最多一個；該 class 會在最後吃掉剩餘機率，讓總和精確回到 baseWeight。
		if c.prob <= 0 {
			if foundzero {
				return nil, errs.Warnf("hit_prob err: you can only set one zero")
			}
			foundzero = true
			c.prob = 0
			pos = i
		}
		// 從剩餘機率池扣除本 class 的機率；若扣到負數代表總和超過 baseWeight（配置錯誤）。
		p -= c.prob
		if p < 0 {
			return nil, errs.Warnf("err : sum of class hit_prob > %d", baseWeight)
		}
		tuner.Classes[i] = c
		if len(c.tags) > 0 {
			for _, t := range c.tags {
				if len(tag) == 0 {
					tag = append(tag, t)
					continue
				}
				dup := false
				for _, g := range tag {
					if g == t {
						dup = true
						break
					}
				}
				if !dup {
					tag = append(tag, t)
				}
			}
		}
	}
	// 若存在 remainder class，將剩餘的 p 一次性補給它，
	// 使得所有 class 的 prob 加總剛好等於 baseWeight（避免 class 抽樣出現誤差/落空區間）。
	if foundzero {
		tuner.Classes[pos].prob = p
	}
	if !foundzero && p != 0 {
		return nil, errs.Warnf("sum of hit_prob must be %d", baseWeight)
	}
	tuner.tagBuf = make([]string, 0, len(tag))
	tuner.tager = GetTager(tag...)
	return tuner, nil
}

func (t *Tuner) collect(gid spec.GID, betmode int, lab *problab.Problab, seed int64) error {
	if _, ok := lab.EntryById(gid); !ok {
		return errs.Warnf("gid not found: %d", gid)
	}
	if betmode < 0 {
		return errs.Warnf("betmode must be non-negative: %d", betmode)
	}
	summary, err := lab.Summary()
	if err != nil {
		return err
	}
	m, err := lab.NewMachineWithSeed(gid, seed, false)
	if err != nil {
		return err
	}
	bet := float64(0)
	for _, s := range summary {
		if gid == s.GID {
			if betmode >= len(s.BetUnits) {
				return errs.Warnf("betmode must be less than %d: %d", len(s.BetUnits), betmode)
			}
			bet = float64(s.BetUnits[betmode])
			break
		}
	}
	fullCount := 0
	for _, c := range t.Classes {
		fullCount += c.collect
	}

	bar := pb.StartNew(maxMine)
	bar.Set(pb.CleanOnFinish, true)
	for range maxMine {
		snap, _ := m.SnapshotCore()
		sr := m.SpinInternal(betmode)
		// TagInto 會回傳新的 slice header（長度可能改變），必須接回來才能確保 tagBuf 內容正確。
		t.tagBuf = t.tager.TagInto(sr, t.tagBuf)
		for _, c := range t.Classes {
			if (len(c.samps) < c.collect) && (sr.TotalWin >= c.minWin) && (sr.TotalWin <= c.maxWin) && sub(c.tags, t.tagBuf) {
				c.samps = append(c.samps, Sample{
					CName:    c.name,
					Win:      float64(sr.TotalWin) / bet,
					CoreSnap: snap,
				})
				if len(c.samps) >= c.collect {
					c.collected()
				}
				fullCount--
				// 下一輪 Spin
				break
			}
		}
		if fullCount <= 0 {
			break
		}
		bar.Increment()
	}
	bar.Finish()
	for _, c := range t.Classes {
		if len(c.samps) < c.collect {
			return errs.Warnf("class %s is not full: want %d got %d", c.name, c.collect, len(c.samps))
		}
	}
	return nil
}

func (t *Tuner) Run(gid spec.GID, betmode int, lab *problab.Problab, seed int64) error {
	seeds := problab.NewSeedMaker(seed)
	// 執行優化
	// 1. collect
	fmt.Println("step1: collect")
	if err := t.collect(gid, betmode, lab, seeds.Next()); err != nil {
		return err
	}
	// 2. By Class
	core, err := lab.NewCore(seeds.Next())
	if err != nil {
		return err
	}
	fmt.Println("step2: class")
	for _, class := range t.Classes {
		fmt.Printf("\rclass %s", class.name)
		// in class
		//  1) class 生成Basis(用shape產出足夠pos/neg)
		fmt.Printf("\rclass %s: make basis...", class.name)
		base, err := class.MakeBasis(core)
		if err != nil {
			return err
		}

		count := 0
		for {
			//  2) fitExp
			shape := class.fitOneOnOne(base, core)
			if shape == nil {
				fmt.Printf("\r.")
			}
			//  3) quality eval
			if (shape != nil) && class.qualityEval(shape) {
				count = 0
				class.shapes = append(class.shapes, shape)
				//  循環直到收滿
				if len(class.shapes) >= class.shapesCollect {
					fmt.Printf("\r")
					break
				}
			}
			count++
			if count%100 == 0 {
				fmt.Printf("\rclass %s: try %d", class.name, count)
			}
		}
		if count >= maxTry {
			return errs.Warnf("class %s shapes not collect full", class.name)
		}
	}
	// 3. 組合評分
	fmt.Printf("step3: final eval")
	ga, snap := t.Eval(core)
	if ga == nil {
		return errs.Warnf("can not find matched")
	}
	// 4. 結果存儲
	if err := t.Save(ga, snap); err != nil {
		return err
	}
	return nil
}

func (t *Tuner) Eval(c *core.Core) (*Gacha, []byte) {
	classProbs := make([]int, len(t.Classes))
	startIdx := make([]int, len(t.Classes))
	count := 0
	seedLen := len(t.Classes[0].samps[0].CoreSnap)
	for i, class := range t.Classes {
		classProbs[i] = class.prob
		startIdx[i] = count
		count += len(class.samps)
	}
	wins := make([]float64, 0, count)
	seeds := make([]byte, 0, count*seedLen)
	for _, class := range t.Classes {
		wins = append(wins, class.wins...)
		seeds = append(seeds, class.seeds...)
	}
	try := 0
	rtpdiff := 0.005
	stdscale := 0.1
	for {
		weights := make([]float64, 0, count)
		for _, class := range t.Classes {
			id := c.IntN(len(class.shapes))
			shape := class.shapes[id]
			for _, w := range shape.Weights {
				weights = append(weights, w*float64(class.prob)/float64(baseWeight))
			}
		}
		mean, std := stat.PopMeanStdDev(wins, weights)
		if mean > (t.rtp-rtpdiff) && mean < (t.rtp+rtpdiff) && (std > (1-stdscale)*t.std) && (std < (1+stdscale)*t.std) {
			// normalize
			normalAT := sampler.BuildAliasTable(quantizeWeights(weights))
			return &Gacha{
				Picker:  normalAT,
				SeedLen: seedLen,
			}, seeds
		}
		try++
		if try%100 == 0 {
			stdscale += 0.1
			rtpdiff += 0.05
			fmt.Printf("\rstep 3: final eval try %d -> mean:%f %f %f, std:%f %f %f", try, (t.rtp - rtpdiff), mean, (t.rtp + rtpdiff), (1-stdscale)*t.std, std, (1+stdscale)*t.std)
		}
		if try >= 10*maxTry {
			break
		}
	}
	return nil, nil
}

func (t *Tuner) Save(gc *Gacha, snap []byte) error {
	if gc == nil {
		return errs.Warnf("save: gacha is nil")
	}
	if err := gc.Validate(); err != nil {
		return errs.Wrap(err, "save: invalid gacha")
	}
	if len(snap) == 0 {
		return errs.Warnf("save: snap is empty")
	}

	// Output directory (dev-friendly default): ./build/optimizer
	outDir := filepath.Join("build", "optimizer")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return errs.Wrap(err, "save: mkdir output dir")
	}

	// 1) Save gacha as JSON then zstd-compress into gacha.json.zst
	jsonBytes, err := json.Marshal(gc)
	if err != nil {
		return errs.Wrap(err, "save: marshal gacha json")
	}
	gachaPath := filepath.Join(outDir, "gacha.json.zst")
	f, err := os.Create(gachaPath)
	if err != nil {
		return errs.Wrap(err, "save: create gacha.json.zst")
	}
	defer func() { _ = f.Close() }()

	zw, err := zstd.NewWriter(f)
	if err != nil {
		return errs.Wrap(err, "save: create zstd writer")
	}
	if _, err := zw.Write(jsonBytes); err != nil {
		_ = zw.Close()
		return errs.Wrap(err, "save: write gacha.json.zst")
	}
	if err := zw.Close(); err != nil {
		return errs.Wrap(err, "save: close zstd writer")
	}
	if err := f.Close(); err != nil {
		return errs.Wrap(err, "save: close gacha.json.zst")
	}

	// 2) Save snaps as raw bin
	snapPath := filepath.Join(outDir, "snaps.bin")
	if err := os.WriteFile(snapPath, snap, 0o644); err != nil {
		return errs.Wrap(err, "save: write snaps.bin")
	}

	// 3) Optional: quick sanity check that gacha can be read back (in-memory)
	// This is dev-only correctness guard; cheap for typical sizes.
	zr, err := zstd.NewReader(bytes.NewReader(mustReadFile(gachaPath)))
	if err != nil {
		return errs.Wrap(err, "save: verify zstd reader")
	}
	zr.Close()

	return nil
}

func mustReadFile(path string) []byte {
	b, _ := os.ReadFile(path)
	return b
}

type Class struct {
	name          string
	cfg           *ClassSetting
	samps         []Sample
	wins          []float64
	gener         ShapeGenerator
	prob          int
	fail          int
	skew          []float64
	seeds         []byte
	tags          []string
	shapes        []*Shape // 最終結果
	minWin        int
	maxWin        int
	collect       int
	shapesCollect int
	isOK          bool
}

func (c *Class) collected() {
	if len(c.samps) >= c.collect {
		sort.Slice(c.samps, func(i, j int) bool {
			return c.samps[i].Win < c.samps[j].Win
		})
		c.wins = c.wins[:0]
		c.seeds = c.seeds[:0]
		for _, s := range c.samps {
			c.wins = append(c.wins, s.Win)
			c.seeds = append(c.seeds, s.CoreSnap...)
		}
	}
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
		name:          cs.CName,
		cfg:           cs,
		prob:          int(cs.HitProb),
		samps:         make([]Sample, 0, cs.Collect),
		wins:          make([]float64, 0, cs.Collect),
		seeds:         make([]byte, 0, cs.Collect*24),
		shapes:        make([]*Shape, 0, cs.ShapesCollect),
		gener:         g,
		skew:          cs.QualityEval.MeanMedianRatio[:],
		tags:          cs.MatchTags,
		minWin:        int(cs.MinWin),
		maxWin:        int(cs.MaxWin),
		collect:       cs.Collect,
		isOK:          false,
		shapesCollect: cs.ShapesCollect,
	}
	return c, nil
}

// ----------------------------

type OptimizerSetting struct {
	TargetRTP float64         `yaml:"trget_rtp"`
	TargetStd float64         `yaml:"trget_std"`
	Classes   []*ClassSetting `yaml:"class_settings"`
}

// ClassSetting 一個分類
type ClassSetting struct {
	// 識別
	CName string `yaml:"class_name"`

	// 篩選規則
	MatchTags []string `yaml:"match_tags"` // 1. 特徵批配 ex: Trigger
	MinWin    float64  `yaml:"min_win"`    // 2. 最低贏倍
	MaxWin    float64  `yaml:"max_win"`    // 3. 最高贏倍
	Collect   int      `yaml:"collect"`    // 4. 目標收集數量

	// 底數為100萬的機率
	// 只有允許一個0（代表剩餘機率都給他）
	HitProb uint `yaml:"hit_prob"`

	// 本類目標期望贏分(尚未包含觸發率)。允許小數以描述期望值。
	ExpWin float64 `yaml:"exp_win"`

	Basis uint `yaml:"basis"`
	// 型態設定
	ShapeCfg *ShapeCfg `yaml:"shape_cfg"`

	// fit rtp + Normalization 使用的方法
	MatchExp *MatchExp `yaml:"match_exp"`

	// 品質評估
	QualityEval   *QualityEvaluate `yaml:"quality_evaluate"`
	ShapesCollect int              `yaml:"shapes_collect"` // 本class要的數量
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

	StdRange  [2]float64 `yaml:"std_range"`
	AmpRange  [2]float64 `yaml:"amp_range"`
	ZeroRange [2]float64 `yaml:"zero_range"`
	// 可選：人為製造一個小峰值（極端值附近的微量質量），用於提升尾部體驗。
	// 若未設定或 mass_range 都是 0，則不啟用。
	// SpikeCfg 用於在分布上加入一個「微量峰值」（point-mass peak）。
	// 這裡刻意不暴露 style 選項，以保持設定乾淨：
	// 啟用後，系統會在 wins 的「指定區間」隨機選一個點加上 mass。
	//
	// MassRange 建議很小，例如 [0.0001, 0.0003] (0.01%~0.03%)。
	Spike *SpikeCfg `yaml:"spike"`

	Biases []Bias `yaml:"biases"`
}

type Bias struct {
	Range [2]float64 `yaml:"range"`
	Prob  int        `yaml:"prob"` // 基底100萬
}

type SpikeCfg struct {
	MassRange [2]float64 `yaml:"mass_range"`
	WinRange  [2]float64 `yaml:"win_range"`
}

type Gamma struct {
	KRange [2]int `yaml:"k_range"`

	MuCenter float64 `yaml:"mu_center"`
	MuStd    float64 `yaml:"mu_std"`

	StdRange  [2]float64 `yaml:"std_range"`
	AmpRange  [2]float64 `yaml:"amp_range"`
	ZeroRange [2]float64 `yaml:"zero_range"`
	// 可選：人為製造一個小峰值（極端值附近的微量質量），用於提升尾部體驗。
	// 若未設定或 mass_range 都是 0，則不啟用。
	// SpikeCfg 用於在分布上加入一個「微量峰值」（point-mass peak）。
	// 這裡刻意不暴露 style 選項，以保持設定乾淨：
	// 啟用後，系統會在 wins 的「指定區間」隨機選一個點加上 mass。
	//
	// MassRange 建議很小，例如 [0.0001, 0.0003] (0.01%~0.03%)。
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
			return nil, errs.Warnf("class %s init error: %s", c.CName, err.Error())
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
	if c.HitProb > uint(baseWeight) {
		return errs.Warnf("class %s: hit_prob must be less than %d", c.CName, baseWeight)
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
	if c.ShapesCollect <= 0 {
		return errs.Warnf("class %s: shapes_collect must be at least 1", c.CName)
	}
	return nil
}

// ---------------------------------

// Gacha 籤桶/抽卡
type Gacha struct {
	// 把各池按照比例混合後(各池內部權重*對應機率)計算出要取用第幾個種子的AliasTable
	Picker  *sampler.AliasTable `json:"picker"`
	SeedLen int                 `json:"seed_len"` // 抽到對應第幾個種子，就要 * SeedLen 取[n*SeedLen:(n+1)*SeedLen]
}

func (g *Gacha) Pick(c *core.Core) (start int, end int) {
	s := g.Picker.Pick(c)
	start = s * g.SeedLen
	end = start + g.SeedLen
	return
}

// Validate 檢查 Gacha 設定是否合理。
func (g Gacha) Validate() error {
	if g.Picker == nil {
		return errs.Warnf("gacha: GroupPicker is required")
	}
	if g.SeedLen <= 0 {
		return errs.Warnf("gacha: SeedLen must be > 0")
	}
	return nil
}

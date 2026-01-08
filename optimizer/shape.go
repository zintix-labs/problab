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
	"math"

	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/sampler"
	"gonum.org/v1/gonum/stat/distuv"
)

var shapes = map[string]func(*ClassSetting) (ShapeGenerator, error){
	"gaussian": NewGaussianMixtureShapeGenerator,
	"gamma":    NewGammaMixtureShapeGenerator,
	"uniform":  NewUniformShapeGenerator,
}

func GetShapeGenerator(method string, cs *ClassSetting) (ShapeGenerator, error) {
	if fn, ok := shapes[method]; ok {
		return fn(cs)
	}
	return nil, errs.Warnf("class %s get shape generator err: method %s not found", cs.CName, method)
}

type ShapeGenerator interface {
	Gen([]float64, *core.Core) *Shape // returns w
}

type Shape struct {
	Weights []float64 // len == len(wins), sum=1
	Mean    float64   // E[win]
	Median  float64   // median
}

// --------------------------------------

type GaussianShape struct {
	Amp float64
	Mu  float64
	Std float64
}

type GaussianMixtureShapeGenerator struct {
	cs *ClassSetting

	KMin int
	KMax int

	// mu sampling controls
	MuCenter float64 // use ExpWin
	MuStd    float64 // how wide the mus spread around MuCenter

	// std range
	StdMin float64
	StdMax float64

	// amp range (positive)
	AmpMin float64
	AmpMax float64

	SpikeOn    bool
	SpikeRange [2]float64

	Biases    []Bias
	BiasAlias *sampler.AliasTable
}

func NewGaussianMixtureShapeGenerator(cs *ClassSetting) (ShapeGenerator, error) {
	cfg := cs.ShapeCfg.Gaussian
	if cfg == nil {
		return nil, errs.NewWarn("shape cfg gaussian is required")
	}
	if cfg.MuCenter < float64(cs.MinWin) {
		return nil, errs.Warnf("class %s shape cfg err: mu_center must be at least min win", cs.CName)
	}
	if cfg.MuCenter > float64(cs.MaxWin) {
		return nil, errs.Warnf("class %s shape cfg err: mu_center must be less than max win", cs.CName)
	}
	if cfg.KRange[1] < cfg.KRange[0] {
		return nil, errs.Warnf("class %s shape cfg err: k_range[0] must be less than k_range[1]", cs.CName)
	}
	if cfg.KRange[0] < 1 {
		return nil, errs.Warnf("class %s shape cfg err: k_range[0] must be at least 1", cs.CName)
	}
	if cfg.StdRange[1] < cfg.StdRange[0] {
		return nil, errs.Warnf("class %s shape cfg err: std_range[0] must be less than std_range[1]", cs.CName)
	}
	if cfg.AmpRange[1] < cfg.AmpRange[0] {
		return nil, errs.Warnf("class %s shape cfg err: amp_range[0] must be less than amp_range[1]", cs.CName)
	}
	gm := &GaussianMixtureShapeGenerator{
		cs:       cs,
		KMin:     cfg.KRange[0],
		KMax:     cfg.KRange[1],
		MuCenter: cfg.MuCenter,
		MuStd:    cfg.MuStd,
		StdMin:   cfg.StdRange[0],
		StdMax:   cfg.StdRange[1],
		AmpMin:   cfg.AmpRange[0],
		AmpMax:   cfg.AmpRange[1],
	}
	if cfg.Spike != nil {
		if cfg.Spike.MassRange[0] > cfg.Spike.MassRange[1] {
			return nil, errs.Warnf("class %s shape cfg err: spike[0] must be less than spike[1]", cs.CName)
		}
		gm.SpikeOn = true
		gm.SpikeRange = cfg.Spike.MassRange
	}
	mainWeight := biasWeight
	weights := make([]int, 0, len(cfg.Biases)+1)
	biasess := make([]Bias, 0, len(cfg.Biases))
	if len(cfg.Biases) != 0 {
		for _, b := range cfg.Biases {
			if b.Prob > mainWeight {
				return nil, errs.Warnf("class %s shape cfg err: biases prob over %d", cs.CName, biasWeight)
			}
			if b.Range[0] > b.Range[1] {
				return nil, errs.Warnf("class %s shape cfg err: biases range[1] must be grater than range[0]", cs.CName)
			}
			if b.Range[0] < float64(cs.MinWin) {
				return nil, errs.Warnf("class %s shape cfg err: biases range[0] must be at least min win", cs.CName)
			}
			if b.Range[1] > float64(cs.MaxWin) {
				return nil, errs.Warnf("class %s shape cfg err: biases range[1] must be less than max win", cs.CName)
			}
			mainWeight -= b.Prob
			if b.Prob > 0 {
				weights = append(weights, b.Prob)
				biasess = append(biasess, b)
			}
		}
	}
	weights = append(weights, mainWeight)
	gm.Biases = biasess
	gm.BiasAlias = sampler.BuildAliasTable(weights)

	return gm, nil
}

func (g *GaussianMixtureShapeGenerator) Gen(wins []float64, c *core.Core) *Shape {
	// NOTE: wins 必須事先由呼叫端排序（ascending）。為了性能，這裡不做任何 runtime assert。
	if len(wins) == 0 {
		return nil
	}
	// 從範圍取得本次要混合的GaussianShape數量
	k := g.KMin
	if g.KMax > g.KMin {
		k = g.KMin + c.IntN(g.KMax-g.KMin+1)
	}

	gauss := make([]GaussianShape, 0, k)
	for i := 0; i < k; i++ {
		// --- Mu: centered around MuCenter (natural) ---
		pick := g.BiasAlias.Pick(c)
		center := g.MuCenter
		if pick < len(g.Biases) {
			start := g.Biases[pick].Range[0]
			maxrange := g.Biases[pick].Range[1] - start
			center = start + c.Float64()*maxrange
		}
		// mu = center + N(0,1)*MuStd
		mu := center + c.NormFloat64()*g.MuStd

		// optional: clamp mu into [minWin, maxWin] to avoid useless gauss
		mu = max(mu, float64(g.cs.MinWin))
		mu = min(mu, float64(g.cs.MaxWin))

		// --- Std: uniform in [StdMin, StdMax] ---
		std := g.StdMin + c.Float64()*(g.StdMax-g.StdMin)
		if std <= 1e-9 {
			std = 1e-9
		}

		// --- Amp: uniform in [AmpMin, AmpMax] ---
		amp := g.AmpMin + c.Float64()*(g.AmpMax-g.AmpMin)
		if amp <= 0 {
			amp = 1e-9
		}

		gauss = append(gauss, GaussianShape{Amp: amp, Mu: mu, Std: std})
	}

	weights := make([]float64, len(wins))
	var sumW float64
	for i, x := range wins {
		var w float64
		for _, b := range gauss {
			w += b.Amp * normalPDF(x, b.Mu, b.Std)
		}
		weights[i] = w
		sumW += w
	}

	// fallback: if all weights ~0, revert to uniform
	if !(sumW > 0) || math.IsNaN(sumW) || math.IsInf(sumW, 0) {
		u := 1.0 / float64(len(weights))
		for i := range weights {
			weights[i] = u
		}
		return &Shape{Weights: weights, Mean: meanOf(wins, weights), Median: medianOf(wins, weights)}
	}

	// normalize to probability distribution
	inv := 1.0 / sumW
	for i := range weights {
		weights[i] *= inv
	}

	// Optional: add a small, human-made peak near the tail (spike)
	if g.SpikeOn {
		applySpike(g.SpikeRange, wins, weights, c)
	}

	return &Shape{
		Weights: weights,
		Mean:    meanOf(wins, weights),
		Median:  medianOf(wins, weights),
	}
}

func normalPDF(x, mu, std float64) float64 {
	// 1/(std*sqrt(2*pi)) * exp(-0.5*((x-mu)/std)^2)
	z := (x - mu) / std
	return (1.0 / (std * math.Sqrt(2*math.Pi))) * math.Exp(-0.5*z*z)
}

// ---------------------------------

type GammaShape struct {
	Amp float64
	Mu  float64
	Std float64
}

type GammaMixtureShapeGenerator struct {
	cs *ClassSetting

	KMin int
	KMax int

	// mu sampling controls
	MuCenter float64 // use ExpWin
	MuStd    float64 // how wide the mus spread around MuCenter

	// std range
	StdMin float64
	StdMax float64

	// amp range (positive)
	AmpMin float64
	AmpMax float64

	SpikeOn    bool
	SpikeRange [2]float64

	Biases    []Bias
	BiasAlias *sampler.AliasTable
}

func NewGammaMixtureShapeGenerator(cs *ClassSetting) (ShapeGenerator, error) {
	cfg := cs.ShapeCfg.Gamma
	if cfg == nil {
		return nil, errs.NewWarn("shape cfg gamma is required")
	}
	if cfg.MuCenter < float64(cs.MinWin) {
		return nil, errs.Warnf("class %s shape cfg err: mu_center must be at least min win", cs.CName)
	}
	if cfg.MuCenter > float64(cs.MaxWin) {
		return nil, errs.Warnf("class %s shape cfg err: mu_center must be less than max win", cs.CName)
	}
	if cfg.KRange[1] < cfg.KRange[0] {
		return nil, errs.Warnf("class %s shape cfg err: k_range[0] must be less than k_range[1]", cs.CName)
	}
	if cfg.KRange[0] < 1 {
		return nil, errs.Warnf("class %s shape cfg err: k_range[0] must be at least 1", cs.CName)
	}
	if cfg.StdRange[1] < cfg.StdRange[0] {
		return nil, errs.Warnf("class %s shape cfg err: std_range[0] must be less than std_range[1]", cs.CName)
	}
	if cfg.AmpRange[1] < cfg.AmpRange[0] {
		return nil, errs.Warnf("class %s shape cfg err: amp_range[0] must be less than amp_range[1]", cs.CName)
	}
	gm := &GammaMixtureShapeGenerator{
		cs:       cs,
		KMin:     cfg.KRange[0],
		KMax:     cfg.KRange[1],
		MuCenter: cfg.MuCenter,
		MuStd:    cfg.MuStd,
		StdMin:   cfg.StdRange[0],
		StdMax:   cfg.StdRange[1],
		AmpMin:   cfg.AmpRange[0],
		AmpMax:   cfg.AmpRange[1],
	}
	if cfg.Spike != nil {
		if cfg.Spike.MassRange[0] > cfg.Spike.MassRange[1] {
			return nil, errs.Warnf("class %s shape cfg err: spike[0] must be less than spike[1]", cs.CName)
		}
		gm.SpikeOn = true
		gm.SpikeRange = cfg.Spike.MassRange
	}
	mainWeight := biasWeight
	weights := make([]int, 0, len(cfg.Biases)+1)
	biasess := make([]Bias, 0, len(cfg.Biases))
	if len(cfg.Biases) != 0 {
		for _, b := range cfg.Biases {
			if b.Prob > mainWeight {
				return nil, errs.Warnf("class %s shape cfg err: biases prob over %d", cs.CName, biasWeight)
			}
			if b.Range[0] > b.Range[1] {
				return nil, errs.Warnf("class %s shape cfg err: biases range[1] must be grater than range[0]", cs.CName)
			}
			if b.Range[0] < float64(cs.MinWin) {
				return nil, errs.Warnf("class %s shape cfg err: biases range[0] must be at least min win", cs.CName)
			}
			if b.Range[1] > float64(cs.MaxWin) {
				return nil, errs.Warnf("class %s shape cfg err: biases range[1] must be less than max win", cs.CName)
			}
			mainWeight -= b.Prob
			if b.Prob > 0 {
				weights = append(weights, b.Prob)
				biasess = append(biasess, b)
			}
		}
	}
	weights = append(weights, mainWeight)
	gm.Biases = biasess
	gm.BiasAlias = sampler.BuildAliasTable(weights)

	return gm, nil
}

func (g *GammaMixtureShapeGenerator) Gen(wins []float64, c *core.Core) *Shape {
	// NOTE: wins 必須事先由呼叫端排序（ascending）。為了性能，這裡不做任何 runtime assert。
	if len(wins) == 0 {
		return nil
	}
	// 從範圍取得本次要混合的GammaShape數量
	k := g.KMin
	if g.KMax > g.KMin {
		k = g.KMin + c.IntN(g.KMax-g.KMin+1)
	}

	shape := make([]GammaShape, 0, k)
	for i := 0; i < k; i++ {
		// --- Mu: centered around MuCenter (natural) ---
		pick := g.BiasAlias.Pick(c)
		center := g.MuCenter
		if pick < len(g.Biases) {
			start := g.Biases[pick].Range[0]
			maxrange := g.Biases[pick].Range[1] - start
			center = start + c.Float64()*maxrange
		}
		// mu = center + N(0,1)*MuStd
		mu := center + c.NormFloat64()*g.MuStd

		// optional: clamp mu into [minWin, maxWin] to avoid useless shape
		mu = max(mu, float64(g.cs.MinWin))
		mu = min(mu, float64(g.cs.MaxWin))
		if mu <= 0 {
			mu = 1e-6 // guard
		}

		// --- Std: uniform in [StdMin, StdMax] ---
		std := g.StdMin + c.Float64()*(g.StdMax-g.StdMin)
		if std <= 1e-9 {
			std = 1e-9
		}

		// --- Amp: uniform in [AmpMin, AmpMax] ---
		amp := g.AmpMin + c.Float64()*(g.AmpMax-g.AmpMin)
		if amp <= 0 {
			amp = 1e-9
		}

		shape = append(shape, GammaShape{Amp: amp, Mu: mu, Std: std})
	}

	weights := make([]float64, len(wins))
	gma := distuv.Gamma{Src: c} // use core
	var sumW float64
	for i, x := range wins {
		var w float64
		for _, b := range shape {
			alpha := (b.Mu / b.Std) * (b.Mu / b.Std) // alpha = (mu/std)^2
			// guard alpha must be over 1.2
			alpha = max(1.2, alpha)
			beta := alpha / b.Mu // keep mean
			gma.Alpha = alpha
			gma.Beta = beta
			w += b.Amp * gma.Prob(x) // PDF
		}
		weights[i] = w
		sumW += w
	}

	// fallback: if all weights ~0, revert to uniform
	if !(sumW > 0) || math.IsNaN(sumW) || math.IsInf(sumW, 0) {
		u := 1.0 / float64(len(weights))
		for i := range weights {
			weights[i] = u
		}
		return &Shape{Weights: weights, Mean: meanOf(wins, weights), Median: medianOf(wins, weights)}
	}

	// normalize to probability distribution
	inv := 1.0 / sumW
	for i := range weights {
		weights[i] *= inv
	}

	// Optional: add a small, human-made peak near the tail (spike)
	if g.SpikeOn {
		applySpike(g.SpikeRange, wins, weights, c)
	}

	return &Shape{
		Weights: weights,
		Mean:    meanOf(wins, weights),
		Median:  medianOf(wins, weights),
	}
}

// -----------------------------------

func NewUniformShapeGenerator(cs *ClassSetting) (ShapeGenerator, error) {
	return &UniformShapeGenerator{}, nil
}

type UniformShapeGenerator struct{}

func (u *UniformShapeGenerator) Gen(wins []float64, c *core.Core) *Shape {
	// NOTE: wins 必須事先由呼叫端排序（ascending）。為了性能，這裡不做任何 runtime assert。
	if len(wins) == 0 {
		return nil
	}

	weights := make([]float64, len(wins))
	w := 1.0 / float64(len(wins))
	for i := range wins {
		weights[i] = w
	}
	return &Shape{
		Weights: weights,
		Mean:    meanOf(wins, weights),
		Median:  medianOf(wins, weights),
	}
}

// -------------

func quantizeWeights(probs []float64) []int {
	// Convert probs (sum≈1) into integer weights that quantize with base accuracy, sum may be < accuracy
	// NOTE: assumes `accuracy` fits into int on 64-bit platforms.
	if len(probs) == 0 {
		return nil
	}
	base := int(accuracy)
	ws := make([]int, len(probs))
	sum := 0
	for i, p := range probs {
		if p < 0 {
			p = 0
		}
		w := int(math.Floor(p * float64(base)))
		if w < 0 {
			w = 0
		}
		ws[i] = w
		sum += w
	}

	if sum == 0 {
		// fallback uniform
		for i := range ws {
			ws[i] = 1
		}
	}
	// no fix diff
	return ws
}

func applySpike(spike [2]float64, wins, weights []float64, c *core.Core) {
	// mass range sanity
	m0, m1 := spike[0], spike[1]
	if m0 < 0 {
		m0 = 0
	}
	if m1 < 0 {
		m1 = 0
	}
	if m1 < m0 {
		m0, m1 = m1, m0
	}
	if m1 == 0 {
		return
	}

	// pick mass
	mass := m0
	if m1 > m0 {
		mass = m0 + c.Float64()*(m1-m0)
	}
	if mass <= 0 {
		return
	}
	if mass >= 1 {
		mass = 0.999999
	}

	// Auto upper-tail window (wins are sorted ascending by contract)
	// We intentionally keep this logic config-free to avoid extra knobs.
	// TailFrac = top 5% of points (at least 1 point).
	const tailFrac = 0.05
	if len(wins) == 0 {
		return
	}

	l := int(float64(len(wins)) * (1.0 - tailFrac))
	if l < 0 {
		l = 0
	}
	if l >= len(wins) {
		l = len(wins) - 1
	}
	r := len(wins) - 1
	if l > r {
		return
	}

	// First shrink the base distribution by (1-mass)
	baseScale := 1.0 - mass
	for i := range weights {
		weights[i] *= baseScale
	}

	// choose a random point in [l,r]
	idx := l
	if r > l {
		idx = l + c.IntN(r-l+1)
	}
	weights[idx] += mass

	// Note: weights remain normalized (sum remains 1) by construction.
}

func meanOf(wins, probs []float64) float64 {
	var m float64
	for i := range wins {
		m += wins[i] * probs[i]
	}
	return m
}

func medianOf(wins, probs []float64) float64 {
	// Assumes wins are sorted ascending and probs sum to 1.
	var acc float64
	for i := range wins {
		acc += probs[i]
		if acc >= 0.5 {
			return wins[i]
		}
	}
	// Fallback: due to rounding, return the last win.
	if len(wins) == 0 {
		return 0
	}
	return wins[len(wins)-1]
}

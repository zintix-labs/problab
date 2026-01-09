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
	Set([]float64) bool
	Gen(*core.Core) (*Shape, error) // returns w
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

	// zero range
	ZeroMin float64
	ZeroMax float64

	SpikeOn        bool
	SpikeMassRange [2]float64
	SpikeWinRange  [2]float64

	Biases    []Bias
	BiasAlias *sampler.AliasTable

	// Set
	isSet    bool
	zeros    int       // 有幾個0
	wins     []float64 // 贏分
	failed   int
	spikeidx []int
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
	if cfg.ZeroRange[1] < cfg.ZeroRange[0] {
		return nil, errs.Warnf("class %s shape cfg err: zero_range[0] must be less than zero_range[1]", cs.CName)
	}
	if cfg.ZeroRange[0] < 0.0 {
		return nil, errs.Warnf("class %s shape cfg err: zero_range[0] must be non-negative", cs.CName)
	}
	if cfg.ZeroRange[1] > 1.0 {
		return nil, errs.Warnf("class %s shape cfg err: zero_range[1] must less than 1.0", cs.CName)
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
		ZeroMin:  cfg.ZeroRange[0],
		ZeroMax:  cfg.ZeroRange[1],
		isSet:    false,
	}
	if cfg.Spike != nil {
		if cfg.Spike.MassRange[0] > cfg.Spike.MassRange[1] {
			return nil, errs.Warnf("class %s shape cfg spike err: mass[0] must be less than mass[1]", cs.CName)
		}
		if cfg.Spike.MassRange[0] < 0 {
			return nil, errs.Warnf("class %s shape cfg spike err: mass[0] must be non-negative", cs.CName)
		}
		if cfg.Spike.MassRange[1] > 1.0 {
			return nil, errs.Warnf("class %s shape cfg spike err: mass[0] must be less than 1.0", cs.CName)
		}
		if cfg.Spike.MassRange[1]+gm.ZeroMax > 1.0 {
			return nil, errs.Warnf("class %s shape cfg spike err: mass[1] + zero_max must be less than 1.0", cs.CName)
		}
		if cfg.Spike.WinRange[0] > cfg.Spike.WinRange[1] {
			return nil, errs.Warnf("class %s shape cfg spike err: win[0] must be less than win[1]", cs.CName)
		}
		gm.SpikeOn = true
		gm.SpikeMassRange = cfg.Spike.MassRange
		gm.SpikeWinRange = cfg.Spike.WinRange
	}
	mainWeight := baseWeight
	weights := make([]int, 0, len(cfg.Biases)+1)
	biasess := make([]Bias, 0, len(cfg.Biases))
	if len(cfg.Biases) != 0 {
		for _, b := range cfg.Biases {
			if b.Prob > mainWeight {
				return nil, errs.Warnf("class %s shape cfg err: biases prob over %d", cs.CName, baseWeight)
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

func (g *GaussianMixtureShapeGenerator) Set(wins []float64) bool {
	// NOTE: wins 必須事先由呼叫端排序（ascending）。為了性能，這裡不做任何 runtime assert。
	if g.isSet {
		return false
	}
	if len(wins) == 0 {
		return false
	}
	zeros := 0
	for _, w := range wins {
		if w > 0.0 {
			break
		}
		zeros++
	}
	if g.SpikeOn {
		index := make([]int, 0, 10)
		smin := g.SpikeWinRange[0]
		smax := g.SpikeWinRange[1]
		for i := len(wins) - 1; i >= zeros; i-- {
			w := wins[i]
			if (w <= smax) && (w >= smin) {
				index = append(index, i)
			}
		}
		if len(index) == 0 {
			g.SpikeOn = false
		}
		g.spikeidx = index
	}
	if zeros == 0 {
		g.ZeroMin = 0.0
		g.ZeroMax = 0.0
	}
	g.zeros = zeros
	g.wins = wins
	g.isSet = true
	return true
}

func (g *GaussianMixtureShapeGenerator) Gen(c *core.Core) (*Shape, error) {
	if !g.isSet {
		return nil, errs.Warnf("set wins required")
	}

	// 1. 0分率(選用):從範圍取得本次0分率
	zeroRate := 0.0
	zeroRatePerZeroWin := 0.0
	weights := make([]float64, len(g.wins))
	if g.ZeroMax > 0.0 && (g.zeros > 0) {
		diffrange := g.ZeroMax - g.ZeroMin
		zeroRate = g.ZeroMin + c.Float64()*diffrange
		// 均攤
		zeroRatePerZeroWin = zeroRate / float64(g.zeros)
		for i := range weights[:g.zeros] {
			weights[i] = zeroRatePerZeroWin
		}
	}

	// 2. Spike(選用): add a small, human-made peak near the tail (spike)
	spikeProb := 0.0
	spikeIdx := 0
	if g.SpikeOn && (len(g.spikeidx) > 0) {
		diff := g.SpikeMassRange[1] - g.SpikeMassRange[0]
		spikeProb = g.SpikeMassRange[0] + c.Float64()*diff
		spikeIdx = c.Pick(g.spikeidx) // 從列表中隨機挑一個
	}
	remain := 1.0 - zeroRate - spikeProb
	if remain > epsilon {
		if g.zeros >= len(g.wins) {
			return nil, errs.Warnf(
				"class=%s method=%s invalid_support: remain=%.12g (>eps=%.12g) but wins have no non-zero bins (zeros=%d wins_len=%d, win_range=[%.6g,%.6g]). "+
					"Meaning: config requires mixture mass, but there is nowhere to place it. "+
					"Fix: reduce zero_range/spike_mass so remain<=eps, or provide non-zero wins (wins must include >0 values).",
				g.cs.CName, "gaussian", remain, epsilon,
				g.zeros, len(g.wins),
				g.wins[0], g.wins[len(g.wins)-1],
			)
		}
		// 3. 從範圍取得本次要混合的GaussianShape數量
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

		var sumW float64
		for i := g.zeros; i < len(g.wins); i++ {
			x := g.wins[i]
			var w float64
			for _, b := range gauss {
				w += b.Amp * normalPDF(x, b.Mu, b.Std)
			}
			weights[i] = w
			sumW += w
		}

		// fallback: if all weights ~0, try again
		if !(sumW > 0) || math.IsNaN(sumW) || math.IsInf(sumW, 0) {
			g.failed++
			if g.failed > 500 {
				return nil, errs.Warnf(
					"class=%s method=gaussian gen_failed(retry_limit) failed=%d/%d wins_len=%d zeros=%d win_range=[%.6g,%.6g] first_nonzero=%.6g zero_range=[%.6g,%.6g] zero_rate=%.6g spike_on=%t spike_range=[%.6g,%.6g] spike_prob=%.6g k_range=[%d,%d] mu_center=%.6g mu_std=%.6g std_range=[%.6g,%.6g] amp_range=[%.6g,%.6g] biases=%d",
					g.cs.CName, g.failed, 500,
					len(g.wins), g.zeros,
					g.wins[0], g.wins[len(g.wins)-1],
					func() float64 {
						if g.zeros < len(g.wins) {
							return g.wins[g.zeros]
						}
						return g.wins[len(g.wins)-1]
					}(),
					g.ZeroMin, g.ZeroMax, zeroRate,
					g.SpikeOn, g.SpikeMassRange[0], g.SpikeMassRange[1], spikeProb,
					g.KMin, g.KMax,
					g.MuCenter, g.MuStd,
					g.StdMin, g.StdMax,
					g.AmpMin, g.AmpMax,
					len(g.Biases),
				)
			}
			return g.Gen(c)
		}

		// normalize to probability distribution
		inv := remain / sumW
		for i := g.zeros; i < len(weights); i++ {
			weights[i] *= inv
		}
	}

	// 這時後spike 把 spikeProb 加上去
	if g.SpikeOn && (spikeProb > 0.0) {
		weights[spikeIdx] += spikeProb
	}

	g.failed = 0
	return &Shape{
		Weights: weights,
		Mean:    meanOf(g.wins, weights),
		Median:  medianOf(g.wins, weights),
	}, nil
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

	// zero range
	ZeroMin float64
	ZeroMax float64

	SpikeOn        bool
	SpikeMassRange [2]float64
	SpikeWinRange  [2]float64

	Biases    []Bias
	BiasAlias *sampler.AliasTable

	// Set
	isSet    bool
	zeros    int       // 有幾個0
	wins     []float64 // 贏分
	failed   int
	spikeidx []int
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
	if cfg.ZeroRange[1] < cfg.ZeroRange[0] {
		return nil, errs.Warnf("class %s shape cfg err: zero_range[0] must be less than zero_range[1]", cs.CName)
	}
	if cfg.ZeroRange[0] < 0.0 {
		return nil, errs.Warnf("class %s shape cfg err: zero_range[0] must be non-negative", cs.CName)
	}
	if cfg.ZeroRange[1] > 1.0 {
		return nil, errs.Warnf("class %s shape cfg err: zero_range[1] must less than 1.0", cs.CName)
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
		ZeroMin:  cfg.ZeroRange[0],
		ZeroMax:  cfg.ZeroRange[1],
		isSet:    false,
	}
	if cfg.Spike != nil {
		if cfg.Spike.MassRange[0] > cfg.Spike.MassRange[1] {
			return nil, errs.Warnf("class %s shape cfg spike err: mass[0] must be less than mass[1]", cs.CName)
		}
		if cfg.Spike.MassRange[0] < 0 {
			return nil, errs.Warnf("class %s shape cfg spike err: mass[0] must be non-negative", cs.CName)
		}
		if cfg.Spike.MassRange[1] > 1.0 {
			return nil, errs.Warnf("class %s shape cfg spike err: mass[0] must be less than 1.0", cs.CName)
		}
		if cfg.Spike.MassRange[1]+gm.ZeroMax > 1.0 {
			return nil, errs.Warnf("class %s shape cfg spike err: mass[1] + zero_max must be less than 1.0", cs.CName)
		}
		if cfg.Spike.WinRange[0] > cfg.Spike.WinRange[1] {
			return nil, errs.Warnf("class %s shape cfg spike err: win[0] must be less than win[1]", cs.CName)
		}
		gm.SpikeOn = true
		gm.SpikeMassRange = cfg.Spike.MassRange
		gm.SpikeWinRange = cfg.Spike.WinRange
	}
	mainWeight := baseWeight
	weights := make([]int, 0, len(cfg.Biases)+1)
	biasess := make([]Bias, 0, len(cfg.Biases))
	if len(cfg.Biases) != 0 {
		for _, b := range cfg.Biases {
			if b.Prob > mainWeight {
				return nil, errs.Warnf("class %s shape cfg err: biases prob over %d", cs.CName, baseWeight)
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

func (g *GammaMixtureShapeGenerator) Set(wins []float64) bool {
	// NOTE: wins 必須事先由呼叫端排序（ascending）。為了性能，這裡不做任何 runtime assert。
	if g.isSet {
		return false
	}
	if len(wins) == 0 {
		return false
	}
	zeros := 0
	for _, w := range wins {
		if w > 0.0 {
			break
		}
		zeros++
	}
	if g.SpikeOn {
		index := make([]int, 0, 10)
		smin := g.SpikeWinRange[0]
		smax := g.SpikeWinRange[1]
		for i := len(wins) - 1; i >= zeros; i-- {
			w := wins[i]
			if (w <= smax) && (w >= smin) {
				index = append(index, i)
			}
		}
		if len(index) == 0 {
			g.SpikeOn = false
		}
		g.spikeidx = index
	}
	if zeros == 0 {
		g.ZeroMin = 0.0
		g.ZeroMax = 0.0
	}
	g.zeros = zeros
	g.wins = wins
	g.isSet = true
	return true
}

func (g *GammaMixtureShapeGenerator) Gen(c *core.Core) (*Shape, error) {
	if !g.isSet {
		return nil, errs.Warnf("set wins required")
	}

	// 1. 0分率(選用):從範圍取得本次0分率
	zeroRate := 0.0
	zeroRatePerZeroWin := 0.0
	weights := make([]float64, len(g.wins))
	if g.ZeroMax > 0.0 && (g.zeros > 0) {
		diffrange := g.ZeroMax - g.ZeroMin
		zeroRate = g.ZeroMin + c.Float64()*diffrange
		// 均攤
		zeroRatePerZeroWin = zeroRate / float64(g.zeros)
		for i := range weights[:g.zeros] {
			weights[i] = zeroRatePerZeroWin
		}
	}

	// 2. Spike(選用): add a small, human-made peak near the tail (spike)
	spikeProb := 0.0
	spikeIdx := 0
	if g.SpikeOn && (len(g.spikeidx) > 0) {
		diff := g.SpikeMassRange[1] - g.SpikeMassRange[0]
		spikeProb = g.SpikeMassRange[0] + c.Float64()*diff
		spikeIdx = c.Pick(g.spikeidx) // 從列表中隨機挑一個
	}
	remain := 1.0 - zeroRate - spikeProb
	if remain > epsilon {
		if g.zeros >= len(g.wins) {
			return nil, errs.Warnf(
				"class=%s method=%s invalid_support: remain=%.12g (>eps=%.12g) but wins have no non-zero bins (zeros=%d wins_len=%d, win_range=[%.6g,%.6g]). "+
					"Meaning: config requires mixture mass, but there is nowhere to place it. "+
					"Fix: reduce zero_range/spike_mass so remain<=eps, or provide non-zero wins (wins must include >0 values).",
				g.cs.CName, "gamma", remain, epsilon,
				g.zeros, len(g.wins),
				g.wins[0], g.wins[len(g.wins)-1],
			)
		}
		// 3. 從範圍取得本次要混合的GammaShape數量
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

		gma := distuv.Gamma{Src: c} // use core
		var sumW float64
		for i, x := range g.wins[g.zeros:] {
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
			weights[g.zeros+i] = w
			sumW += w
		}

		// fallback: if all weights ~0, try again
		if !(sumW > 0) || math.IsNaN(sumW) || math.IsInf(sumW, 0) {
			g.failed++
			if g.failed > 500 {
				return nil, errs.Warnf(
					"class=%s method=gamma gen_failed(retry_limit) failed=%d/%d wins_len=%d zeros=%d win_range=[%.6g,%.6g] first_nonzero=%.6g zero_range=[%.6g,%.6g] zero_rate=%.6g spike_on=%t spike_range=[%.6g,%.6g] spike_prob=%.6g k_range=[%d,%d] mu_center=%.6g mu_std=%.6g std_range=[%.6g,%.6g] amp_range=[%.6g,%.6g] biases=%d",
					g.cs.CName, g.failed, 500,
					len(g.wins), g.zeros,
					g.wins[0], g.wins[len(g.wins)-1],
					func() float64 {
						if g.zeros < len(g.wins) {
							return g.wins[g.zeros]
						}
						return g.wins[len(g.wins)-1]
					}(),
					g.ZeroMin, g.ZeroMax, zeroRate,
					g.SpikeOn, g.SpikeMassRange[0], g.SpikeMassRange[1], spikeProb,
					g.KMin, g.KMax,
					g.MuCenter, g.MuStd,
					g.StdMin, g.StdMax,
					g.AmpMin, g.AmpMax,
					len(g.Biases),
				)
			}
			return g.Gen(c)
		}

		// normalize to probability distribution
		inv := remain / sumW
		for i := g.zeros; i < len(weights); i++ {
			weights[i] *= inv
		}
	}

	// 這時後spike 把 spikeProb 加上去
	if g.SpikeOn && (spikeProb > 0.0) {
		weights[spikeIdx] += spikeProb
	}

	g.failed = 0 // 失敗次數歸零
	return &Shape{
		Weights: weights,
		Mean:    meanOf(g.wins, weights),
		Median:  medianOf(g.wins, weights),
	}, nil
}

// -----------------------------------

func NewUniformShapeGenerator(cs *ClassSetting) (ShapeGenerator, error) {
	return &UniformShapeGenerator{}, nil
}

type UniformShapeGenerator struct {
	isSet bool
	wins  []float64
}

func (u *UniformShapeGenerator) Set(wins []float64) bool {
	// NOTE: wins 必須事先由呼叫端排序（ascending）。為了性能，這裡不做任何 runtime assert。
	if u.isSet {
		return false
	}
	if len(wins) == 0 {
		return false
	}
	u.wins = wins
	u.isSet = true
	return true
}

func (u *UniformShapeGenerator) Gen(c *core.Core) (*Shape, error) {
	if !u.isSet {
		return nil, errs.Warnf("set wins required")
	}

	weights := make([]float64, len(u.wins))
	w := 1.0 / float64(len(u.wins))
	for i := range u.wins {
		weights[i] = w
	}
	return &Shape{
		Weights: weights,
		Mean:    meanOf(u.wins, weights),
		Median:  medianOf(u.wins, weights),
	}, nil
}

// -------------

func quantizeWeights(probs []float64) []int {
	// Convert probs into integer weights that quantize with base accuracy
	// First normalize probs to sum=1, then quantize with dynamic accuracy to avoid overflow
	if len(probs) == 0 {
		return nil
	}

	// 1. Normalize weights to sum=1
	sum := 0.0
	for _, p := range probs {
		if p > 0 {
			sum += p
		}
	}

	if sum <= 0 {
		// fallback uniform
		ws := make([]int, len(probs))
		for i := range ws {
			ws[i] = 1
		}
		return ws
	}

	// 2. Calculate dynamic accuracy to avoid overflow in BuildAliasTable
	// BuildAliasTable checks: total * n <= math.MaxInt64
	// where total = sum of quantized weights, n = len(probs)
	// We want: accuracy * n <= math.MaxInt64
	// So: accuracy <= math.MaxInt64 / n
	n := len(probs)
	maxSafeAccuracy := int64(math.MaxInt64) / int64(n)

	// Use the smaller of the configured accuracy and the safe accuracy
	base := int(accuracy) / n
	if base < 1 {
		base = 1
	}
	if int64(base) > maxSafeAccuracy {
		base = int(maxSafeAccuracy)
		// Ensure base is at least 1
		if base < 1 {
			base = 1
		}
	}

	// 3. Quantize normalized weights
	ws := make([]int, len(probs))
	for i, p := range probs {
		if p < 0 {
			p = 0
		}
		// Normalize first, then quantize
		normalized := p / sum
		w := int(math.Floor(normalized * float64(base)))
		if w < 0 {
			w = 0
		}
		ws[i] = w
	}

	// 4. Ensure at least one non-zero weight
	hasNonZero := false
	for _, w := range ws {
		if w > 0 {
			hasNonZero = true
			break
		}
	}
	if !hasNonZero {
		// fallback uniform
		for i := range ws {
			ws[i] = 1
		}
	}

	return ws
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

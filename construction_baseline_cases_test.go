// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/spec"
)

// ---------------------------------------------------------------------------
// 環境組合（techspec §6.2）
// ---------------------------------------------------------------------------

// baselineCombo 是一組「PRNG Factory × optimal 來源 × 可用遊戲」。optimal
// fixture 依遊戲、bet units 與 PRNG snapshot format 配對：現有 fixture 只有
// demo_0（bet unit 40）的 ChaCha20 manifest、PCG64 manifest 與 ChaCha20 legacy。
type baselineCombo struct {
	Name    string
	Factory string // chacha | pcg
	Optimal string // off | manifest | legacy
	Games   []spec.GID
}

var baselineCombos = []baselineCombo{
	{Name: "off-chacha", Factory: "chacha", Optimal: "off", Games: []spec.GID{0, 1, fixtureGameID}},
	{Name: "off-pcg", Factory: "pcg", Optimal: "off", Games: []spec.GID{0, 1, fixtureGameID}},
	{Name: "manifest-chacha", Factory: "chacha", Optimal: "manifest", Games: []spec.GID{0}},
	{Name: "manifest-pcg", Factory: "pcg", Optimal: "manifest", Games: []spec.GID{0}},
	{Name: "legacy-chacha", Factory: "chacha", Optimal: "legacy", Games: []spec.GID{0}},
}

func (c baselineCombo) factory() core.PRNGFactory {
	if c.Factory == "pcg" {
		return core.PCG64()
	}
	f, err := core.NewChaCha20Factory(newDetReader("reseed:" + c.Name))
	if err != nil {
		panic(err)
	}
	return f
}

func (c baselineCombo) configFS(t testing.TB) fs.FS {
	t.Helper()
	switch c.Optimal {
	case "manifest":
		return testManifestConfigFS(t)
	case "legacy":
		return testLegacyConfigFS(t)
	}
	files := fstest.MapFS{"fixture.yaml": &fstest.MapFile{Data: fixtureConfigYAML(t)}}
	for _, name := range []string{"game_0_demonormal.yaml", "game_1_democascade.yaml"} {
		raw, err := fs.ReadFile(demo_configs.FS, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		files[name] = &fstest.MapFile{Data: raw}
	}
	return files
}

func (c baselineCombo) optimalFS(t testing.TB) fs.FS {
	t.Helper()
	switch {
	case c.Optimal == "manifest" && c.Factory == "pcg":
		return testPCGManifestFS(t)
	case c.Optimal == "manifest":
		return testManifestFS(t, false)
	case c.Optimal == "legacy":
		return testOptimalFS(t)
	}
	return nil
}

// newBaselineLab 每次都建立全新的 Lab，seed entropy 與 reseed entropy 都從頭開始，
// 因此同一案例的每次建構都消耗相同的 entropy。
func newBaselineLab(t testing.TB, c baselineCombo, freeze bool) (*Problab, *detReader, error) {
	t.Helper()
	entropy := newDetReader("seed:" + c.Name)
	opts := []ProblabOption{WithSeedEntropy(entropy)}
	if ofs := c.optimalFS(t); ofs != nil {
		opts = append(opts, WithOptimalFS(ofs))
	}
	cfgs := Configs(c.configFS(t))
	logics := Logics(demo_logic.Logics, fixtureLogics)
	if !freeze {
		lab, err := New(c.factory(), cfgs, logics, opts...)
		return lab, entropy, err
	}
	lab, err := NewAuto(c.factory(), cfgs, logics, opts...)
	return lab, entropy, err
}

// rawConfig 回傳該組合中某遊戲的原始 YAML，供 ByYAML 與 ByJSON 案例使用。
func (c baselineCombo) rawConfig(t testing.TB, id spec.GID) []byte {
	t.Helper()
	cfg := c.configFS(t)
	entries, err := fs.ReadDir(cfg, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		raw, err := fs.ReadFile(cfg, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		gs, err := spec.GetGameSettingByYAML(raw)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		if gs.GameID == id {
			return raw
		}
	}
	t.Fatalf("combo %s has no game %d", c.Name, id)
	return nil
}

func yamlToJSON(t testing.TB, raw []byte) []byte {
	t.Helper()
	gs, err := spec.GetGameSettingByYAML(raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(gs)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// ---------------------------------------------------------------------------
// seed 形式
// ---------------------------------------------------------------------------

type seedArg struct {
	Label string
	Int   int64
	Bytes []byte
	Str   string
}

var (
	seedFixed32 = bytes.Repeat([]byte{0xa5, 0x5a, 0x3c, 0xc3}, 8)
	seedInts    = []seedArg{{Label: "int:0", Int: 0}, {Label: "int:42", Int: 42}, {Label: "int:-1", Int: -1}}
	seedBytes   = []seedArg{
		{Label: "bytes:nil", Bytes: nil},
		{Label: "bytes:empty", Bytes: []byte{}},
		{Label: "bytes:fixed32", Bytes: seedFixed32},
		{Label: "bytes:int42", Bytes: core.EncodeInt64Seed(42)},
	}
	seedStrings = []seedArg{{Label: "string:empty", Str: ""}, {Label: "string:release-9", Str: "release-9"}}

	repInt    = seedArg{Label: "int:42", Int: 42}
	repBytes  = seedArg{Label: "bytes:int42", Bytes: core.EncodeInt64Seed(42)}
	repString = seedArg{Label: "string:release-9", Str: "release-9"}
	noSeed    = seedArg{Label: "generated"}
)

// ---------------------------------------------------------------------------
// 案例
// ---------------------------------------------------------------------------

type baselineCase struct {
	Key     string
	Kind    string // machine | simulator | devsim | runtime
	Combo   baselineCombo
	Game    spec.GID
	Entry   string
	Seed    seedArg
	IsSim   bool
	Unfroze bool
	Raw     []byte
}

func referenceKey(key string) string { return "ref/" + key }

var machineByRawEntries = map[string]string{
	"NewMachineByJSON": "json", "NewMachineByJSONWithSeedBytes": "json",
	"NewMachineByYAML": "yaml", "NewMachineByYAMLWithSeedBytes": "yaml",
}

var simulatorByRawEntries = map[string]string{
	"NewSimulatorByJSON": "json", "NewSimulatorByJSONWithSeedBytes": "json",
	"NewSimulatorByYAML": "yaml", "NewSimulatorByYAMLWithSeedBytes": "yaml",
}

// target 是可執行的案例映射表（PRD 0012 §3.6、techspec §6.1）：回傳比對基準的
// B0 鍵，以及遷移後預期的 OmitExtend。
func (c baselineCase) target() (string, *bool) {
	if !baselineBuildersMigrated {
		// C1：舊入口仍存在，每個案例比對自己在 B0 中的紀錄。
		if c.Kind == "machine" {
			return c.Key, boolPtr(c.IsSim)
		}
		if c.Kind == "simulator" {
			return c.Key, boolPtr(true)
		}
		return c.Key, nil
	}
	switch c.Kind {
	case "machine":
		if c.removedMode() {
			return referenceKey(c.Key), boolPtr(false)
		}
		return c.Key, boolPtr(c.IsSim)
	case "simulator":
		return c.Key, boolPtr(true)
	}
	return c.Key, nil
}

// removedMode 表示舊案例屬於被移除的「省略 Extend 的 Machine」模式。
func (c baselineCase) removedMode() bool {
	if c.Kind != "machine" {
		return false
	}
	if _, ok := machineByRawEntries[c.Entry]; ok {
		return true
	}
	return c.IsSim
}

func (c baselineCase) needsReference() bool { return c.removedMode() }

// omitsExtendNow 表示此案例「現在」建出的物件處於省略 Extend 的模式：Simulator
// 一律如此；舊的 isSim=true Machine 與 ByJSON/YAML Machine 在舊入口刪除前也是。
func (c baselineCase) omitsExtendNow() bool {
	switch c.Kind {
	case "simulator":
		return true
	case "machine":
		return !baselineBuildersMigrated && c.removedMode()
	}
	return false
}

// fixtureOmitted：fixture 遊戲不檢查 isSim，在省略模式下其內部 Extend 預期會因
// 引擎不呼叫 Snapshot() 而變為 nil，所以只比對非 Extend 結果，並要求 Snapshot()
// 呼叫次數為 0（techspec C3）。demo 遊戲自行檢查 isSim，不受此影響，仍做完整比對。
func (c baselineCase) fixtureOmitted() bool {
	return baselineEngineOmitsSnapshot && c.Game == fixtureGameID && c.omitsExtendNow()
}

func (c baselineCase) compareExtend() bool { return !c.fixtureOmitted() }

func (c baselineCase) record(t testing.TB) *baselineRecord {
	t.Helper()
	switch c.Kind {
	case "machine":
		return recordMachineCase(t, c.Key, func() (*Machine, *detReader, error) {
			lab, entropy, err := newBaselineLab(t, c.Combo, !c.Unfroze)
			if err != nil {
				t.Fatalf("%s: lab: %v", c.Key, err)
			}
			m, err := buildBaselineMachine(lab, c)
			return m, entropy, err
		})
	case "simulator":
		return recordSimulatorCase(t, c.Key, func() (*Simulator, *detReader, error) {
			lab, entropy, err := newBaselineLab(t, c.Combo, !c.Unfroze)
			if err != nil {
				t.Fatalf("%s: lab: %v", c.Key, err)
			}
			s, err := buildBaselineSimulator(lab, c)
			return s, entropy, err
		})
	case "devsim":
		return recordDevSimCase(t, c.Key, func() (*DevSimulator, *detReader, error) {
			lab, entropy, err := newBaselineLab(t, c.Combo, true)
			if err != nil {
				t.Fatalf("%s: lab: %v", c.Key, err)
			}
			d, err := lab.NewDevSimulator(c.Game, c.Seed.Int)
			return d, entropy, err
		})
	case "runtime":
		return recordRuntimeCase(t, c.Key, func() (*SlotRuntime, *Problab, *detReader, error) {
			lab, entropy, err := newBaselineLab(t, c.Combo, true)
			if err != nil {
				t.Fatalf("%s: lab: %v", c.Key, err)
			}
			rt, err := lab.BuildRuntime(baselineRuntimePool)
			return rt, lab, entropy, err
		})
	}
	t.Fatalf("unknown case kind %q", c.Kind)
	return nil
}

func (c baselineCase) recordReference(t testing.TB) *baselineRecord {
	t.Helper()
	return recordMachineCase(t, referenceKey(c.Key), func() (*Machine, *detReader, error) {
		lab, entropy, err := newBaselineLab(t, c.Combo, true)
		if err != nil {
			t.Fatalf("%s: lab: %v", c.Key, err)
		}
		m, err := buildBaselineReference(lab, c)
		return m, entropy, err
	})
}

func baselineKey(kind string, combo baselineCombo, game spec.GID, entry string, seed seedArg, isSim bool, variant string) string {
	key := fmt.Sprintf("%s/%s/g%d/%s/%s/sim=%v", kind, combo.Name, game, entry, seed.Label, isSim)
	if variant != "" {
		key += "/" + variant
	}
	return key
}

func baselineCases(t testing.TB) []baselineCase {
	t.Helper()
	var out []baselineCase
	add := func(c baselineCase, variant string) {
		c.Key = baselineKey(c.Kind, c.Combo, c.Game, c.Entry, c.Seed, c.IsSim, variant)
		out = append(out, c)
	}
	for _, combo := range baselineCombos {
		for _, game := range combo.Games {
			variety := combo.Optimal == "off" && game == 0
			ints, bytesSeeds, strs := []seedArg{repInt}, []seedArg{repBytes}, []seedArg{repString}
			if variety {
				ints, bytesSeeds, strs = seedInts, seedBytes, seedStrings
			}
			yamlRaw := combo.rawConfig(t, game)
			jsonRaw := yamlToJSON(t, yamlRaw)
			rawFor := func(kind string) []byte {
				if kind == "json" {
					return jsonRaw
				}
				return yamlRaw
			}
			for _, isSim := range []bool{true, false} {
				m := func(entry string, seed seedArg) {
					add(baselineCase{Kind: "machine", Combo: combo, Game: game, Entry: entry, Seed: seed, IsSim: isSim}, "")
				}
				m("NewMachine", noSeed)
				for _, s := range ints {
					m("NewMachineWithSeed", s)
				}
				for _, s := range bytesSeeds {
					m("NewMachineWithSeedBytes", s)
				}
				for _, s := range strs {
					m("NewMachineWithSeedString", s)
				}
				m("NewUnoptimizedMachineWithSeed", repInt)
				m("NewUnoptimizedMachineWithSeedBytes", repBytes)
			}
			for _, entry := range []string{"NewMachineByJSON", "NewMachineByYAML"} {
				kind := machineByRawEntries[entry]
				add(baselineCase{Kind: "machine", Combo: combo, Game: game, Entry: entry, Seed: repInt, IsSim: true, Raw: rawFor(kind)}, "")
				add(baselineCase{Kind: "machine", Combo: combo, Game: game, Entry: entry + "WithSeedBytes", Seed: repBytes, IsSim: true, Raw: rawFor(kind)}, "")
			}

			s := func(entry string, seed seedArg, raw []byte) {
				add(baselineCase{Kind: "simulator", Combo: combo, Game: game, Entry: entry, Seed: seed, IsSim: true, Raw: raw}, "")
			}
			s("NewSimulator", noSeed, nil)
			for _, seed := range ints {
				s("NewSimulatorWithSeed", seed, nil)
			}
			for _, seed := range bytesSeeds {
				s("NewSimulatorWithSeedBytes", seed, nil)
			}
			for _, seed := range strs {
				s("NewSimulatorWithSeedString", seed, nil)
			}
			for _, entry := range []string{"NewSimulatorByJSON", "NewSimulatorByYAML"} {
				kind := simulatorByRawEntries[entry]
				s(entry, repInt, rawFor(kind))
				s(entry+"WithSeedBytes", repBytes, rawFor(kind))
			}

			add(baselineCase{Kind: "devsim", Combo: combo, Game: game, Entry: "NewDevSimulator", Seed: repInt}, "")
		}
		add(baselineCase{Kind: "runtime", Combo: combo, Entry: "BuildRuntime", Seed: noSeed}, "")
	}
	out = append(out, baselineErrorCases(t)...)
	return out
}

// baselineErrorCases 是錯誤路徑案例（techspec C7）。成功路徑之外，錯誤等級、
// 訊息與錯誤發生時已讀取的 entropy 也必須一致。
func baselineErrorCases(t testing.TB) []baselineCase {
	t.Helper()
	combo := baselineCombos[0]
	var out []baselineCase
	add := func(kind, entry string, game spec.GID, seed seedArg, raw []byte, unfroze bool, variant string) {
		c := baselineCase{Kind: kind, Combo: combo, Game: game, Entry: entry, Seed: seed, IsSim: true, Raw: raw, Unfroze: unfroze}
		c.Key = baselineKey(kind, combo, game, entry, seed, true, "err-"+variant)
		out = append(out, c)
	}
	good := combo.rawConfig(t, 0)
	goodJSON := yamlToJSON(t, good)
	for _, e := range []struct {
		kind, entry string
		seed        seedArg
		raw         []byte
	}{
		{"machine", "NewMachine", noSeed, nil},
		{"machine", "NewMachineWithSeed", repInt, nil},
		{"machine", "NewMachineWithSeedBytes", repBytes, nil},
		{"machine", "NewUnoptimizedMachineWithSeed", repInt, nil},
		{"machine", "NewMachineByJSON", repInt, goodJSON},
		{"machine", "NewMachineByYAML", repInt, good},
		{"simulator", "NewSimulator", noSeed, nil},
		{"simulator", "NewSimulatorWithSeed", repInt, nil},
		{"simulator", "NewSimulatorByJSON", repInt, goodJSON},
		{"simulator", "NewSimulatorByYAML", repInt, good},
	} {
		add(e.kind, e.entry, 0, e.seed, e.raw, true, "unfrozen")
	}
	for _, e := range []struct {
		kind, entry string
		seed        seedArg
	}{
		{"machine", "NewMachine", noSeed},
		{"machine", "NewMachineWithSeed", repInt},
		{"machine", "NewUnoptimizedMachineWithSeed", repInt},
		{"simulator", "NewSimulator", noSeed},
		{"simulator", "NewSimulatorWithSeed", repInt},
	} {
		add(e.kind, e.entry, 77, e.seed, nil, false, "unknown-id")
	}
	replace := func(old, new string) []byte {
		out := strings.Replace(string(good), old, new, 1)
		if out == string(good) {
			t.Fatalf("error fixture replacement failed: %q", old)
		}
		return []byte(out)
	}
	manifest, err := fs.ReadFile(testManifestConfigFS(t), "game.yaml")
	if err != nil {
		t.Fatal(err)
	}
	raws := map[string][]byte{
		"bad-yaml":         []byte("game_id: ["),
		"unknown-name":     replace("game_name: demo_normal", "game_name: not_registered"),
		"unknown-logic":    replace("logic_key: demo_normal", "logic_key: not_registered"),
		"optimal-missing":  manifest,
		"bad-json-as-yaml": []byte("{"),
	}
	for _, variant := range []string{"bad-yaml", "unknown-name", "unknown-logic", "optimal-missing", "bad-json-as-yaml"} {
		add("machine", "NewMachineByYAML", 0, repInt, raws[variant], false, variant)
		add("simulator", "NewSimulatorByYAML", 0, repInt, raws[variant], false, variant)
	}
	add("machine", "NewMachineByJSON", 0, repInt, []byte("{"), false, "bad-json")
	add("simulator", "NewSimulatorByJSON", 0, repInt, []byte("{"), false, "bad-json")
	return out
}

// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/playerexp"
	"github.com/zintix-labs/problab/sdk/strategy"
)

func TestEmbeddedConfigAndArguments(t *testing.T) {
	c, e := playerexp.ParseConfig(config)
	if e != nil || len(c.Analysis) != 2 {
		t.Fatal(c, e)
	}
	if code, e := run([]string{"-game=0"}, nil); code != 1 || e == nil {
		t.Fatal(code, e)
	}
}

func TestApplicationInjectionPoints(t *testing.T) {
	oldLab, oldCatalog := pLab, strategyCatalog
	t.Cleanup(func() { pLab, strategyCatalog = oldLab, oldCatalog })
	sentinel := errors.New("private lab failure")
	pLab = func() (*problab.Problab, error) { return nil, sentinel }
	if code, err := run(nil, nil); code != 1 || !errors.Is(err, sentinel) {
		t.Fatal(code, err)
	}
	pLab = oldLab
	strategyCatalog = strategy.Catalog{77: {{Name: "private", Factory: nil}}}
	if code, err := run(nil, nil); code != 1 || err == nil || !strings.Contains(err.Error(), "game 77") {
		t.Fatal("catalog injection was bypassed", code, err)
	}
}

// -config selects another YAML: a missing file fails before the Lab is built,
// and the choice-game examples run with the demo strategies.
func TestConfigFlagAndChoiceExamples(t *testing.T) {
	if code, err := run([]string{"-config", "examples/missing.yaml"}, nil); code != 1 || err == nil || !strings.Contains(err.Error(), "missing.yaml") {
		t.Fatal(code, err)
	}
	if code, err := run([]string{"-config"}, nil); code != 1 || err == nil {
		t.Fatal("a flag without a value was accepted", code, err)
	}
	if code, err := run([]string{"extra"}, nil); code != 1 || err == nil {
		t.Fatal("a positional argument was accepted", code, err)
	}
	lab, err := pLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	reg, err := strategyCatalog.BuildRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"examples/game_2.yaml", "examples/game_3.yaml"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		c, err := playerexp.ParseConfig(raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for i := range c.Analysis {
			c.Analysis[i].Player.Players = 5 // keep the test short
		}
		r, err := playerexp.Run(context.Background(), c, playerexp.Options{Lab: lab, Strategies: reg, Tags: gameTags})
		if err != nil || r.Groups[0].Completed != 5 {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

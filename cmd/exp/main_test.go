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
	"errors"
	"strings"
	"testing"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/playerexp"
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
	strategyCatalog = playerexp.StrategyCatalog{77: {{Name: "private", Factory: nil}}}
	if code, err := run(nil, nil); code != 1 || err == nil || !strings.Contains(err.Error(), "game 77") {
		t.Fatal("catalog injection was bypassed", code, err)
	}
}

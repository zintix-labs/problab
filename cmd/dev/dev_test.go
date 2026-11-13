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

package main

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

// 預設的注入點：pLab 建出 demo 的機台，devGames 為每一台提供 <gid>.js。
func TestDefaultInjectionPoints(t *testing.T) {
	lab, err := pLab()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	ids := lab.IDs()
	if len(ids) == 0 {
		t.Fatal("no games registered")
	}
	for _, gid := range ids {
		raw, err := fs.ReadFile(devGames, fmt.Sprintf("%d.js", gid))
		if err != nil {
			t.Fatalf("game %d has no plugin: %v", gid, err)
		}
		if !strings.Contains(string(raw), fmt.Sprintf("DEV.register(%d,", gid)) {
			t.Fatalf("%d.js does not register game %d", gid, gid)
		}
	}
}

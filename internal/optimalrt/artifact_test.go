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

package optimalrt

import (
	"bytes"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zintix-labs/problab/sdk/core"
)

func routesArtifact(t *testing.T) *Artifact {
	t.Helper()
	gs, files := modesSetting()
	addPair(t, files, "a", 3)
	addPair(t, files, "b", 5)
	a, err := loadModesArtifact(fsSource{fsys: files}, "k", testSeedLen, nil, gs)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestArtifactOffers(t *testing.T) {
	a := routesArtifact(t)
	for next, want := range map[string]bool{"3": true, "3,7": true, "7": true, "3,1": false, "": false} {
		var entries []int
		for _, f := range strings.Split(next, ",") {
			switch f {
			case "1":
				entries = append(entries, 1)
			case "3":
				entries = append(entries, 3)
			case "7":
				entries = append(entries, 7)
			}
		}
		if got := a.Offers(0, entries); got != want {
			t.Errorf("Offers(%v) = %v", entries, got)
		}
	}
	if a.Offers(1, []int{3}) || a.Offers(-1, []int{3}) {
		t.Fatal("an out-of-range bet mode offers entries")
	}
}

// start 與入口各從自己的庫抽；不抽庫、未配置、越界與已關閉的位置回傳錯誤。
func TestArtifactPicks(t *testing.T) {
	a := routesArtifact(t)
	rng, err := core.Default().New(core.EncodeInt64Seed(1))
	if err != nil {
		t.Fatal(err)
	}
	c := core.New(rng)
	for range 50 {
		start, err := a.PickStart(0, c)
		if err != nil || len(start) != testSeedLen || start[0] < 1 || start[0] > 3 {
			t.Fatalf("start %v %v", start, err)
		}
		entry, err := a.PickEntry(0, 3, c)
		if err != nil || len(entry) != testSeedLen || entry[0] < 1 || entry[0] > 5 || !bytes.Equal(entry, bytes.Repeat(entry[:1], testSeedLen)) {
			t.Fatalf("entry %v %v", entry, err)
		}
	}
	for name, pick := range map[string]func() error{
		"natural entry":  func() error { _, err := a.PickEntry(0, 7, c); return err },
		"unknown entry":  func() error { _, err := a.PickEntry(0, 1, c); return err },
		"bet mode range": func() error { _, err := a.PickStart(1, c); return err },
	} {
		if pick() == nil {
			t.Errorf("%s: no error", name)
		}
	}
	natural, err := loadModesArtifact(fsSource{fsys: fstest.MapFS{}}, "k", testSeedLen, nil, modesNaturalSetting())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := natural.PickStart(0, c); err == nil {
		t.Fatal("a natural start was picked")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PickStart(0, c); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("closed artifact: %v", err)
	}
}

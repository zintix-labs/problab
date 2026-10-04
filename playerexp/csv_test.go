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
package playerexp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func csvFixture(t *testing.T) Report {
	t.Helper()
	c, o, m := fixture(1)
	c.Analysis[0].Name = "中文,\"group\"\nnext"
	c.Analysis[0].Events[0].Name = "=事件,\"quoted\"\nline"
	register(t, &o, fixedFactory(1))
	r, e := runMachine(context.Background(), c, o, m, m.units)
	if e != nil {
		t.Fatal(e)
	}
	// Exact integer formatting must not pass through float64.
	r.Groups[0].TotalWin = 9007199254740993
	r.Groups = append(r.Groups, GroupReport{Status: NotStarted, Config: GroupConfig{Name: "later", Player: PlayerConfig{Players: 1}}})
	return r
}
func TestFullJSONReportPreservesGroupsAndTags(t *testing.T) {
	r := csvFixture(t)
	r.Groups[0].Config.Events[0].Tags = []string{"1", "2", "3", "4"}
	r.Groups[1].Config.Events = []EventConfig{{Name: "same", Tags: []string{"3", "4", "5", "6"}}}
	r.Status = Failed
	r.Error = "interrupted run"
	root := t.TempDir()
	path, err := WriteCSV(root, r)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(path, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got JSONReport
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != "playerexp-report-v1" {
		t.Fatal(got.SchemaVersion)
	}
	// Private samples are intentionally excluded; all public report fields survive.
	for i := range r.Groups {
		r.Groups[i].rtps = nil
	}
	if !reflect.DeepEqual(got.Report, r) {
		t.Fatalf("report did not round-trip: %+v", got.Report)
	}
	if !bytes.Contains(raw, []byte("9007199254740993")) {
		t.Fatal("integer precision lost")
	}
	if bytes.Contains(raw, []byte("\"rtps\"")) {
		t.Fatal("private player history exported")
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 6 {
		t.Fatal(entries, err)
	}
}

func TestCSVGoldenAndRoundTrip(t *testing.T) {
	r := csvFixture(t)
	root := t.TempDir()
	path, e := WriteCSV(root, r)
	if e != nil {
		t.Fatal(e)
	}
	// Fixed schema/value goldens; printed hash is useful when deliberately
	// reviewing a format revision, never updated automatically by the test.
	want := []string{
		"e2cec7cb464bc4971db750dc819a861d76ff5cb29f08571467be6d79b65531af",
		"429f721dc43f58ebb15d5cc9b8e4cc969c8d138753c5317b380bfd32f236f797",
		"8628b5af0c0792534d0ca1fc5f7b95b8e4bc2b2edf94996deea906456d0e1444",
		"e0af31404dfa0029aefc72078949d2ec11418f39ba98db3e37279e23a87d4274",
		"c1106aa3ca1344d0f9b9604da9558b40333e43cf8dfbc6c82eee5df01a283c25",
	}
	for i, name := range csvNames {
		raw, e := os.ReadFile(filepath.Join(path, name))
		if e != nil {
			t.Fatal(e)
		}
		var direct bytes.Buffer
		if e = writeTable(&direct, r, i); e != nil || !bytes.Equal(raw, direct.Bytes()) {
			t.Fatal("publication mismatch", e)
		}
		rows, e := csv.NewReader(bytes.NewReader(raw)).ReadAll()
		if e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(rows[0], append([]string{"group_index", "group_name", "group_status"}, csvHeaders[i]...)) {
			t.Fatal(rows[0])
		}
		if i > 0 && rows[1][1] != r.Groups[0].Config.Name {
			t.Fatal("name did not roundtrip")
		}
		for _, row := range rows[1:] {
			if i > 0 && row[0] != "0" {
				t.Fatal("NOT_STARTED fake statistics", row)
			}
		}
		hash := fmt.Sprintf("%x", sha256.Sum256(raw))
		if hash != want[i] {
			t.Fatalf("%s golden mismatch: %s", name, hash)
		}
		if i == 0 && !strings.Contains(string(raw), "9007199254740993") {
			t.Fatal("integer precision")
		}
		if bytes.Contains(raw, []byte("NaN")) || bytes.Contains(raw, []byte("+Inf")) {
			t.Fatal("non-finite output")
		}
	}
	second, e := WriteCSV(root, r)
	if e != nil || second == path {
		t.Fatal("overwrote run", e)
	}
}

type faultCSVFS struct {
	osCSVFS
	phase    string
	sentinel error
}

func (f faultCSVFS) Create(p string) (io.WriteCloser, error) {
	if strings.HasPrefix(f.phase, "json-") {
		if filepath.Base(p) != "report.json" {
			return f.osCSVFS.Create(p)
		}
		f.phase = strings.TrimPrefix(f.phase, "json-")
	}
	if f.phase == "create" {
		return nil, f.sentinel
	}
	w, e := f.osCSVFS.Create(p)
	if e != nil {
		return nil, e
	}
	return &faultCSVWriter{WriteCloser: w, phase: f.phase, sentinel: f.sentinel}, nil
}
func (f faultCSVFS) Rename(a, b string) error {
	if f.phase == "rename" {
		return f.sentinel
	}
	return f.osCSVFS.Rename(a, b)
}

type faultCSVWriter struct {
	io.WriteCloser
	phase    string
	sentinel error
}

func (w *faultCSVWriter) Write(p []byte) (int, error) {
	if w.phase == "write" {
		return 0, w.sentinel
	}
	return w.WriteCloser.Write(p)
}
func (w *faultCSVWriter) Close() error {
	e := w.WriteCloser.Close()
	if w.phase == "close" {
		return errors.Join(e, w.sentinel)
	}
	return e
}

func TestCSVAtomicPublicationFailures(t *testing.T) {
	for _, phase := range []string{"create", "write", "close", "rename", "json-create", "json-write", "json-close"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			r := csvFixture(t)
			old, e := WriteCSV(root, r)
			if e != nil {
				t.Fatal(e)
			}
			original, e := os.ReadFile(filepath.Join(old, "summary.csv"))
			if e != nil {
				t.Fatal(e)
			}
			sentinel := errors.New(phase)
			if phase == "write" {
				// Force an underlying write before the final Flush.
				r.Groups[0].Config.Name = strings.Repeat("x", 16384)
			}
			path, e := writeCSV(root, r, faultCSVFS{phase: phase, sentinel: sentinel})
			if !errors.Is(e, sentinel) || path != "" {
				t.Fatal(path, e)
			}
			entries, e := os.ReadDir(root)
			if e != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(old) {
				t.Fatal("staging leak or old report damage", entries, e)
			}
			after, e := os.ReadFile(filepath.Join(old, "summary.csv"))
			if e != nil || !bytes.Equal(original, after) {
				t.Fatal("old data changed", e)
			}
		})
	}
	// Fewer than 4KB: failure happens only on csv.Writer.Flush.
	e := errors.New("flush")
	if !errors.Is(writeTable(&faultCSVWriter{phase: "write", sentinel: e}, Report{}, 1), e) {
		t.Fatal("lost flush error")
	}
}

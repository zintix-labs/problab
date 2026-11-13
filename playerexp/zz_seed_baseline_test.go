package playerexp

// Temporary seed baseline (seed relocation acceptance): runs the shipped exp
// configs with int64, UTF-8 and hex seeds and keeps every published file.

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/demo/demo_strategies"
	"github.com/zintix-labs/problab/demo/demo_tags"
)

func TestZZSeedBaseline(t *testing.T) {
	out := os.Getenv("ZZ_BASELINE_OUT")
	if out == "" {
		t.Skip("ZZ_BASELINE_OUT not set")
	}
	root := filepath.Join(out, "playerexp")
	b := &zzBaseline{}
	zzSeedCorpus(b)

	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	reg, err := demo_strategies.Catalog.BuildRegistry()
	if err != nil {
		t.Fatal(err)
	}
	configs := []string{"../cmd/exp/exp_cfg.yaml", "../cmd/exp/examples/game_2.yaml", "../cmd/exp/examples/game_3.yaml"}
	seeds := [][2]string{{"int64", "77"}, {"utf8", `"utf8:baseline seed"`}, {"hex", `"hex:00FF10AB"`}}
	for _, path := range configs {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "seed: 77\n") {
			t.Fatalf("%s has no seed: 77", path)
		}
		for _, pair := range seeds {
			kind, seed := pair[0], pair[1]
			name := strings.TrimSuffix(filepath.Base(path), ".yaml") + "_" + kind
			c, err := ParseConfig([]byte(strings.Replace(string(raw), "seed: 77\n", "seed: "+seed+"\n", 1)))
			if err != nil {
				b.fail("ParseConfig "+name, err)
				continue
			}
			cj, _ := json.Marshal(c)
			b.line("config %s => %s", name, cj)
			r, err := Run(context.Background(), c, Options{Lab: lab, Strategies: reg, Tags: demo_tags.Catalog})
			if err != nil {
				b.fail("Run "+name, err)
				continue
			}
			published, err := WriteCSV(t.TempDir(), r)
			if err != nil {
				t.Fatal(err)
			}
			dst := filepath.Join(root, "runs", name)
			if err := filepath.WalkDir(published, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				rel, _ := filepath.Rel(published, p)
				body, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				if err := os.MkdirAll(filepath.Dir(filepath.Join(dst, rel)), 0o755); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(dst, rel), body, 0o644)
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	b.save(t, root)
}

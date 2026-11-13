package v2

// Temporary seed baseline (seed relocation acceptance): config resolution and
// hashes, overrides, fresh collection and the complete tuner pipeline under
// int64, UTF-8 and hex root seeds.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/zintix-labs/problab/sdk/core"
)

func zzFileSHA(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func TestZZSeedBaseline(t *testing.T) {
	out := os.Getenv("ZZ_BASELINE_OUT")
	if out == "" {
		t.Skip("ZZ_BASELINE_OUT not set")
	}
	b := &zzBaseline{}
	zzSeedCorpus(b)

	utf8Seed, err := UTF8Seed("autumn-build-7")
	if err != nil {
		t.Fatal(err)
	}
	hexSeed, err := HexSeed("00112233445566778899AABBCCDDEEFF00112233445566778899AABBCCDDEEFF")
	if err != nil {
		t.Fatal(err)
	}
	seeds := []struct {
		name string
		yaml string
		spec SeedSpec
	}{
		{"int64", "4127483647", Int64Seed(4127483647)},
		{"utf8", `"utf8:autumn-build-7"`, utf8Seed},
		{"hex", `"hex:00112233445566778899AABBCCDDEEFF00112233445566778899AABBCCDDEEFF"`, hexSeed},
	}

	// Config resolution, canonical JSON, config hash and overrides.
	for _, s := range seeds {
		config, err := ParseConfig([]byte(strings.Replace(validConfigYAML, "seed: 4127483647", "seed: "+s.yaml, 1)))
		if err != nil {
			b.fail("ParseConfig "+s.name, err)
			continue
		}
		resolved, err := config.ResolvePlan(config.Plans[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(resolved)
		hash, err := hashCanonicalJSON(resolved)
		if err != nil {
			t.Fatal(err)
		}
		b.line("resolved %s => hash=%s json=%s", s.name, hash, raw)
		for _, o := range seeds {
			seed := o.spec
			overridden, err := resolved.WithOverrides(RunOverrides{Seed: &seed})
			if err != nil {
				t.Fatal(err)
			}
			ohash, _ := hashCanonicalJSON(overridden)
			oraw, _ := json.Marshal(RunOverrides{Seed: &seed})
			b.line("override %s<-%s => hash=%s overrides=%s seed=%s", s.name, o.name, ohash, oraw, overridden.Plan.Seed.String())
		}
		yamlOut, _ := json.Marshal(config.Plans[0].Seed)
		b.line("plan seed %s => %s", s.name, yamlOut)
	}

	// Fresh collection: snapshot digest and wins depend on the root seed bytes.
	for _, s := range seeds {
		lab := newCollectionFactoryLab(t, core.Default())
		plan := collectionFixturePlan(0, 2, 64, 64, 16)
		plan.Plan.Seed = s.spec
		collected, diagnostics, err := NewCollector(lab).Collect(context.Background(), plan, 0)
		_ = lab.Close()
		if err != nil || diagnostics.StopsRun() {
			t.Fatalf("Collect %s: diagnostics=%+v err=%v", s.name, diagnostics, err)
		}
		b.line("collect %s => digest=%s wins=%v", s.name, collectionSnapshotsDigest(t, collected), collectionTotalWins(t, collected))
	}

	// Complete tuner pipeline (collection, LP, verification, publication).
	for _, s := range seeds {
		result := runCompleteProductionPipeline(t, s.spec, "baseline-"+s.name)
		r := result.Report
		overrides, _ := json.Marshal(r.Overrides)
		// ConfigHash covers Output.Directory (a fresh TempDir per run), so it is
		// recorded below from the same config with a fixed directory instead.
		b.line("pipeline %s => status=%s model=%s solution=%s artifact=%s engine=%s overrides=%s",
			s.name, result.Status, r.ModelHash, r.SolutionHash, r.ArtifactHash, r.Engine.Version, overrides)
		b.line("pipeline %s bank => seeds=%d bytes=%d sha=%s", s.name, r.Collection.Bank.SeedCount, r.Collection.Bank.Bytes, zzFileSHA(t, r.Collection.Bank.Path))
		var artifacts []string
		for _, path := range result.ArtifactPaths {
			rel, err := filepath.Rel(r.Publication.Directory, path)
			if err != nil {
				rel = filepath.Base(path)
			}
			artifacts = append(artifacts, rel+"="+zzFileSHA(t, path))
		}
		sort.Strings(artifacts)
		b.line("pipeline %s artifacts => %s", s.name, strings.Join(artifacts, " "))
	}
	// Pipeline config hash with the per-run output directory pinned.
	for _, s := range seeds {
		lab := newCollectionFactoryLab(t, core.Default())
		const samples uint64 = 256
		collection := CollectionOptions{Workers: 2, BatchSize: 64, MaxSpins: samples}
		fixture := collectionFixturePlan(0, collection.Workers, samples, collection.MaxSpins, collection.BatchSize)
		fixture.Plan.Seed = s.spec
		collected, _, err := NewCollector(lab).Collect(context.Background(), fixture, 0)
		_ = lab.Close()
		if err != nil {
			t.Fatal(err)
		}
		config := exactPipelineConfig(t, collected, samples, collection, s.spec, "baseline-"+s.name, Target{Game: 1, BetModes: []int{0}})
		for i := range config.Plans {
			config.Plans[i].Output.Directory = "/baseline/output"
		}
		resolved, err := config.ResolvePlan("baseline-" + s.name)
		if err != nil {
			t.Fatal(err)
		}
		hash, err := hashCanonicalJSON(resolved)
		if err != nil {
			t.Fatal(err)
		}
		b.line("pipeline %s config hash (fixed output dir) => %s", s.name, hash)
	}
	b.save(t, filepath.Join(out, "optimizer_v2"))
}

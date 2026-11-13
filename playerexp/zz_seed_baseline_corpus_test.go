package playerexp

// Temporary seed baseline (seed relocation acceptance): records every observable
// SeedSpec output so the pre- and post-change trees can be compared byte for
// byte. Error texts go to a separate file because their prefix changes on purpose.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type zzBaseline struct {
	data strings.Builder
	errs strings.Builder
}

func (b *zzBaseline) line(format string, args ...any) { fmt.Fprintf(&b.data, format+"\n", args...) }

func (b *zzBaseline) fail(label string, err error) {
	b.line("%s => ERR", label)
	fmt.Fprintf(&b.errs, "%s => %v\n", label, err)
}

func (b *zzBaseline) save(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte(b.data.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "errors.txt"), []byte(b.errs.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func zzShort(s string) string {
	if len(s) <= 96 {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("len=%d sha256=%s", len(s), hex.EncodeToString(sum[:]))
}

func zzDescribe(b *zzBaseline, label string, s SeedSpec) {
	js, jerr := json.Marshal(s)
	ys, yerr := yaml.Marshal(struct {
		Seed SeedSpec `yaml:"seed"`
	}{s})
	b.line("%s => kind=%q len=%d bytes=%s string=%s json=%s jsonErr=%v yaml=%s yamlErr=%v",
		label, s.Kind(), s.Len(), zzShort(hex.EncodeToString(s.Bytes())), zzShort(s.String()),
		zzShort(string(js)), jerr != nil, zzShort(strings.TrimSpace(string(ys))), yerr != nil)
}

func zzSeedCorpus(b *zzBaseline) {
	var zero SeedSpec
	zzDescribe(b, "zero", zero)
	for _, v := range []int64{0, 1, -1, 42, 77, 4127483647, math.MaxInt64, math.MinInt64} {
		zzDescribe(b, fmt.Sprintf("Int64Seed(%d)", v), Int64Seed(v))
	}
	for _, m := range []string{"a", "autumn-build-7", "  ", " 12 ", "中文-seed", strings.Repeat("x", 1024), "", string([]byte{0xff}), strings.Repeat("x", 1025)} {
		label := fmt.Sprintf("UTF8Seed(%s)", zzShort(fmt.Sprintf("%q", m)))
		if s, err := UTF8Seed(m); err != nil {
			b.fail(label, err)
		} else {
			zzDescribe(b, label, s)
		}
	}
	for _, m := range []string{"9f3a", "9F3A", "00", "00112233445566778899AABBCCDDEEFF", strings.Repeat("ab", 1024), "", "abc", "xyz", strings.Repeat("ab", 1025)} {
		label := fmt.Sprintf("HexSeed(%s)", zzShort(fmt.Sprintf("%q", m)))
		if s, err := HexSeed(m); err != nil {
			b.fail(label, err)
		} else {
			zzDescribe(b, label, s)
		}
	}
	for _, text := range []string{"utf8:x", "utf8: a ", "hex:9F3A", "hex:", "utf8:", "UTF8:x", "Hex:9f", "4127483647", "", "bare", "hex:abc"} {
		label := fmt.Sprintf("ParseSeedSpec(%q)", text)
		if s, err := ParseSeedSpec(text); err != nil {
			b.fail(label, err)
		} else {
			zzDescribe(b, label, s)
		}
	}
	for _, raw := range []string{`0`, `-1`, `77`, `9223372036854775807`, `9223372036854775808`, `-9223372036854775808`,
		`"utf8:x"`, `"hex:AB"`, `"hex:abc"`, `"42"`, `"utf8:"`, `null`, `true`, `1.5`, `1e3`, `[]`, `{}`, `1 2`, ``, `"中"`} {
		label := fmt.Sprintf("json.Unmarshal(%s)", raw)
		var s SeedSpec
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			b.fail(label, err)
		} else {
			zzDescribe(b, label, s)
		}
	}
	for _, doc := range []string{"seed: 77", "seed: -1", "seed: 0x7f", "seed: 0o17", "seed: 1_000", "seed:", "seed: ~",
		`seed: "utf8:a b"`, `seed: "hex:9F3A"`, `seed: "bare"`, "seed: 1.5", "seed: true", "seed: [1]", "seed: {a: 1}",
		"seed: 9223372036854775808", "seed: !<tag:yaml.org,2002:int> 42", "x: 1\nseed: \"hex:\""} {
		label := fmt.Sprintf("yaml.Unmarshal(%q)", doc)
		var v struct {
			Seed SeedSpec `yaml:"seed"`
		}
		if err := yaml.Unmarshal([]byte(doc), &v); err != nil {
			b.fail(label, err)
		} else {
			zzDescribe(b, label, v.Seed)
		}
	}
}

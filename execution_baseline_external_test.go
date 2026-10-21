// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package problab_test

// E0 的 playerexp 案例。playerexp
// import problab，所以放在外部測試套件，資料存於獨立檔案並以獨立常數保護。
// 錄製方式與 TestExecutionBaseline 相同：PROBLAB_RECORD_EXEC_BASELINE=e0。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zintix-labs/problab/demo"
	"github.com/zintix-labs/problab/playerexp"
	"github.com/zintix-labs/problab/playerexp/strategies"
)

// execE0PlayerexpSHA256 固定 e0/playerexp.json 的 SHA-256。
const execE0PlayerexpSHA256 = "e588e14989e4c5249a32a960de39169aeadfa8c6b13a2773edff12889f601f00"

const (
	execPlayerexpDir      = "testdata/execution_baseline"
	execPlayerexpFile     = "e0/playerexp.json"
	execPlayerexpManifest = "e0-playerexp.sha256"
)

func execPlayerexpReport(t *testing.T) []byte {
	t.Helper()
	lab, err := demo.NewProbLab()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	c, err := playerexp.ParseConfig([]byte(`game_id: 0
seed: 77
analysis:
  - name: e0
    player: {players: 20, init_bet: 300, strategy: flat}
    win_buckets: [0, 1, 2, 5]
`))
	if err != nil {
		t.Fatal(err)
	}
	rg := playerexp.NewStrategyRegistry()
	if err := rg.Register(0, "flat", strategies.Monotone{BetMode: 0, BetMult: 1, MaxSpins: 200, SatisfiedRatio: 3}); err != nil {
		t.Fatal(err)
	}
	r, err := playerexp.Run(context.Background(), c, playerexp.Options{Lab: lab, Strategies: rg})
	if err != nil || r.Status != playerexp.Completed {
		t.Fatalf("playerexp run: status=%v err=%v", r.Status, err)
	}
	// 只保留整數欄位與非數值欄位；衍生浮點數不比對。
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		t.Fatal(err)
	}
	out, err := json.MarshalIndent(execExternalKeepInts(tree), "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func execExternalKeepInts(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if kept := execExternalKeepInts(val); kept != nil {
				out[k] = kept
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, val := range x {
			out = append(out, execExternalKeepInts(val))
		}
		return out
	case json.Number:
		if _, err := x.Int64(); err == nil {
			return x
		}
		return nil
	default:
		return x
	}
}

func TestExecutionBaselinePlayerexp(t *testing.T) {
	mode := os.Getenv("PROBLAB_RECORD_EXEC_BASELINE")
	if mode == "" && testing.Short() {
		t.Skip("執行路徑基準比對在 -short 下略過")
	}
	path := filepath.Join(execPlayerexpDir, execPlayerexpFile)
	switch mode {
	case "e0":
		first, second := execPlayerexpReport(t), execPlayerexpReport(t)
		if !bytes.Equal(first, second) {
			t.Fatal("同一個 commit 上兩次錄製的結果不同，基準不可重現")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, first, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(first)
		manifest := fmt.Sprintf("%x  playerexp.json\n", sum)
		if err := os.WriteFile(filepath.Join(execPlayerexpDir, execPlayerexpManifest), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("已錄製 E0 playerexp：SHA-256=%x", sum)
	case "":
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(want))
		manifest, err := os.ReadFile(filepath.Join(execPlayerexpDir, execPlayerexpManifest))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(manifest), sum+" ") {
			t.Fatalf("%s 與 %s 不符，基準已被改動", path, execPlayerexpManifest)
		}
		if sum != execE0PlayerexpSHA256 {
			t.Fatalf("%s 的 SHA-256=%s 與程式中固定的常數 %q 不符", path, sum, execE0PlayerexpSHA256)
		}
		if got := execPlayerexpReport(t); !bytes.Equal(got, want) {
			t.Fatalf("playerexp 報表與 E0 不同\n got=%s", got)
		}
	default:
		t.Fatalf("unknown PROBLAB_RECORD_EXEC_BASELINE=%q", mode)
	}
}

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

package cli

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo"
)

func devLab(t *testing.T) *problab.Problab {
	t.Helper()
	lab, err := demo.NewProbLabForDev()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	return lab
}

// freeAddr 回傳目前沒有被占用的本機位址。
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// syncBuffer 讓測試讀取 Run 在另一個 goroutine 寫入的輸出。
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func status(t *testing.T, url string) int {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	return res.StatusCode
}

// Run 只提供 /dev（ModePanel），印出網址；ctx 結束時關閉 server 並回傳 0。
func TestRunServesPanelUntilCanceled(t *testing.T) {
	addr := freeAddr(t)
	out := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, err := Run(ctx, Options{
			Lab:      devLab(t),
			DevGames: fstest.MapFS{"0.js": &fstest.MapFile{Data: []byte("//")}},
			Addr:     addr,
			Output:   out,
			Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		done <- result{code, err}
	}()
	url := "http://" + addr
	for i := 0; !strings.Contains(out.String(), "ProbLab Dev: "+url+"/dev"); i++ {
		if i > 200 {
			t.Fatalf("panel never reported ready; output %q", out.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	for path, want := range map[string]int{"/dev": 200, "/dev/meta": 200, "/dev/games/0.js": 200, "/dev/games/1.js": 404, "/v1/health": 404} {
		if got := status(t, url+path); got != want {
			t.Errorf("%s: status %d, want %d", path, got, want)
		}
	}
	cancel()
	select {
	case r := <-done:
		if r.code != 0 || r.err != nil {
			t.Fatalf("Run after cancel: code %d err %v", r.code, r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if _, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		t.Fatal("server still listening after Run returned")
	}
}

func TestRunReportsStartupErrors(t *testing.T) {
	if code, err := Run(context.Background(), Options{}); code != 1 || err == nil || !strings.Contains(err.Error(), "Lab is required") {
		t.Fatalf("no Lab: code %d err %v", code, err)
	}
	lab := devLab(t)
	if code, err := Run(context.Background(), Options{Lab: lab, Addr: "no-port"}); code != 1 || err == nil || !strings.Contains(err.Error(), "invalid address") {
		t.Fatalf("bad address: code %d err %v", code, err)
	}
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code, err := Run(ctx, Options{Lab: lab, Addr: busy.Addr().String(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if code != 1 || err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("port in use: code %d err %v", code, err)
	}
}

func TestHosts(t *testing.T) {
	for host, want := range map[string][2]string{
		"":          {"127.0.0.1", "localhost"},
		"0.0.0.0":   {"127.0.0.1", "localhost"},
		"::":        {"127.0.0.1", "localhost"},
		"127.0.0.1": {"127.0.0.1", "127.0.0.1"},
		"devbox":    {"devbox", "devbox"},
	} {
		if got := [2]string{dialHost(host), browseHost(host)}; got != want {
			t.Errorf("%q: dial/browse %v, want %v", host, got, want)
		}
	}
}

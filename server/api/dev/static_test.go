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

package dev

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"
	"github.com/zintix-labs/problab/server/svrcfg"
)

func get(t *testing.T, ts *httptest.Server, path string) (int, string, string) {
	t.Helper()
	res, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, res.Header.Get("Content-Type"), string(body)
}

func TestStaticFiles(t *testing.T) {
	ts := newServer(t, demoLab(t))
	for _, c := range []struct {
		path, ctype, contains string
	}{
		{"/dev", "text/html; charset=utf-8", "<title>ProbLab Dev</title>"},
		{"/dev/", "text/html; charset=utf-8", "<title>ProbLab Dev</title>"},
		{"/dev/app.css", "text/css; charset=utf-8", "--primary"},
		{"/dev/common.js", "text/javascript; charset=utf-8", "DEV.register"},
		{"/dev/fonts/JetBrainsMono-Regular.woff2", "font/woff2", "wOF2"},
	} {
		code, ctype, body := get(t, ts, c.path)
		if code != http.StatusOK || ctype != c.ctype || !strings.Contains(body, c.contains) {
			t.Errorf("%s: status %d type %q", c.path, code, ctype)
		}
	}
	for _, path := range []string{"/dev/missing.css", "/dev/../go.mod", "/dev/web/index.html", "/dev/games/x.js", "/dev/games/99.js", "/dev/fonts/"} {
		if code, _, _ := get(t, ts, path); code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, code)
		}
	}
}

// 應用端注入的外掛優先於內建；沒有注入的機台用內建的外掛；兩邊都沒有時回 404，前端只顯示公版。
func TestDevGamesOverrideBuiltinPlugins(t *testing.T) {
	rt := &testRouter{r: chi.NewRouter()}
	Register(rt, &svrcfg.SvrCfg{Problab: demoLab(t), Mode: svrcfg.ModeDev, DevGames: fstest.MapFS{
		"0.js":  &fstest.MapFile{Data: []byte("// injected 0")},
		"42.js": &fstest.MapFile{Data: []byte("// injected 42")},
	}})
	ts := httptest.NewServer(rt.r)
	t.Cleanup(ts.Close)
	for path, want := range map[string]string{"/dev/games/0.js": "// injected 0", "/dev/games/42.js": "// injected 42"} {
		code, ctype, body := get(t, ts, path)
		if code != http.StatusOK || ctype != "text/javascript; charset=utf-8" || body != want {
			t.Errorf("%s: status %d type %q body %q", path, code, ctype, body)
		}
	}
	if code, _, body := get(t, ts, "/dev/games/1.js"); code != http.StatusOK || !strings.Contains(body, "DEV.register(1,") {
		t.Errorf("built-in plugin for game 1: status %d", code)
	}
	if code, _, _ := get(t, ts, "/dev/games/7.js"); code != http.StatusNotFound {
		t.Errorf("plugin for a game without one: status %d, want 404", code)
	}
}

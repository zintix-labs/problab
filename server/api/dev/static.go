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
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/zintix-labs/problab/server/netsvr"
	"github.com/zintix-labs/problab/server/svrcfg"
	"github.com/zintix-labs/problab/spec"
)

// web 是內建的前端：公版（index.html、app.css、common.js）與字型。problab 不內建任何機台的外掛。
//
//go:embed web
var web embed.FS

// contentTypes 列出可以送出的檔案類型；其他副檔名一律 404。
var contentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".woff2": "font/woff2",
}

// pluginName 是機台外掛的檔名：<gid>.js。
var pluginName = regexp.MustCompile(`^[0-9]+\.js$`)

// registerStatic 註冊頁面與靜態檔。明確的 API 路徑優先於 /dev/* 萬用路徑。
func registerStatic(svr netsvr.NetRouter, cfg *svrcfg.SvrCfg) {
	svr.Get("/dev", serveIndex)
	svr.Get("/dev/games/*", servePlugin(cfg))
	svr.Get("/dev/*", serveStatic)
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	serveFile(w, r, web, "web/index.html")
}

func serveStatic(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/dev/")
	if name == "" {
		serveIndex(w, r)
		return
	}
	serveFile(w, r, web, path.Join("web", name))
}

// servePlugin 只從應用端注入的外掛目錄（SvrCfg.DevGames）讀取。前端只在 /dev/meta 標示有
// 外掛時才請求，所以正常操作不會走到 404。
func servePlugin(cfg *svrcfg.SvrCfg) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/dev/games/")
		if !pluginName.MatchString(name) || cfg == nil || cfg.DevGames == nil {
			http.NotFound(w, r)
			return
		}
		serveFile(w, r, cfg.DevGames, name)
	}
}

// hasPlugin 回報應用端是否注入了這台的外掛。
func hasPlugin(devGames fs.FS, gid spec.GID) bool {
	if devGames == nil {
		return false
	}
	_, err := fs.Stat(devGames, fmt.Sprintf("%d.js", gid))
	return err == nil
}

func serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) {
	ctype, ok := contentTypes[path.Ext(name)]
	if !ok || !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	body, err := fs.ReadFile(fsys, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(body)
}

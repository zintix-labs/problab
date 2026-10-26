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

package middleware

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
)

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") ||
		r.Header.Get("Upgrade") != ""
}

func isNoBodyStatus(code int) bool {
	// 204 No Content, 304 Not Modified, 1xx Informational
	return (code >= 100 && code < 200) || code == http.StatusNoContent || code == http.StatusNotModified
}

// CompressConfig
type CompressConfig struct {
	GzipLevel int
	ZstdLevel zstd.EncoderLevel
}

var DefaultCompressConfig = CompressConfig{
	GzipLevel: gzip.DefaultCompression,
	ZstdLevel: zstd.SpeedFastest,
}

// --- Pools ---
var (
	gzipPool sync.Pool
	zstdPool sync.Pool
)

// --- Zstd Logic ---
func getZstdWriter(w io.Writer) *zstd.Encoder {
	if v := zstdPool.Get(); v != nil {
		zw := v.(*zstd.Encoder)
		zw.Reset(w)
		return zw
	}
	zw, err := zstd.NewWriter(w,
		zstd.WithEncoderLevel(DefaultCompressConfig.ZstdLevel),
		zstd.WithEncoderConcurrency(1),
	)
	if err != nil {
		panic(err)
	}
	return zw
}

func releaseZstdWriter(zw *zstd.Encoder) {
	_ = zw.Close()
	zstdPool.Put(zw)
}

// --- Gzip Logic ---
func getGzipWriter(w io.Writer) *gzip.Writer {
	if v := gzipPool.Get(); v != nil {
		gw := v.(*gzip.Writer)
		gw.Reset(w)
		return gw
	}
	gw, _ := gzip.NewWriterLevel(w, DefaultCompressConfig.GzipLevel)
	return gw
}

func releaseGzipWriter(gw *gzip.Writer) {
	_ = gw.Close()
	gzipPool.Put(gw)
}

// --- ResponseWriter Wrapper ---

type compressResponseWriter struct {
	http.ResponseWriter
	w        io.Writer // 指向 gzip.Writer 或 zstd.Encoder
	disabled bool      // 標記是否動態取消壓縮
}

func (cw *compressResponseWriter) Write(b []byte) (int, error) {
	// 1. 如果已停用壓縮 (204/304)，直接寫入底層
	if cw.disabled {
		return cw.ResponseWriter.Write(b)
	}

	// 2. 防禦隱式 Header 發送
	cw.Header().Del("Content-Length")

	// 3. 嗅探 Content-Type
	if cw.Header().Get("Content-Type") == "" {
		cw.Header().Set("Content-Type", http.DetectContentType(b))
	}

	// 4. 寫入壓縮器
	return cw.w.Write(b)
}

func (cw *compressResponseWriter) WriteHeader(code int) {
	cw.Header().Del("Content-Length")

	// 動態偵測是否應該取消壓縮 (204/304/1xx)
	if isNoBodyStatus(code) {
		cw.disabled = true
		dropCompressionHeaders(cw.Header())
	}

	cw.ResponseWriter.WriteHeader(code)
}

func (cw *compressResponseWriter) Flush() {
	// 只有在啟用壓縮時，才 Flush 壓縮器
	if !cw.disabled {
		if f, ok := cw.w.(interface{ Flush() error }); ok {
			_ = f.Flush()
		}
	}
	// 永遠 Flush 底層
	if f, ok := cw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (cw *compressResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := cw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("underlying response writer does not support Hijacker")
	}
	return hj.Hijack()
}

func (cw *compressResponseWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := cw.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return errors.New("underlying response writer does not support Pusher")
}

// dropCompressionHeaders 撤回 Compression 對回應標頭的修改：移除 Content-Encoding、
// 不再對應實際內容的 Content-Length，以及本 middleware 加入的 Vary: Accept-Encoding；
// 其他 middleware 設定的 Vary 保留。
func dropCompressionHeaders(h http.Header) {
	h.Del("Content-Encoding")
	h.Del("Content-Length")
	vary := h.Values("Vary")
	for i := len(vary) - 1; i >= 0; i-- {
		if vary[i] == "Accept-Encoding" {
			vary = append(vary[:i:i], vary[i+1:]...)
			break
		}
	}
	if len(vary) == 0 {
		h.Del("Vary")
	} else {
		h["Vary"] = vary
	}
}

// abandonCompression 在 handler 沒有正常返回（panic）時撤回壓縮標頭；呼叫端同時把壓縮器
// 導向 io.Discard。否則壓縮器 Close 時寫出的結尾會先送出 200，外層 Recoverer 的 500 失效。
// 只對尚未送出回應的 panic 有效：handler 已送出的標頭與內容無法撤回。不攔截 panic，讓它
// 帶著原本的 stack 繼續往外傳。
func abandonCompression(w http.ResponseWriter) {
	dropCompressionHeaders(w.Header())
}

// --- Middleware 入口 ---

func Compression(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// [Guard 1] WebSocket / Head
		if r.Method == http.MethodHead || isWebSocketUpgrade(r) {
			next.ServeHTTP(w, r)
			return
		}

		// [Guard 2] 避免二次壓縮
		if w.Header().Get("Content-Encoding") != "" {
			next.ServeHTTP(w, r)
			return
		}

		encoding := r.Header.Get("Accept-Encoding")

		// 1. Zstd
		if strings.Contains(encoding, "zstd") {
			w.Header().Set("Content-Encoding", "zstd")
			w.Header().Add("Vary", "Accept-Encoding")

			zw := getZstdWriter(w)
			// [關鍵修復] 如果 response 被標記為 disabled，將 Writer 重置到 io.Discard
			// 這樣 Close() 時產生的 Footer 就會被丟棄，不會污染 204/304 回應
			cw := &compressResponseWriter{ResponseWriter: w, w: zw}
			completed := false
			defer func() {
				if !completed {
					abandonCompression(w)
				}
				if cw.disabled || !completed {
					zw.Reset(io.Discard)
				}
				releaseZstdWriter(zw)
			}()

			next.ServeHTTP(cw, r)
			completed = true
			return
		}

		// 2. Gzip
		if strings.Contains(encoding, "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Add("Vary", "Accept-Encoding")

			gw := getGzipWriter(w)
			// 同上，防止 204/304 寫入 Gzip Footer
			cw := &compressResponseWriter{ResponseWriter: w, w: gw}
			completed := false
			defer func() {
				if !completed {
					abandonCompression(w)
				}
				if cw.disabled || !completed {
					gw.Reset(io.Discard)
				}
				releaseGzipWriter(gw)
			}()

			next.ServeHTTP(cw, r)
			completed = true
			return
		}

		// 3. 不壓縮
		next.ServeHTTP(w, r)
	})
}

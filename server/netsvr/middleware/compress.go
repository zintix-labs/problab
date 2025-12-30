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
		cw.Header().Del("Content-Encoding")
		cw.Header().Del("Vary")
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
			defer func() {
				if cw.disabled {
					zw.Reset(io.Discard)
				}
				releaseZstdWriter(zw)
			}()

			next.ServeHTTP(cw, r)
			return
		}

		// 2. Gzip
		if strings.Contains(encoding, "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Add("Vary", "Accept-Encoding")

			gw := getGzipWriter(w)
			// 同上，防止 204/304 寫入 Gzip Footer
			cw := &compressResponseWriter{ResponseWriter: w, w: gw}
			defer func() {
				if cw.disabled {
					gw.Reset(io.Discard)
				}
				releaseGzipWriter(gw)
			}()

			next.ServeHTTP(cw, r)
			return
		}

		// 3. 不壓縮
		next.ServeHTTP(w, r)
	})
}

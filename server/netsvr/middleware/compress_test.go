package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
)

// handler panic 時，Recover 寫出的 500 不被壓縮器的結尾蓋掉，也不帶壓縮標頭。
func TestCompressionLetsRecoverWriteStatusOnPanic(t *testing.T) {
	h := Recover(Compression(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})))
	for _, enc := range []string{"gzip", "zstd", ""} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if enc != "" {
			req.Header.Set("Accept-Encoding", enc)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError || rec.Header().Get("Content-Encoding") != "" || rec.Body.Len() != 0 {
			t.Fatalf("%q: status %d encoding %q body %q", enc, rec.Code, rec.Header().Get("Content-Encoding"), rec.Body.Bytes())
		}
	}
}

// panic 前 handler 設定的 Content-Length 不留在 500 上；其他 middleware 設定的 Vary 保留，
// 只撤回 Compression 加入的 Accept-Encoding。
func TestCompressionDropsOnlyItsHeadersOnPanic(t *testing.T) {
	h := Recover(varyOrigin(Compression(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "42")
		panic("boom")
	}))))
	for _, enc := range []string{"gzip", "zstd"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept-Encoding", enc)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		hd := rec.Header()
		if rec.Code != http.StatusInternalServerError || hd.Get("Content-Encoding") != "" || hd.Get("Content-Length") != "" || !reflect.DeepEqual(hd.Values("Vary"), []string{"Origin"}) {
			t.Fatalf("%s: status %d headers %v", enc, rec.Code, hd)
		}
	}
}

// 204 取消壓縮時同樣只撤回自己的 Vary。
func TestCompressionNoBodyStatusKeepsOtherVary(t *testing.T) {
	h := varyOrigin(Compression(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Content-Encoding") != "" || !reflect.DeepEqual(rec.Header().Values("Vary"), []string{"Origin"}) || rec.Body.Len() != 0 {
		t.Fatalf("status %d headers %v body %q", rec.Code, rec.Header(), rec.Body.Bytes())
	}
}

// varyOrigin 模擬在 Compression 之前設定 Vary 的其他 middleware。
func varyOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		next.ServeHTTP(w, r)
	})
}

// 正常返回時照常壓縮。
func TestCompressionEncodesBody(t *testing.T) {
	body := bytes.Repeat([]byte("problab "), 64)
	h := Compression(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	decode := map[string]func(io.Reader) ([]byte, error){
		"gzip": func(r io.Reader) ([]byte, error) {
			zr, err := gzip.NewReader(r)
			if err != nil {
				return nil, err
			}
			return io.ReadAll(zr)
		},
		"zstd": func(r io.Reader) ([]byte, error) {
			zr, err := zstd.NewReader(r)
			if err != nil {
				return nil, err
			}
			defer zr.Close()
			return io.ReadAll(zr)
		},
	}
	for enc, dec := range decode {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept-Encoding", enc)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		got, err := dec(rec.Body)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != enc || err != nil || !bytes.Equal(got, body) {
			t.Fatalf("%s: status %d encoding %q err %v", enc, rec.Code, rec.Header().Get("Content-Encoding"), err)
		}
	}
}

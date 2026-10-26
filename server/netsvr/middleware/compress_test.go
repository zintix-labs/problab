package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
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

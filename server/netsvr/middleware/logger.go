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
	"log/slog"
	"net/http"
	"time"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	return r.ResponseWriter.Write(b)
}

// AccessLog is an HTTP middleware that emits one structured access log per request.
//
// Design notes:
//   - This middleware logs ONLY request/response envelope signals (method/path/status/latency).
//   - It does NOT introduce any custom log-event type; everything is emitted via slog.
//   - Async / buffering behavior is controlled by the slog.Handler wiring done by the caller
//     (e.g., wrapping the base handler with an AsyncHandler).
//
// If log is nil, the middleware becomes a no-op.
func AccessLog(log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		return func(next http.Handler) http.Handler { return next }
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			rw := &statusRecorder{
				ResponseWriter: w,
				status:         http.StatusOK, // default 200
			}

			next.ServeHTTP(rw, r)

			status := rw.status
			lvl := levelByStatus(status)

			// NOTE: keep the message stable for log-based metrics aggregation.
			log.LogAttrs(
				r.Context(),
				lvl,
				"http.access",
				slog.Int("status", status),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Duration("latency", time.Since(start)),
			)
		})
	}
}

func levelByStatus(status int) slog.Level {
	switch {
	case status >= 500:
		return slog.LevelError
	case status >= 400:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

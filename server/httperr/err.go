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

package httperr

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/zintix-labs/problab/errs"
)

// StatusCode 將錯誤映射成 HTTP status code。
//
// 規則（邊界層最小映射、可預期）：
//   - ctx timeout/cancel → 504/408（請求生命週期問題）
//   - errs.Warn         → 400（請求/參數問題）
//   - errs.Fatal        → 500（系統/不可恢復問題）
//
// 注意：本函數屬於 HTTP 邊界層，因此放在 server/*（而不是 core errs）。
// 這樣可以避免讓核心錯誤包依賴 net/http 等傳輸層細節。
func StatusCode(err error) int {
	status := http.StatusInternalServerError

	// 1) 先處理 context 取消/超時（即使被 wrap 也能被 errors.Is 命中）
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout // 504
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout // 408
	default:
		// fallthrough
	}

	// 2) 再處理內部錯誤分級（errs.E/Wrap）
	var e *errs.E
	if errors.As(err, &e) {
		switch e.ErrLv {
		case errs.Warn:
			status = http.StatusBadRequest // 400
		case errs.Fatal:
			status = http.StatusInternalServerError // 500
		default:
			status = http.StatusInternalServerError
		}
	}

	return status
}

func Errs(w http.ResponseWriter, err error) {
	// HTTP 邊界層：把常見錯誤做最小且可預期的狀態碼映射。
	// 這裡只負責：決定 status code + 寫回簡單的 http.Error。
	if err == nil {
		return
	}
	status := StatusCode(err)
	http.Error(w, err.Error(), status)
}

func Log(log *slog.Logger, msg string, err error) {
	// HTTP 邊界層：把常見錯誤做最小且可預期的狀態碼映射。
	// 這裡只負責：決定 status code + 寫回簡單的 http.Error。
	if err == nil {
		return
	}
	status := StatusCode(err)
	if (status == 408) || (status == 409) || (status == 429) {
		log.Warn(msg, slog.Any("err", err))
	} else if (status >= 500) && (status < 600) {
		log.Error(msg, slog.Any("err", err))
	}
}

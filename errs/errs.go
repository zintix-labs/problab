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

package errs

import (
	"errors"
	"fmt"
)

// ErrLevel : Error 分級，使最上層理解問題嚴重程度
type ErrLevel uint8

const (
	None ErrLevel = iota
	Fatal
	Warn
	Log
)

var errLvMap = map[ErrLevel]string{
	None:  "",
	Fatal: "fatal",
	Warn:  "warn",
	Log:   "log",
}

func ErrLv(errlv ErrLevel) string {
	if str, ok := errLvMap[errlv]; ok {
		return str
	}
	return ""
}

// E 是統一的錯誤型別。
// Message 為經過樣板格式化後的主訊息；Extra 為呼叫端可追加的額外上下文；
// Cause 可串接下層錯誤（wrap）；Fatal 表示是否為致命錯誤（需立即中止）。
type E struct {
	Message string
	Extra   string
	Cause   error
	ErrLv   ErrLevel
}

// Error 實作 error 介面並回傳格式化後的錯誤訊息。
func (e *E) Error() string {
	base := fmt.Sprintf("errlv=%s %s", ErrLv(e.ErrLv), e.Message)
	if e.Extra != "" {
		base += " | extra: " + e.Extra
	}
	if e.Cause != nil {
		base += fmt.Sprintf(" (cause: %v)", e.Cause)
	}
	return base
}

// Unwrap 讓 errors.Is / errors.As 能夠向下展開。
func (e *E) Unwrap() error { return e.Cause }

// New 依錯誤碼與參數建立錯誤
func New(errLv ErrLevel, msg string) *E {
	return &E{Message: msg, ErrLv: errLv}
}

func NewFatal(msg string) *E {
	return &E{Message: msg, ErrLv: Fatal}
}

func NewWarn(msg string) *E {
	return &E{Message: msg, ErrLv: Warn}
}

func NewLog(msg string) *E {
	return &E{Message: msg, ErrLv: Log}
}

func Fatalf(format string, a ...any) *E {
	return NewFatal(fmt.Sprintf(format, a...))
}

func Warnf(format string, a ...any) *E {
	return NewWarn(fmt.Sprintf(format, a...))
}

func Logf(format string, a ...any) *E {
	return NewLog(fmt.Sprintf(format, a...))
}

// NewWithExtra 與 New 相同，但可附加額外上下文字串（不影響主訊息）。
func NewWithExtra(errLv ErrLevel, msg string, extra string) *E {
	e := New(errLv, msg)
	e.Extra = extra
	return e
}

// Wrap 使用給定的錯誤碼與訊息包裝底層錯誤，建立一個 *E。
//
// ErrLevel 規則：
//   - 若 cause 已經是 *E，則沿用其 ErrLv（保持原本嚴重度）。
//   - 若 cause 不是本包定義的 *E（多半是標準庫或三方依賴錯誤），則 ErrLv 一律視為 ErrLvFatal。
//
// 建議使用方式：
//   - 若你已判斷該錯誤是「可預期且可處理」的情境，請直接建立一個 *E
//     （使用 New / NewWithExtra 並自行指定 ErrLv），而不要對其呼叫 Wrap。
func Wrap(cause error, msg string) *E {
	var e *E
	errLv := Fatal
	if errors.As(cause, &e) {
		errLv = e.ErrLv
	}
	r := New(errLv, msg)
	r.Cause = cause
	return r
}

// WrapWithExtra 使用給定的錯誤碼與訊息與上下文包裝底層錯誤，建立一個 *E
//
// ErrLevel 規則：
//   - 若 cause 已經是 *E，則沿用其 ErrLv（保持原本嚴重度）。
//   - 若 cause 不是本包定義的 *E（多半是標準庫或三方依賴錯誤），則 ErrLv 一律視為 ErrLvFatal。
//
// 建議使用方式：
//   - 若你已判斷該錯誤是「可預期且可處理」的情境，請直接建立一個 *E
//     （使用 New / NewWithExtra 並自行指定 ErrLv），而不要對其呼叫 Wrap。
func WrapWithExtra(cause error, msg string, extra string) *E {
	var e *E
	errLv := Fatal
	if errors.As(cause, &e) {
		errLv = e.ErrLv
	}
	r := NewWithExtra(errLv, msg, extra)
	r.Cause = cause
	return r
}

func AsErr(err error) (*E, bool) {
	var e *E
	if errors.As(err, &e) {
		return e, true
	}
	return e, false
}

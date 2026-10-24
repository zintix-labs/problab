// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

// Package tag compiles named result predicates into immutable bit assignments.
package tag

import (
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/spec"
)

// GameTagCatalog groups named predicates by game ID; it is the only entry point
// for resolving tags. Names are game-scoped; the catalog does not register tags
// automatically. Applications assemble it and inject it into consumers, which
// compile the names they use into a Tagger with Compile or CompileBy.
// Do not mutate its maps while consumers are compiling from it.
type GameTagCatalog map[spec.GID]map[string]IsTag

// IsTag 判斷一份借用的結果是否帶有某個標籤。契約：
//   - 只根據傳入的結果判斷，不依賴呼叫次數；
//   - 不修改結果，也不修改 RNG 或其他執行與分析狀態；
//   - 必須能安全地對所屬遊戲的每一份合法段結果，以及 buf.SpinAssembler 組出的每一份完整局
//     結果執行，不依賴事件範圍、贏分區間或其他 tag 先成立（消費端可能對每一份結果都判斷
//     全部引用的 tag）；
//   - 同一個 tag 用在段與完整局時語意可能不同（例如 GameModeCount > 1 在段上表示這一段內
//     有模式切換，在完整局上表示這一局有觸發），由 tag 的作者依用途負責；
//   - 編譯出的 Tagger 被多個 worker 共用時，必須能安全地並行呼叫。
type IsTag func(sr *buf.SpinResult) bool

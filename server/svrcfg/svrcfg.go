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

package svrcfg

import (
	"log/slog"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/server/logger"
)

type SvrCfg struct {
	Log         *slog.Logger
	SlotBufSize int
	Problab     *problab.Problab
}

func (sc *SvrCfg) Vaild() error {
	if sc.Log != nil {
		if ah, ok := sc.Log.Handler().(*logger.AsyncHandler); ok && !ah.Ready() {
			return errs.NewFatal("nil default log handler: async handler is nil")
		}
	} else {
		// 保持安靜、合法
		sc.Log, _ = logger.NewAsync(1024, logger.ModeDev)
	}

	// 1 <= sc.SlotBuffer <= 10
	// for 資源管理
	sc.SlotBufSize = max(1, sc.SlotBufSize)
	sc.SlotBufSize = min(10, sc.SlotBufSize)
	if sc.Problab == nil {
		return errs.NewFatal("problab is required")
	}
	return nil
}

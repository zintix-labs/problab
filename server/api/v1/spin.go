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

package v1

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/server/httperr"
	"github.com/zintix-labs/problab/server/svrcfg"
	"github.com/zintix-labs/problab/spec"
)

func (s *SpinHandler) Spin(w http.ResponseWriter, r *http.Request) {
	// 請求方法、結構體校驗
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req, err := buf.DecodeSpinRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// 請求解析完成，設置超時 context
	ctx := r.Context()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// 開始 Spin
	result, err := s.rt.Spin(ctx, req)
	if err != nil {
		httperr.Log(s.log, "spin failed", err)
		httperr.Errs(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		httperr.Errs(w, err)
		return
	}
	// 需要作兩次記憶體寫入，但保證不會解析錯誤(寫到一半才error)
	// var b bytes.Buffer
	// enc := json.NewEncoder(&b)

	// if err := enc.Encode(result); err != nil {
	// 	httperr.Errs(w, err)
	// 	return
	// }

	// w.Header().Set("Content-Type", "application/json")
	// w.WriteHeader(http.StatusOK)
	// _, _ = w.Write(b.Bytes())
}

func (s *SpinHandler) Health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	health := s.rt.Health()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(health); err != nil {
		httperr.Errs(w, err)
		return
	}
}

func (s *SpinHandler) PoolMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	game := r.URL.Query().Get("gid")
	if game == "" {
		httperr.Errs(w, errs.NewWarn("gid is required"))
		return
	}

	gidint, err := strconv.Atoi(game)
	if err != nil {
		httperr.Errs(w, errs.NewWarn("gid parse error "+err.Error()))
		return
	}
	if gidint < 0 {
		httperr.Errs(w, errs.NewWarn("gid must be a non-negative integer"))
		return
	}

	metrics, ok := s.rt.PoolMetrics(spec.GID(gidint))
	if !ok {
		httperr.Errs(w, errs.NewWarn("gid is not exist: "+game))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(metrics); err != nil {
		httperr.Errs(w, err)
		return
	}
}

// ============================================================
// ** SpinHandler **
// ============================================================

type SpinHandler struct {
	rt  *problab.SlotRuntime
	log *slog.Logger
}

func NewSpinHandler(sCfg *svrcfg.SvrCfg) (*SpinHandler, error) {
	rt, err := sCfg.Problab.BuildRuntime(sCfg.SlotBufSize)
	if err != nil {
		return nil, errs.Wrap(err, "build spin handler error")
	}
	return &SpinHandler{rt: rt, log: sCfg.Log}, nil
}

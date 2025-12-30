package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/server/httperr"
	"github.com/zintix-labs/problab/server/svrcfg"
)

func (c *SpinHandler) Spin(w http.ResponseWriter, q *http.Request) {
	// 請求方法、結構體校驗
	if q.Method != http.MethodGet && q.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req, err := buf.DecodeSpinRequest(q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// 請求解析完成，設置超時 context
	ctx := q.Context()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// 開始 Spin
	result, err := c.rt.Spin(ctx, req)
	if err != nil {
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

// ============================================================
// ** SpinHandler **
// ============================================================

type SpinHandler struct {
	rt *problab.SlotRuntime
}

func NewSpinHandler(sCfg *svrcfg.SvrCfg) (*SpinHandler, error) {
	rt, err := sCfg.Problab.BuildRuntime(sCfg.SlotBufSize)
	if err != nil {
		return nil, errs.Wrap(err, "build spin handler error")
	}
	return &SpinHandler{rt: rt}, nil
}

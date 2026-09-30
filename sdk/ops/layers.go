package ops

import "github.com/zintix-labs/problab/sdk/layer"

// validateLayers 在任何寫入前檢查長度。nil 或長度不符直接 panic。
// 操作入口不提供並行安全或交易回滾；其餘參數須符合原操作契約。
// Layer 不可重複、彼此共用儲存，或與 screen 共用儲存；不在熱路徑檢查別名。
func validateLayers(screen []int16, layers []layer.SyncOps) {
	for _, lr := range layers {
		if lr == nil || lr.Len() != len(screen) {
			panic("ops: layer length differs from screen or layer is nil")
		}
	}
}

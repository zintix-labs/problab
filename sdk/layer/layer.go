// Package layer 提供與盤面位置對應的值型別狀態容器。
// Layer 不判斷圖標、空格或遊戲規則，也不提供並行讀寫保護。
package layer

// LayerValue 限定可按值複製的基本型別及其自訂型別，排除可變引用資料。
// 數值的遊戲語意（含 NaN、Inf 是否允許）由呼叫者驗證。
type LayerValue interface {
	~bool | ~string |
		~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
		~float32 | ~float64
}

// Layer 保存各格狀態與重置用預設值。索引越界直接 panic，不回滾。
// State 可直接存取；參與同步操作時不得改變長度、與 screen 共用底層陣列，
// 或與其他傳入 Layer 共用底層陣列。同一 Layer 不可重複傳入同一次操作。
type Layer[T LayerValue] struct {
	// State 的索引對應正常盤面的格子索引。
	State        []T
	defaultValue T
}

// New 建立 size 格並填入 dv。size 可為 0；負值直接 panic。
func New[T LayerValue](size int, dv T) *Layer[T] {
	lr := &Layer[T]{
		State:        make([]T, size),
		defaultValue: dv,
	}
	lr.Reset()
	return lr
}

// Len 回傳目前格子數；nil receiver 會 panic。
func (lr *Layer[T]) Len() int { return len(lr.State) }

// Set 修改單格狀態，不驗證遊戲數值的合理性。
func (lr *Layer[T]) Set(idx int, value T) {
	lr.State[idx] = value
}

// Swap 交換兩格；相同索引不改變內容。
func (lr *Layer[T]) Swap(idxA, idxB int) {
	lr.State[idxA], lr.State[idxB] = lr.State[idxB], lr.State[idxA]
}

// Copy 複製來源格到目的格，不清除來源。
func (lr *Layer[T]) Copy(dst, src int) {
	lr.State[dst] = lr.State[src]
}

// ResetIdx 將指定格恢復為建構時的預設值。
func (lr *Layer[T]) ResetIdx(idx int) {
	lr.State[idx] = lr.defaultValue
}

// Reset 將所有格子恢復為建構時的預設值。
func (lr *Layer[T]) Reset() {
	for i := range lr.State {
		lr.State[i] = lr.defaultValue
	}
}

// Snapshot 配置獨立、可修改的副本，保留各格狀態與預設值。
// 不可與原 Layer 的寫入並行執行。
func (lr *Layer[T]) Snapshot() *Layer[T] {
	snap := &Layer[T]{
		State:        make([]T, len(lr.State)),
		defaultValue: lr.defaultValue,
	}
	copy(snap.State, lr.State)
	return snap
}

// SyncOps 讓 ops 同步不同型別的 Layer，不需要註冊。
// Copy 不清除來源；ResetIdx 使用各 Layer 的預設值。
// 只傳入隨圖標操作的狀態，固定在格子上的狀態不要傳入。
// 實作者須遵守上述語意；ops 不保證自訂方法 panic 時的回滾。
type SyncOps interface {
	Len() int
	Copy(dst, src int)
	ResetIdx(idx int)
}

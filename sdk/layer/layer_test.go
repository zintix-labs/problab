package layer

import "testing"

func testValues[T LayerValue](t *testing.T, dv, value T) {
	t.Helper()
	lr := New(3, dv)
	var sync SyncOps = lr
	if sync.Len() != 3 || lr.State[0] != dv {
		t.Fatal("initial state")
	}
	lr.Set(0, value)
	lr.Copy(1, 0)
	if lr.State[0] != value || lr.State[1] != value {
		t.Fatal("copy changed source")
	}
	lr.Swap(1, 2)
	lr.Swap(0, 0)
	if lr.State[1] != dv || lr.State[2] != value {
		t.Fatal("swap")
	}
	snap := lr.Snapshot()
	lr.Reset()
	if snap.State[0] != value {
		t.Fatal("snapshot not independent")
	}
	snap.ResetIdx(0)
	if snap.State[0] != dv {
		t.Fatal("snapshot lost default")
	}
	for _, v := range lr.State {
		if v != dv {
			t.Fatal("reset")
		}
	}
	if New(0, dv).Len() != 0 {
		t.Fatal("empty")
	}
}

func TestLayerValues(t *testing.T) {
	type flag uint8
	testValues(t, 7, 42)
	testValues(t, true, false)
	testValues(t, "empty", "filled")
	testValues(t, 1.5, 2.5)
	testValues(t, flag(1), flag(2))
}

func TestLayerInvalidIndexPanics(t *testing.T) {
	for _, f := range []func(){
		func() { New(-1, 0) },
		func() { New(1, 0).Set(-1, 2) },
		func() { New(1, 0).Copy(0, 1) },
		func() { New(1, 0).Swap(0, 1) },
		func() { New(1, 0).ResetIdx(1) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("expected panic")
				}
			}()
			f()
		}()
	}
}

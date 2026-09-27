package menuboard

import (
	"sync/atomic"
	"testing"
	"time"
)

func strp(s string) *string { return &s }
func i64p(v int64) *int64   { return &v }
func boolp(v bool) *bool    { return &v }

func boardItem(b *Board, id string) *Item {
	for si := range b.Sections {
		for ii := range b.Sections[si].Items {
			if b.Sections[si].Items[ii].ID == id {
				return &b.Sections[si].Items[ii]
			}
		}
	}
	return nil
}

func mustDoWithin(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not complete within %v", what, d)
	}
}

// A render of display A is blocked inside the hook. While it is blocked, a
// batch changing two of A's items, a GetBoard of B, and another batch must
// all complete. The blocked call must not return a torn board, and the next
// GetBoard(A) must observe both changes.
func TestBlockedHookConsistency(t *testing.T) {
	var blockA atomic.Bool
	enteredA := make(chan struct{}, 1)
	releaseA := make(chan struct{})
	hook := func(id string, v uint64) {
		if id == "A" && blockA.Load() {
			enteredA <- struct{}{}
			<-releaseA
		}
	}
	st := New(hook)

	if _, err := st.ApplyBatch([]Change{
		{ID: "x", Name: strp("X"), Regular: i64p(100)},
		{ID: "y", Name: strp("Y"), Regular: i64p(200)},
		{ID: "z", Name: strp("Z"), Regular: i64p(300)},
	}); err != nil {
		t.Fatalf("seed batch: %v", err)
	}
	if _, err := st.DefineBoard("A", BoardDef{Sections: []SectionDef{
		{ItemIDs: []string{"x", "y"}},
	}}); err != nil {
		t.Fatalf("define A: %v", err)
	}
	if _, err := st.DefineBoard("B", BoardDef{Sections: []SectionDef{
		{ItemIDs: []string{"z"}},
	}}); err != nil {
		t.Fatalf("define B: %v", err)
	}
	// Warm B's cache so its later GetBoard is a pure cache hit.
	if _, _, err := st.GetBoard("B"); err != nil {
		t.Fatalf("warm B: %v", err)
	}

	// Block A's first render inside the hook.
	blockA.Store(true)
	type result struct {
		b   *Board
		v   uint64
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		b, v, err := st.GetBoard("A")
		resCh <- result{b, v, err}
	}()
	select {
	case <-enteredA:
	case <-time.After(2 * time.Second):
		t.Fatal("render of A did not reach the hook")
	}

	// While A's render is blocked, a batch changing both of A's items
	// must complete.
	var batchV uint64
	mustDoWithin(t, 2*time.Second, "batch during blocked render", func() {
		var err error
		batchV, err = st.ApplyBatch([]Change{
			{ID: "x", Regular: i64p(999)},
			{ID: "y", SoldOut: boolp(true)},
		})
		if err != nil {
			t.Errorf("batch during blocked render: %v", err)
		}
	})

	// While blocked, GetBoard(B) must complete.
	mustDoWithin(t, 2*time.Second, "GetBoard(B) during blocked render", func() {
		b, _, err := st.GetBoard("B")
		if err != nil {
			t.Errorf("GetBoard(B): %v", err)
			return
		}
		if it := boardItem(b, "z"); it == nil || it.Regular != 300 {
			t.Errorf("GetBoard(B) returned wrong z: %+v", it)
		}
	})

	// While blocked, another (unrelated) batch must complete.
	mustDoWithin(t, 2*time.Second, "second batch during blocked render", func() {
		if _, err := st.ApplyBatch([]Change{
			{ID: "w", Name: strp("W"), Regular: i64p(1)},
		}); err != nil {
			t.Errorf("second batch: %v", err)
		}
	})

	// Release the hook; the blocked call must return a coherent snapshot
	// taken before the batch: old price, not sold out.
	close(releaseA)
	var res result
	select {
	case res = <-resCh:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked GetBoard(A) did not return after hook release")
	}
	if res.err != nil {
		t.Fatalf("blocked GetBoard(A): %v", res.err)
	}
	if res.v >= batchV {
		t.Fatalf("blocked GetBoard(A) returned version %d, want < batch version %d", res.v, batchV)
	}
	if it := boardItem(res.b, "x"); it == nil || it.Regular != 100 {
		t.Fatalf("blocked GetBoard(A) x = %+v, want regular 100 (torn board?)", it)
	}
	if it := boardItem(res.b, "y"); it == nil || it.SoldOut {
		t.Fatalf("blocked GetBoard(A) y = %+v, want not sold out (torn board?)", it)
	}

	// The next GetBoard(A) must observe both changes at once.
	b2, v2, err := st.GetBoard("A")
	if err != nil {
		t.Fatalf("GetBoard(A) after release: %v", err)
	}
	if v2 < batchV {
		t.Fatalf("GetBoard(A) version %d < batch version %d", v2, batchV)
	}
	if it := boardItem(b2, "x"); it == nil || it.Regular != 999 {
		t.Fatalf("GetBoard(A) x = %+v, want regular 999", it)
	}
	if it := boardItem(b2, "y"); it == nil || !it.SoldOut {
		t.Fatalf("GetBoard(A) y = %+v, want sold out", it)
	}
}

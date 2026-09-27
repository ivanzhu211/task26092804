package menuboard

import (
	"sync"
	"testing"
	"time"
)

// withTimeout runs fn and fails the test if it does not return within d.
func withTimeout(t *testing.T, d time.Duration, what string, fn func()) {
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

func findItem(t *testing.T, b Board, id string) Item {
	t.Helper()
	for _, sec := range b.Sections {
		for _, it := range sec {
			if it.ID == id {
				return it
			}
		}
	}
	t.Fatalf("item %q not found in board %+v", id, b)
	return Item{}
}

// A render of display A is blocked inside the hook. While it is blocked,
// writes and other displays' reads must complete; the blocked call must
// return a consistent (not half-new) board, and the next read of A must
// observe all committed changes.
func TestHookBlockedRenderConsistency(t *testing.T) {
	entered := make(chan struct{})
	unblock := make(chan struct{})
	var once sync.Once
	hook := func(displayID string, version uint64) {
		if displayID != "A" {
			return
		}
		once.Do(func() {
			close(entered)
			<-unblock
		})
	}
	c := New(hook)

	if _, err := c.DefineBoard("A", BoardDef{Sections: [][]string{{"i1", "i2"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DefineBoard("B", BoardDef{Sections: [][]string{{"i3"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ApplyBatch([]ItemChange{
		{Kind: Upsert, ID: "i1", Name: Provide("one"), BasePrice: Provide(int64(100))},
		{Kind: Upsert, ID: "i2", Name: Provide("two"), BasePrice: Provide(int64(200))},
		{Kind: Upsert, ID: "i3", Name: Provide("three"), BasePrice: Provide(int64(300))},
	}); err != nil {
		t.Fatal(err)
	}

	// Start the render of A that will block inside the hook.
	type result struct {
		board Board
		ver   uint64
		err   error
	}
	blockedRes := make(chan result, 1)
	go func() {
		b, v, err := c.GetBoard("A")
		blockedRes <- result{b, v, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("render of A never reached the hook")
	}

	// While A's render is blocked: a batch changing both of A's items.
	var v2 uint64
	withTimeout(t, 5*time.Second, "batch changing A's items", func() {
		var err error
		v2, err = c.ApplyBatch([]ItemChange{
			{Kind: Upsert, ID: "i1", BasePrice: Provide(int64(150))},
			{Kind: Upsert, ID: "i2", SoldOut: Provide(true)},
		})
		if err != nil {
			t.Error(err)
		}
	})

	// While blocked: GetBoard on display B must complete.
	withTimeout(t, 5*time.Second, "GetBoard(B) during blocked render", func() {
		b, _, err := c.GetBoard("B")
		if err != nil {
			t.Error(err)
			return
		}
		if it := findItem(t, b, "i3"); it.BasePrice != 300 {
			t.Errorf("i3 price = %d, want 300", it.BasePrice)
		}
	})

	// While blocked: another batch must complete.
	var v3 uint64
	withTimeout(t, 5*time.Second, "second batch during blocked render", func() {
		var err error
		v3, err = c.ApplyBatch([]ItemChange{
			{Kind: Upsert, ID: "i3", BasePrice: Provide(int64(350))},
		})
		if err != nil {
			t.Error(err)
		}
	})

	close(unblock)
	var res result
	select {
	case res = <-blockedRes:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked GetBoard(A) did not return after hook release")
	}
	if res.err != nil {
		t.Fatal(res.err)
	}
	// The blocked call must not return a half-new board: it snapshotted
	// before v2, so both items must be in their old state.
	if res.ver >= v2 {
		t.Fatalf("blocked call returned version %d, want < %d", res.ver, v2)
	}
	if it := findItem(t, res.board, "i1"); it.BasePrice != 100 {
		t.Errorf("blocked call: i1 price = %d, want 100 (fully old)", it.BasePrice)
	}
	if it := findItem(t, res.board, "i2"); it.SoldOut {
		t.Errorf("blocked call: i2 sold out, want not sold out (fully old)")
	}

	// The next read of A must observe both changes.
	b, v, err := c.GetBoard("A")
	if err != nil {
		t.Fatal(err)
	}
	if v < v3 {
		t.Fatalf("GetBoard(A) version = %d, want >= %d", v, v3)
	}
	if it := findItem(t, b, "i1"); it.BasePrice != 150 {
		t.Errorf("i1 price = %d, want 150", it.BasePrice)
	}
	if it := findItem(t, b, "i2"); !it.SoldOut {
		t.Errorf("i2 not sold out, want sold out")
	}
}

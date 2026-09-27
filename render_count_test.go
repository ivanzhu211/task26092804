package menuboard

import (
	"sync"
	"testing"
)

// renderCounter counts actual renders per display via the hook.
type renderCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func newRenderCounter() *renderCounter {
	return &renderCounter{counts: make(map[string]int)}
}

func (r *renderCounter) hook(displayID string, _ uint64) {
	r.mu.Lock()
	r.counts[displayID]++
	r.mu.Unlock()
}

func (r *renderCounter) get(displayID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[displayID]
}

// Unrelated batches must not trigger re-renders, and concurrent misses on
// the same display at the same version must render exactly once.
func TestRenderCounting(t *testing.T) {
	rc := newRenderCounter()
	c := New(rc.hook)

	if _, err := c.DefineBoard("X", BoardDef{Sections: [][]string{{"i1", "i2"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DefineBoard("Y", BoardDef{Sections: [][]string{{"i3"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ApplyBatch([]ItemChange{
		{Kind: Upsert, ID: "i1", Name: Provide("one"), BasePrice: Provide(int64(100))},
		{Kind: Upsert, ID: "i2", Name: Provide("two"), BasePrice: Provide(int64(200))},
		{Kind: Upsert, ID: "i3", Name: Provide("three"), BasePrice: Provide(int64(300))},
	}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := c.GetBoard("X"); err != nil {
		t.Fatal(err)
	}
	if n := rc.get("X"); n != 1 {
		t.Fatalf("renders[X] = %d, want 1", n)
	}
	// Repeated read: cache hit, no render.
	if _, _, err := c.GetBoard("X"); err != nil {
		t.Fatal(err)
	}
	if n := rc.get("X"); n != 1 {
		t.Fatalf("renders[X] = %d after repeat read, want 1", n)
	}

	// Batch touching only i3 (display Y): X must not re-render, but the
	// returned version must still advance.
	vY, err := c.ApplyBatch([]ItemChange{
		{Kind: Upsert, ID: "i3", BasePrice: Provide(int64(333))},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, vX, err := c.GetBoard("X")
	if err != nil {
		t.Fatal(err)
	}
	if vX < vY {
		t.Fatalf("GetBoard(X) version = %d, want >= %d", vX, vY)
	}
	if n := rc.get("X"); n != 1 {
		t.Fatalf("renders[X] = %d after unrelated batch, want 1", n)
	}

	// Batch touching i1 (display X): 8 concurrent misses render once.
	if _, err := c.ApplyBatch([]ItemChange{
		{Kind: Upsert, ID: "i1", BasePrice: Provide(int64(111))},
	}); err != nil {
		t.Fatal(err)
	}
	const readers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, _, err := c.GetBoard("X"); err != nil {
				t.Error(err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if n := rc.get("X"); n != 2 {
		t.Fatalf("renders[X] = %d after concurrent misses, want 2", n)
	}

	// Display Y: first read renders once; a batch on i1 must not dirty it.
	if _, _, err := c.GetBoard("Y"); err != nil {
		t.Fatal(err)
	}
	if n := rc.get("Y"); n != 1 {
		t.Fatalf("renders[Y] = %d, want 1", n)
	}
	if _, err := c.ApplyBatch([]ItemChange{
		{Kind: Upsert, ID: "i1", BasePrice: Provide(int64(112))},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.GetBoard("Y"); err != nil {
		t.Fatal(err)
	}
	if n := rc.get("Y"); n != 1 {
		t.Fatalf("renders[Y] = %d after unrelated batch, want 1", n)
	}

	// A no-op upsert (identical values) must not dirty the display either.
	if _, err := c.ApplyBatch([]ItemChange{
		{Kind: Upsert, ID: "i3", Name: Provide("three"), BasePrice: Provide(int64(333))},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.GetBoard("Y"); err != nil {
		t.Fatal(err)
	}
	if n := rc.get("Y"); n != 1 {
		t.Fatalf("renders[Y] = %d after no-op upsert, want 1", n)
	}

	// A real change to i3 re-renders Y exactly once.
	if _, err := c.ApplyBatch([]ItemChange{
		{Kind: Upsert, ID: "i3", SoldOut: Provide(true)},
	}); err != nil {
		t.Fatal(err)
	}
	b, _, err := c.GetBoard("Y")
	if err != nil {
		t.Fatal(err)
	}
	if n := rc.get("Y"); n != 2 {
		t.Fatalf("renders[Y] = %d after real change, want 2", n)
	}
	if it := findItem(t, b, "i3"); !it.SoldOut {
		t.Errorf("i3 not sold out, want sold out")
	}
}

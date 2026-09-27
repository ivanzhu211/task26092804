package menuboard

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type renderCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func (rc *renderCounter) hook(id string, _ uint64) {
	rc.mu.Lock()
	rc.counts[id]++
	rc.mu.Unlock()
}

func (rc *renderCounter) count(id string) int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.counts[id]
}

// Unrelated batches must not trigger re-renders, no-op upserts must not
// trigger re-renders, and concurrent misses on the same display at the same
// version must render exactly once.
func TestRenderCount(t *testing.T) {
	rc := &renderCounter{counts: map[string]int{}}
	st := New(rc.hook)

	if _, err := st.ApplyBatch([]Change{
		{ID: "x", Name: strp("X"), Regular: i64p(100)},
		{ID: "y", Name: strp("Y"), Regular: i64p(200)},
		{ID: "z", Name: strp("Z"), Regular: i64p(300)},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := st.DefineBoard("A", BoardDef{Sections: []SectionDef{{ItemIDs: []string{"x", "y"}}}}); err != nil {
		t.Fatalf("define A: %v", err)
	}
	if _, err := st.DefineBoard("B", BoardDef{Sections: []SectionDef{{ItemIDs: []string{"z"}}}}); err != nil {
		t.Fatalf("define B: %v", err)
	}

	mustGet := func(id string) *Board {
		t.Helper()
		b, _, err := st.GetBoard(id)
		if err != nil {
			t.Fatalf("GetBoard(%s): %v", id, err)
		}
		return b
	}

	mustGet("A")
	mustGet("A")
	if got := rc.count("A"); got != 1 {
		t.Fatalf("A rendered %d times, want 1", got)
	}

	// A batch touching only B's item must not re-render A.
	if _, err := st.ApplyBatch([]Change{{ID: "z", Regular: i64p(301)}}); err != nil {
		t.Fatalf("batch z: %v", err)
	}
	mustGet("A")
	if got := rc.count("A"); got != 1 {
		t.Fatalf("A rendered %d times after unrelated batch, want 1", got)
	}
	mustGet("B")
	if got := rc.count("B"); got != 1 {
		t.Fatalf("B rendered %d times, want 1", got)
	}

	// A no-op upsert (same value) must not re-render A.
	if _, err := st.ApplyBatch([]Change{{ID: "x", Regular: i64p(100)}}); err != nil {
		t.Fatalf("noop batch: %v", err)
	}
	mustGet("A")
	if got := rc.count("A"); got != 1 {
		t.Fatalf("A rendered %d times after no-op batch, want 1", got)
	}

	// A real change to x re-renders A exactly once.
	if _, err := st.ApplyBatch([]Change{{ID: "x", Regular: i64p(500)}}); err != nil {
		t.Fatalf("batch x: %v", err)
	}
	mustGet("A")
	if got := rc.count("A"); got != 2 {
		t.Fatalf("A rendered %d times after x changed, want 2", got)
	}

	// Concurrent misses on B at the same version render exactly once.
	if _, err := st.ApplyBatch([]Change{{ID: "z", Regular: i64p(302)}}); err != nil {
		t.Fatalf("batch z2: %v", err)
	}
	const n = 8
	var wg sync.WaitGroup
	boards := make([]*Board, n)
	versions := make([]uint64, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b, v, err := st.GetBoard("B")
			if err != nil {
				t.Errorf("GetBoard(B) goroutine %d: %v", i, err)
				return
			}
			boards[i] = b
			versions[i] = v
		}(i)
	}
	wg.Wait()
	if got := rc.count("B"); got != 2 {
		t.Fatalf("B rendered %d times for concurrent misses, want 2", got)
	}
	for i := 1; i < n; i++ {
		if boards[i] != boards[0] {
			t.Fatalf("goroutine %d got a different board pointer", i)
		}
		if versions[i] != versions[0] {
			t.Fatalf("goroutine %d got version %d, want %d", i, versions[i], versions[0])
		}
	}
	if it := boardItem(boards[0], "z"); it == nil || it.Regular != 302 {
		t.Fatalf("z = %+v, want regular 302", it)
	}

	// Replacing a definition re-renders that display.
	if _, err := st.DefineBoard("A", BoardDef{Sections: []SectionDef{{ItemIDs: []string{"y"}}}}); err != nil {
		t.Fatalf("redefine A: %v", err)
	}
	b := mustGet("A")
	if got := rc.count("A"); got != 3 {
		t.Fatalf("A rendered %d times after redefine, want 3", got)
	}
	if boardItem(b, "x") != nil {
		t.Fatalf("redefined A still contains x")
	}
	if it := boardItem(b, "y"); it == nil || it.Regular != 200 {
		t.Fatalf("redefined A y = %+v, want regular 200", it)
	}
}

// A blocked render must not delay renders of other displays.
func TestBlockedHookDoesNotStallOthers(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	var blockA atomicFlag
	hook := func(id string, _ uint64) {
		if id == "A" && blockA.load() {
			entered <- struct{}{}
			<-release
		}
	}
	st := New(hook)
	if _, err := st.ApplyBatch([]Change{
		{ID: "x", Name: strp("X"), Regular: i64p(1)},
		{ID: "z", Name: strp("Z"), Regular: i64p(2)},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := st.DefineBoard("A", BoardDef{Sections: []SectionDef{{ItemIDs: []string{"x"}}}}); err != nil {
		t.Fatalf("define A: %v", err)
	}
	if _, err := st.DefineBoard("B", BoardDef{Sections: []SectionDef{{ItemIDs: []string{"z"}}}}); err != nil {
		t.Fatalf("define B: %v", err)
	}

	blockA.store(true)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = st.GetBoard("A")
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("render of A did not reach the hook")
	}
	mustDoWithin(t, 2*time.Second, "GetBoard(B) while A blocked", func() {
		if _, _, err := st.GetBoard("B"); err != nil {
			t.Errorf("GetBoard(B): %v", err)
		}
	})
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("GetBoard(A) did not return after release")
	}
}

type atomicFlag struct{ v atomic.Bool }

func (f *atomicFlag) load() bool   { return f.v.Load() }
func (f *atomicFlag) store(b bool) { f.v.Store(b) }

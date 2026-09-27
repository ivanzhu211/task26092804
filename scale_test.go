package menuboard

import (
	"fmt"
	"testing"
)

// TestScale smoke-tests the design point of 100k items and 5000 displays:
// writes must stay proportional to the batch size and affected displays, and
// unaffected displays must serve repeats from cache.
func TestScale(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test skipped in -short mode")
	}
	const (
		nItems    = 100000
		nDisplays = 5000
	)
	rc := &renderCounter{counts: map[string]int{}}
	st := New(rc.hook)

	// Seed 100k items in batches of 200.
	for base := 0; base < nItems; base += MaxBatchSize {
		changes := make([]Change, MaxBatchSize)
		for k := range changes {
			id := fmt.Sprintf("i%d", base+k)
			changes[k] = Change{ID: id, Name: strp("item-" + id), Regular: i64p(int64(base + k))}
		}
		if _, err := st.ApplyBatch(changes); err != nil {
			t.Fatalf("seed batch at %d: %v", base, err)
		}
	}

	// Define 5000 displays, each with 6 sections x 40 items.
	for d := 0; d < nDisplays; d++ {
		def := BoardDef{Sections: make([]SectionDef, MaxSections)}
		for s := range def.Sections {
			ids := make([]string, MaxSectionSize)
			for k := range ids {
				ids[k] = fmt.Sprintf("i%d", (d*MaxSectionSize+s*MaxSectionSize+k)%nItems)
			}
			def.Sections[s] = SectionDef{ItemIDs: ids}
		}
		if _, err := st.DefineBoard(fmt.Sprintf("d%d", d), def); err != nil {
			t.Fatalf("define d%d: %v", d, err)
		}
	}

	// Render every board once.
	for d := 0; d < nDisplays; d++ {
		if _, _, err := st.GetBoard(fmt.Sprintf("d%d", d)); err != nil {
			t.Fatalf("GetBoard d%d: %v", d, err)
		}
	}
	if got := rc.count("d0"); got != 1 {
		t.Fatalf("d0 rendered %d times, want 1", got)
	}

	// A batch touching items of exactly one display must not re-render others.
	// Display d references items (d*40 + s*40 + k) % nItems; use d0's first item.
	if _, err := st.ApplyBatch([]Change{{ID: "i0", Regular: i64p(999999)}}); err != nil {
		t.Fatalf("touch batch: %v", err)
	}
	for d := 0; d < nDisplays; d++ {
		if _, _, err := st.GetBoard(fmt.Sprintf("d%d", d)); err != nil {
			t.Fatalf("GetBoard d%d after touch: %v", d, err)
		}
	}
	// Every display rendered once initially; only the handful of displays
	// referencing i0 may have rendered again.
	total := 0
	for d := 0; d < nDisplays; d++ {
		total += rc.count(fmt.Sprintf("d%d", d))
	}
	if total >= nDisplays+100 {
		t.Fatalf("too many re-renders after single-item change: %d", total)
	}
	if got := rc.count("d0"); got != 2 {
		t.Fatalf("d0 rendered %d times, want 2", got)
	}
}

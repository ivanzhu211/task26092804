package menuboard

import (
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// refSnapshot is the full reference state at one version.
type refSnapshot struct {
	items map[string]Item
	defs  map[string]BoardDef
}

// refStore is an independent, lock-serialized reference implementation that
// records the state at every version.
type refStore struct {
	mu       sync.Mutex
	items    map[string]Item
	defs     map[string]BoardDef
	history  map[uint64]refSnapshot
	maxVer   uint64
	complete atomic.Uint64 // max version whose write has fully returned
}

func newRefStore() *refStore {
	return &refStore{
		items:   make(map[string]Item),
		defs:    make(map[string]BoardDef),
		history: make(map[uint64]refSnapshot),
	}
}

func (r *refStore) snapshotLocked() {
	items := make(map[string]Item, len(r.items))
	for k, v := range r.items {
		items[k] = v
	}
	defs := make(map[string]BoardDef, len(r.defs))
	for k, v := range r.defs {
		defs[k] = v
	}
	r.history[r.maxVer] = refSnapshot{items: items, defs: defs}
}

func (r *refStore) defineBoard(id string, def BoardDef) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defs[id] = def
	r.maxVer++
	r.snapshotLocked()
	v := r.maxVer
	r.complete.Store(v)
	return v
}

func (r *refStore) applyBatch(changes []Change) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ch := range changes {
		if ch.Delete {
			delete(r.items, ch.ID)
			continue
		}
		it := r.items[ch.ID]
		it.ID = ch.ID
		if ch.Name != nil {
			it.Name = *ch.Name
		}
		if ch.Regular != nil {
			it.Regular = *ch.Regular
		}
		if ch.Combo != nil {
			v := *ch.Combo
			it.Combo = &v
		}
		if ch.ClearCombo {
			it.Combo = nil
		}
		if ch.Large != nil {
			v := *ch.Large
			it.Large = &v
		}
		if ch.ClearLarge {
			it.Large = nil
		}
		if ch.SoldOut != nil {
			it.SoldOut = *ch.SoldOut
		}
		r.items[ch.ID] = it
	}
	r.maxVer++
	r.snapshotLocked()
	v := r.maxVer
	r.complete.Store(v)
	return v
}

// at returns the snapshot for version v. A GetBoard can observe a store
// version whose write wrapper has not recorded the reference snapshot yet
// (the write pair is serialized, but readers are not part of that critical
// section), so wait briefly for the recorder to catch up.
func (r *refStore) at(v uint64) (refSnapshot, bool) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		snap, ok := r.history[v]
		r.mu.Unlock()
		if ok {
			return snap, true
		}
		if time.Now().After(deadline) {
			return refSnapshot{}, false
		}
		time.Sleep(time.Millisecond)
	}
}

// refRender is the independent reference renderer.
func refRender(def BoardDef, items map[string]Item) *Board {
	b := &Board{Sections: make([]BoardSection, len(def.Sections))}
	for i, sec := range def.Sections {
		for _, id := range sec.ItemIDs {
			if it, ok := items[id]; ok {
				b.Sections[i].Items = append(b.Sections[i].Items, it)
			}
		}
	}
	return b
}

func checkBoardEqual(got *Board, want *Board) error {
	if len(got.Sections) != len(want.Sections) {
		return fmt.Errorf("sections: got %d want %d", len(got.Sections), len(want.Sections))
	}
	for i := range got.Sections {
		g, w := got.Sections[i].Items, want.Sections[i].Items
		if len(g) != len(w) {
			return fmt.Errorf("section %d: got %d items want %d", i, len(g), len(w))
		}
		for j := range g {
			gi, wi := g[j], w[j]
			if gi.ID != wi.ID || gi.Name != wi.Name || gi.Regular != wi.Regular ||
				gi.SoldOut != wi.SoldOut || !priceEq(gi.Combo, wi.Combo) || !priceEq(gi.Large, wi.Large) {
				return fmt.Errorf("section %d item %d: got %+v want %+v", i, j, gi, wi)
			}
		}
	}
	return nil
}

// TestStress hammers the store with concurrent mixed operations and checks
// every returned board against the reference implementation for consistency,
// freshness, and monotonicity.
func TestStress(t *testing.T) {
	st := New(nil)
	ref := newRefStore()

	const (
		workers   = 8
		iters     = 200
		nDisplays = 30
		nItems    = 200
	)
	displayID := func(i int) string { return fmt.Sprintf("d%d", i) }
	itemID := func(i int) string { return fmt.Sprintf("i%d", i) }

	var maxReturned atomic.Uint64

	// Writes are serialized through writeMu so the store and the reference
	// see them in the same order and version numbers line up 1:1.
	var writeMu sync.Mutex
	defineBoard := func(id string, def BoardDef) {
		writeMu.Lock()
		defer writeMu.Unlock()
		v, err := st.DefineBoard(id, def)
		if err != nil {
			t.Errorf("DefineBoard(%s): %v", id, err)
			return
		}
		if rv := ref.defineBoard(id, def); rv != v {
			t.Errorf("DefineBoard version: store %d ref %d", v, rv)
		}
	}
	applyBatch := func(changes []Change) {
		writeMu.Lock()
		defer writeMu.Unlock()
		v, err := st.ApplyBatch(changes)
		if err != nil {
			t.Errorf("ApplyBatch: %v", err)
			return
		}
		if rv := ref.applyBatch(changes); rv != v {
			t.Errorf("ApplyBatch version: store %d ref %d", v, rv)
		}
	}

	// Predefine every display so GetBoard never fails for them.
	for i := 0; i < nDisplays; i++ {
		defineBoard(displayID(i), BoardDef{})
	}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for it := 0; it < iters; it++ {
				switch rng.Intn(10) {
				case 0, 1, 2: // DefineBoard
					id := displayID(rng.Intn(nDisplays))
					nSec := 1 + rng.Intn(MaxSections)
					def := BoardDef{Sections: make([]SectionDef, nSec)}
					for s := range def.Sections {
						n := 1 + rng.Intn(15)
						ids := make([]string, n)
						for k := range ids {
							ids[k] = itemID(rng.Intn(nItems))
						}
						def.Sections[s] = SectionDef{ItemIDs: ids}
					}
					defineBoard(id, def)
				case 3, 4, 5: // ApplyBatch
					n := 1 + rng.Intn(5)
					changes := make([]Change, n)
					for k := range changes {
						id := itemID(rng.Intn(nItems))
						if rng.Intn(10) < 3 {
							changes[k] = Change{ID: id, Delete: true}
							continue
						}
						ch := Change{
							ID:      id,
							Name:    strp(fmt.Sprintf("item-%s", id)),
							Regular: i64p(int64(rng.Intn(10000))),
						}
						switch rng.Intn(3) {
						case 0:
							ch.Combo = i64p(int64(rng.Intn(10000)))
						case 1:
							ch.ClearCombo = true
						}
						switch rng.Intn(3) {
						case 0:
							ch.Large = i64p(int64(rng.Intn(10000)))
						case 1:
							ch.ClearLarge = true
						}
						if rng.Intn(2) == 0 {
							ch.SoldOut = boolp(rng.Intn(2) == 0)
						}
						changes[k] = ch
					}
					applyBatch(changes)
				default: // GetBoard
					id := displayID(rng.Intn(nDisplays))
					if rng.Intn(50) == 0 {
						id = "never-defined"
					}
					startVer := ref.complete.Load()
					startMaxRet := maxReturned.Load()
					b, v, err := st.GetBoard(id)
					if id == "never-defined" {
						if err == nil {
							t.Errorf("GetBoard(never-defined) succeeded")
						}
						continue
					}
					if err != nil {
						t.Errorf("GetBoard(%s): %v", id, err)
						continue
					}
					// Freshness: a write that returned v before this call
					// started must be reflected in the returned version.
					if v < startVer {
						t.Errorf("freshness: GetBoard(%s) version %d < %d at call start", id, v, startVer)
					}
					// Monotonicity: no GetBoard that returned before this one
					// started may have a higher version.
					if v < startMaxRet {
						t.Errorf("monotonicity: GetBoard(%s) version %d < previously returned %d", id, v, startMaxRet)
					}
					for {
						cur := maxReturned.Load()
						if v >= cur || maxReturned.CompareAndSwap(cur, v) {
							break
						}
					}
					// Consistency: the board must equal a from-scratch render
					// of the reference state at the returned version.
					snap, ok := ref.at(v)
					if !ok {
						t.Errorf("GetBoard(%s) version %d: version never recorded by reference", id, v)
						continue
					}
					def, ok := snap.defs[id]
					if !ok {
						t.Errorf("GetBoard(%s) version %d: no def at that version", id, v)
						continue
					}
					want := refRender(def, snap.items)
					if e := checkBoardEqual(b, want); e != nil {
						t.Errorf("consistency: GetBoard(%s) version %d: %v", id, v, e)
					}
				}
			}
		}(int64(w) + 1)
	}
	wg.Wait()
}

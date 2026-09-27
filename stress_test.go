package menuboard

import (
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// refState is a full snapshot of the reference implementation's state.
type refState struct {
	items map[string]Item
	defs  map[string]BoardDef
}

func cloneItem(it Item) Item {
	it.MealPrice = copyI64(it.MealPrice)
	it.LargePrice = copyI64(it.LargePrice)
	return it
}

func cloneDef(d BoardDef) BoardDef {
	out := BoardDef{Sections: make([][]string, len(d.Sections))}
	for i, sec := range d.Sections {
		s := make([]string, len(sec))
		copy(s, sec)
		out.Sections[i] = s
	}
	return out
}

func (s refState) clone() refState {
	items := make(map[string]Item, len(s.items))
	for k, v := range s.items {
		items[k] = cloneItem(v)
	}
	defs := make(map[string]BoardDef, len(s.defs))
	for k, v := range s.defs {
		defs[k] = cloneDef(v)
	}
	return refState{items: items, defs: defs}
}

// reference is an independent, obviously-correct implementation that
// records the full state at every version. Writes are applied strictly in
// version order, buffered if a goroutine records out of order.
type reference struct {
	mu      sync.Mutex
	cond    *sync.Cond
	seq     uint64
	cur     refState
	history map[uint64]refState
	pending map[uint64]func(*refState)
}

func newReference() *reference {
	r := &reference{
		cur:     refState{items: map[string]Item{}, defs: map[string]BoardDef{}},
		history: make(map[uint64]refState),
		pending: make(map[uint64]func(*refState)),
	}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (r *reference) recordWrite(v uint64, apply func(*refState)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending[v] = apply
	for {
		f, ok := r.pending[r.seq+1]
		if !ok {
			break
		}
		delete(r.pending, r.seq+1)
		r.seq++
		f(&r.cur)
		r.history[r.seq] = r.cur.clone()
	}
	r.cond.Broadcast()
}

// boardAt renders the expected board for display at version v from the
// recorded state, waiting for the corresponding writes to be recorded.
func (r *reference) boardAt(display string, v uint64) (Board, bool) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		r.mu.Lock()
		if r.seq >= v {
			st := r.history[v]
			def, ok := st.defs[display]
			if !ok {
				r.mu.Unlock()
				return Board{}, false
			}
			b := Board{Sections: make([][]Item, len(def.Sections))}
			for i, sec := range def.Sections {
				items := make([]Item, 0, len(sec))
				for _, id := range sec {
					if it, ok := st.items[id]; ok {
						items = append(items, cloneItem(it))
					}
				}
				b.Sections[i] = items
			}
			r.mu.Unlock()
			return b, true
		}
		r.mu.Unlock()
		if time.Now().After(deadline) {
			return Board{}, false
		}
		time.Sleep(time.Millisecond)
	}
}

func applyBatchToRef(s *refState, changes []ItemChange) {
	for _, ch := range changes {
		switch ch.Kind {
		case Delete:
			delete(s.items, ch.ID)
		case Upsert:
			it := cloneItem(s.items[ch.ID])
			it.ID = ch.ID
			if ch.Name.Set {
				it.Name = ch.Name.Value
			}
			if ch.BasePrice.Set {
				it.BasePrice = ch.BasePrice.Value
			}
			if ch.MealPrice.Set {
				it.MealPrice = copyI64(ch.MealPrice.Value)
			}
			if ch.LargePrice.Set {
				it.LargePrice = copyI64(ch.LargePrice.Value)
			}
			if ch.SoldOut.Set {
				it.SoldOut = ch.SoldOut.Value
			}
			s.items[ch.ID] = it
		}
	}
}

func ptrEqual(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func boardsEqual(a, b Board) bool {
	if len(a.Sections) != len(b.Sections) {
		return false
	}
	for i := range a.Sections {
		if len(a.Sections[i]) != len(b.Sections[i]) {
			return false
		}
		for j := range a.Sections[i] {
			x, y := a.Sections[i][j], b.Sections[i][j]
			if x.ID != y.ID || x.Name != y.Name || x.BasePrice != y.BasePrice ||
				x.SoldOut != y.SoldOut ||
				!ptrEqual(x.MealPrice, y.MealPrice) || !ptrEqual(x.LargePrice, y.LargePrice) {
				return false
			}
		}
	}
	return true
}

func randomChange(rng *rand.Rand, itemIDs []string) ItemChange {
	id := itemIDs[rng.Intn(len(itemIDs))]
	if rng.Intn(100) < 20 {
		return ItemChange{Kind: Delete, ID: id}
	}
	ch := ItemChange{
		Kind:      Upsert,
		ID:        id,
		Name:      Provide(fmt.Sprintf("name-%s-%d", id, rng.Intn(1000))),
		BasePrice: Provide(int64(rng.Intn(10000))),
	}
	if rng.Intn(100) < 50 {
		if rng.Intn(100) < 25 {
			ch.MealPrice = Provide[*int64](nil) // clear
		} else {
			p := int64(rng.Intn(10000))
			ch.MealPrice = Provide(&p)
		}
	}
	if rng.Intn(100) < 50 {
		if rng.Intn(100) < 25 {
			ch.LargePrice = Provide[*int64](nil)
		} else {
			p := int64(rng.Intn(10000))
			ch.LargePrice = Provide(&p)
		}
	}
	if rng.Intn(100) < 30 {
		ch.SoldOut = Provide(rng.Intn(2) == 0)
	}
	return ch
}

func randomDef(rng *rand.Rand, itemIDs []string) BoardDef {
	nSec := rng.Intn(4)
	def := BoardDef{Sections: make([][]string, nSec)}
	for i := range def.Sections {
		n := rng.Intn(11)
		sec := make([]string, n)
		for j := range sec {
			sec[j] = itemIDs[rng.Intn(len(itemIDs))]
		}
		def.Sections[i] = sec
	}
	return def
}

// Concurrent goroutines randomly mix the three operations while an
// independent reference implementation checks every returned board for
// consistency, freshness and monotonicity.
func TestStressConcurrentMixedOps(t *testing.T) {
	const (
		workers   = 8
		iters     = 400
		nItems    = 30
		nDisplays = 8
	)
	itemIDs := make([]string, nItems)
	for i := range itemIDs {
		itemIDs[i] = fmt.Sprintf("item-%02d", i)
	}
	displayIDs := make([]string, nDisplays)
	for i := range displayIDs {
		displayIDs[i] = fmt.Sprintf("disp-%d", i)
	}

	c := New(nil)
	ref := newReference()

	// Initial setup: define all displays and create all items.
	setupRng := rand.New(rand.NewSource(1))
	for _, d := range displayIDs {
		def := randomDef(setupRng, itemIDs)
		v, err := c.DefineBoard(d, def)
		if err != nil {
			t.Fatal(err)
		}
		dd, disp := def, d
		ref.recordWrite(v, func(s *refState) { s.defs[disp] = cloneDef(dd) })
	}
	var init []ItemChange
	for _, id := range itemIDs {
		init = append(init, ItemChange{
			Kind:      Upsert,
			ID:        id,
			Name:      Provide("init-" + id),
			BasePrice: Provide(int64(1000)),
		})
	}
	v, err := c.ApplyBatch(init)
	if err != nil {
		t.Fatal(err)
	}
	ref.recordWrite(v, func(s *refState) { applyBatchToRef(s, init) })

	var maxReturned atomic.Uint64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			var lastWrite uint64
			for i := 0; i < iters; i++ {
				switch op := rng.Intn(100); {
				case op < 40: // GetBoard
					d := displayIDs[rng.Intn(nDisplays)]
					m0 := maxReturned.Load()
					b, ver, err := c.GetBoard(d)
					if err != nil {
						t.Errorf("GetBoard(%s): %v", d, err)
						return
					}
					if ver < m0 {
						t.Errorf("monotonicity violated: GetBoard returned %d after %d was returned", ver, m0)
						return
					}
					if ver < lastWrite {
						t.Errorf("freshness violated: GetBoard returned %d after own write %d", ver, lastWrite)
						return
					}
					for {
						cur := maxReturned.Load()
						if ver <= cur || maxReturned.CompareAndSwap(cur, ver) {
							break
						}
					}
					want, ok := ref.boardAt(d, ver)
					if !ok {
						t.Errorf("no reference state for display %s at version %d", d, ver)
						return
					}
					if !boardsEqual(want, b) {
						t.Errorf("inconsistent board for %s at version %d\nwant %+v\ngot  %+v", d, ver, want, b)
						return
					}
				case op < 70: // ApplyBatch
					n := 1 + rng.Intn(5)
					changes := make([]ItemChange, n)
					for j := range changes {
						changes[j] = randomChange(rng, itemIDs)
					}
					ver, err := c.ApplyBatch(changes)
					if err != nil {
						t.Errorf("ApplyBatch rejected: %v", err)
						return
					}
					ref.recordWrite(ver, func(s *refState) { applyBatchToRef(s, changes) })
					if ver > lastWrite {
						lastWrite = ver
					}
				default: // DefineBoard
					d := displayIDs[rng.Intn(nDisplays)]
					def := randomDef(rng, itemIDs)
					ver, err := c.DefineBoard(d, def)
					if err != nil {
						t.Errorf("DefineBoard rejected: %v", err)
						return
					}
					ref.recordWrite(ver, func(s *refState) { s.defs[d] = cloneDef(def) })
					if ver > lastWrite {
						lastWrite = ver
					}
				}
			}
		}(int64(w + 2))
	}
	wg.Wait()
}

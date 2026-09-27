package menuboard

import (
	"fmt"
	"sync"
	"sync/atomic"
)

const (
	maxSections     = 6
	maxSectionItems = 40
	maxBatchSize    = 200
)

// itemState is the immutable stored form of an item. Updates replace the
// pointer in the items map, never mutate in place, so snapshots taken under
// writeMu stay valid after the lock is released.
type itemState struct {
	name       string
	basePrice  int64
	mealPrice  *int64
	largePrice *int64
	soldOut    bool
}

// cacheEntry is an immutable rendered board plus the version it matches.
type cacheEntry struct {
	board   Board
	version uint64
}

// displayEntry holds the per-display state. The atomic fields allow
// cache-hit GetBoard to run without acquiring any shared lock.
type displayEntry struct {
	def   atomic.Pointer[BoardDef]   // current definition (immutable once stored)
	cache atomic.Pointer[cacheEntry] // latest rendered/revalidated board
	// dirtyVersion is the latest version at which this display was affected
	// (definition replaced, or a referenced item changed). Writers store it
	// before publishing the version, readers load it after loading the
	// version, so a stale cache is never mistaken for a fresh one.
	dirtyVersion atomic.Uint64
	renderMu     sync.Mutex // per-display singleflight for renders
}

// Cache is an in-memory materialized-view cache of rendered menu boards.
// Create one with New.
type Cache struct {
	hook RenderHook

	// writeMu serializes writers and short render snapshots. It is never
	// held while calling the hook or building a board.
	writeMu sync.Mutex
	// version is the shared strictly increasing version sequence. Writers
	// bump it under writeMu; readers only load it.
	version atomic.Uint64
	// items is the current item state, guarded by writeMu.
	items map[string]*itemState
	// refs is the reverse index itemID -> set of displayIDs referencing it,
	// guarded by writeMu. Entries survive item deletion so a later
	// re-create still dirties the right displays; they are removed only
	// when no definition references the item anymore.
	refs map[string]map[string]struct{}
	// displays maps displayID -> *displayEntry. Loads that hit the
	// read-only copy take no lock, so cache-hit GetBoard stays lock-free.
	displays sync.Map
}

// New creates a Cache. hook may be nil; otherwise it is called once per
// actual render with the display ID and the rendered version.
func New(hook RenderHook) *Cache {
	return &Cache{
		hook:  hook,
		items: make(map[string]*itemState),
		refs:  make(map[string]map[string]struct{}),
	}
}

// DefineBoard creates or replaces the board definition of a display and
// returns the new version.
func (c *Cache) DefineBoard(displayID string, def BoardDef) (uint64, error) {
	if len(def.Sections) > maxSections {
		return 0, fmt.Errorf("%w: %d sections > %d", ErrInvalidDef, len(def.Sections), maxSections)
	}
	for i, sec := range def.Sections {
		if len(sec) > maxSectionItems {
			return 0, fmt.Errorf("%w: section %d has %d items > %d", ErrInvalidDef, i, len(sec), maxSectionItems)
		}
	}
	// Deep copy so later caller mutation cannot affect the stored def.
	cp := BoardDef{Sections: make([][]string, len(def.Sections))}
	for i, sec := range def.Sections {
		s := make([]string, len(sec))
		copy(s, sec)
		cp.Sections[i] = s
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	v := c.version.Load() + 1
	entry := c.displayEntry(displayID)
	if old := entry.def.Load(); old != nil {
		for _, sec := range old.Sections {
			for _, id := range sec {
				c.removeRef(id, displayID)
			}
		}
	}
	for _, sec := range cp.Sections {
		for _, id := range sec {
			c.addRef(id, displayID)
		}
	}
	entry.def.Store(&cp)
	entry.dirtyVersion.Store(v)
	c.version.Store(v)
	return v, nil
}

// ApplyBatch atomically applies up to 200 item changes and returns the new
// version. If any change is invalid the whole batch is rejected and no
// version is consumed.
func (c *Cache) ApplyBatch(changes []ItemChange) (uint64, error) {
	if len(changes) == 0 {
		return 0, ErrEmptyBatch
	}
	if len(changes) > maxBatchSize {
		return 0, fmt.Errorf("%w: %d changes", ErrTooManyChanges, len(changes))
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	// Phase 1: validate every change against the simulated state, applying
	// repeated changes to the same item in order. Nothing is mutated yet,
	// so an invalid change rejects the batch without consuming a version.
	type simEntry struct{ state *itemState } // nil state = does not exist
	sim := make(map[string]simEntry, len(changes))
	lookup := func(id string) *itemState {
		if e, ok := sim[id]; ok {
			return e.state
		}
		return c.items[id]
	}
	for i, ch := range changes {
		switch ch.Kind {
		case Delete:
			sim[ch.ID] = simEntry{}
		case Upsert:
			cur := lookup(ch.ID)
			if cur == nil {
				if !ch.Name.Set || !ch.BasePrice.Set {
					return 0, fmt.Errorf("%w: change %d: new item %q requires name and base price", ErrInvalidChange, i, ch.ID)
				}
				st := &itemState{name: ch.Name.Value, basePrice: ch.BasePrice.Value}
				if ch.MealPrice.Set {
					st.mealPrice = copyI64(ch.MealPrice.Value)
				}
				if ch.LargePrice.Set {
					st.largePrice = copyI64(ch.LargePrice.Value)
				}
				if ch.SoldOut.Set {
					st.soldOut = ch.SoldOut.Value
				}
				sim[ch.ID] = simEntry{st}
			} else {
				st := *cur
				if ch.Name.Set {
					st.name = ch.Name.Value
				}
				if ch.BasePrice.Set {
					st.basePrice = ch.BasePrice.Value
				}
				if ch.MealPrice.Set {
					st.mealPrice = copyI64(ch.MealPrice.Value)
				}
				if ch.LargePrice.Set {
					st.largePrice = copyI64(ch.LargePrice.Value)
				}
				if ch.SoldOut.Set {
					st.soldOut = ch.SoldOut.Value
				}
				sim[ch.ID] = simEntry{&st}
			}
		default:
			return 0, fmt.Errorf("%w: change %d: unknown kind %d", ErrInvalidChange, i, ch.Kind)
		}
	}

	// Phase 2: commit. Only items whose field values actually changed
	// (including creation and deletion) dirty their referencing displays.
	v := c.version.Load() + 1
	affected := make(map[string]struct{})
	for id, e := range sim {
		old := c.items[id]
		if e.state == nil {
			if old != nil {
				delete(c.items, id)
				for displayID := range c.refs[id] {
					affected[displayID] = struct{}{}
				}
			}
			continue
		}
		if old != nil && statesEqual(old, e.state) {
			continue
		}
		c.items[id] = e.state
		for displayID := range c.refs[id] {
			affected[displayID] = struct{}{}
		}
	}
	for displayID := range affected {
		if e, ok := c.displays.Load(displayID); ok {
			e.(*displayEntry).dirtyVersion.Store(v)
		}
	}
	c.version.Store(v)
	return v, nil
}

// GetBoard returns the rendered board of a display and its version.
// The returned Board is a fresh copy; the caller may mutate it freely.
func (c *Cache) GetBoard(displayID string) (Board, uint64, error) {
	v0 := c.version.Load()
	e, ok := c.displays.Load(displayID)
	if !ok {
		return Board{}, 0, ErrBoardNotDefined
	}
	entry := e.(*displayEntry)
	// Cache hit: no locks, no waiting.
	if ce := entry.cache.Load(); ce != nil && ce.version >= v0 {
		return copyBoard(ce.board), ce.version, nil
	}

	entry.renderMu.Lock()
	defer entry.renderMu.Unlock()
	// Re-check under renderMu: another goroutine may have rendered, or the
	// cached board may simply need its version bumped (revalidation) when
	// no write since the cached version affected this display.
	if ce := entry.cache.Load(); ce != nil {
		cur := c.version.Load()
		if ce.version >= cur {
			return copyBoard(ce.board), ce.version, nil
		}
		if ce.version >= entry.dirtyVersion.Load() {
			entry.cache.Store(&cacheEntry{board: ce.board, version: cur})
			return copyBoard(ce.board), cur, nil
		}
	}
	return c.render(displayID, entry)
}

// render snapshots the definition and referenced items at the current
// version under writeMu, then renders outside any lock. Callers must hold
// entry.renderMu.
func (c *Cache) render(displayID string, entry *displayEntry) (Board, uint64, error) {
	c.writeMu.Lock()
	v := c.version.Load()
	def := entry.def.Load()
	var snap map[string]*itemState
	if def != nil {
		snap = make(map[string]*itemState, maxSections*maxSectionItems)
		for _, sec := range def.Sections {
			for _, id := range sec {
				if _, ok := snap[id]; !ok {
					if st, ok := c.items[id]; ok {
						snap[id] = st
					}
				}
			}
		}
	}
	c.writeMu.Unlock()

	if def == nil {
		return Board{}, 0, ErrBoardNotDefined
	}
	if c.hook != nil {
		c.hook(displayID, v)
	}
	board := Board{Sections: make([][]Item, len(def.Sections))}
	for i, sec := range def.Sections {
		items := make([]Item, 0, len(sec))
		for _, id := range sec {
			if st, ok := snap[id]; ok {
				items = append(items, itemFromState(id, st))
			}
		}
		board.Sections[i] = items
	}
	entry.cache.Store(&cacheEntry{board: board, version: v})
	return copyBoard(board), v, nil
}

func (c *Cache) displayEntry(displayID string) *displayEntry {
	if e, ok := c.displays.Load(displayID); ok {
		return e.(*displayEntry)
	}
	entry := &displayEntry{}
	actual, _ := c.displays.LoadOrStore(displayID, entry)
	return actual.(*displayEntry)
}

func (c *Cache) addRef(itemID, displayID string) {
	set := c.refs[itemID]
	if set == nil {
		set = make(map[string]struct{})
		c.refs[itemID] = set
	}
	set[displayID] = struct{}{}
}

func (c *Cache) removeRef(itemID, displayID string) {
	set := c.refs[itemID]
	if set == nil {
		return
	}
	delete(set, displayID)
	if len(set) == 0 {
		delete(c.refs, itemID)
	}
}

func copyI64(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func statesEqual(a, b *itemState) bool {
	return a.name == b.name &&
		a.basePrice == b.basePrice &&
		i64Equal(a.mealPrice, b.mealPrice) &&
		i64Equal(a.largePrice, b.largePrice) &&
		a.soldOut == b.soldOut
}

func i64Equal(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func itemFromState(id string, st *itemState) Item {
	return Item{
		ID:         id,
		Name:       st.name,
		BasePrice:  st.basePrice,
		MealPrice:  copyI64(st.mealPrice),
		LargePrice: copyI64(st.largePrice),
		SoldOut:    st.soldOut,
	}
}

// copyBoard deep-copies a board so callers cannot mutate cached state.
func copyBoard(b Board) Board {
	out := Board{Sections: make([][]Item, len(b.Sections))}
	for i, sec := range b.Sections {
		items := make([]Item, len(sec))
		for j, it := range sec {
			items[j] = Item{
				ID:         it.ID,
				Name:       it.Name,
				BasePrice:  it.BasePrice,
				MealPrice:  copyI64(it.MealPrice),
				LargePrice: copyI64(it.LargePrice),
				SoldOut:    it.SoldOut,
			}
		}
		out.Sections[i] = items
	}
	return out
}

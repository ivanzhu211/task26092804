package menuboard

func maxU64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

// valid reports whether e is a usable cache entry for ds.
func (ds *displayState) valid(e *cacheEntry) bool {
	return e != nil && e.dataVersion >= ds.dirty.Load()
}

func copyItem(it Item) Item {
	if it.Combo != nil {
		v := *it.Combo
		it.Combo = &v
	}
	if it.Large != nil {
		v := *it.Large
		it.Large = &v
	}
	return it
}

// renderBoard builds the board from a def and a snapshot of the referenced
// items. IDs missing from the snapshot are skipped.
func renderBoard(def *BoardDef, snap map[string]Item) *Board {
	b := &Board{Sections: make([]BoardSection, len(def.Sections))}
	for i, sec := range def.Sections {
		items := make([]Item, 0, len(sec.ItemIDs))
		for _, id := range sec.ItemIDs {
			if it, ok := snap[id]; ok {
				items = append(items, copyItem(it))
			}
		}
		b.Sections[i] = BoardSection{Items: items}
	}
	return b
}

// GetBoard returns the rendered board for a display and its version.
//
// The cache-hit fast path uses only atomic loads: it takes no shared lock
// and never waits on in-flight writes or renders.
func (s *Store) GetBoard(displayID string) (*Board, uint64, error) {
	v := s.global.Load()

	dsAny, ok := s.displays.Load(displayID)
	if !ok {
		return nil, 0, ErrBoardNotDefined
	}
	ds := dsAny.(*displayState)

	if e := ds.entry.Load(); ds.valid(e) {
		return e.board, maxU64(v, e.dataVersion), nil
	}

	// Miss path: per-display mutex gives singleflight semantics; a render
	// blocked in the hook only stalls other misses for the same display.
	ds.mu.Lock()
	defer ds.mu.Unlock()

	if e := ds.entry.Load(); ds.valid(e) {
		return e.board, maxU64(v, e.dataVersion), nil
	}

	// Snapshot the def and the referenced items at a consistent version.
	// The hook runs after the lock is released, so a blocked hook never
	// stalls writers or other displays.
	s.dataMu.RLock()
	def := s.defs[displayID]
	rv := s.global.Load()
	snap := make(map[string]Item)
	for _, sec := range def.Sections {
		for _, id := range sec.ItemIDs {
			if it, ok := s.items[id]; ok {
				snap[id] = it
			}
		}
	}
	s.dataMu.RUnlock()

	if s.hook != nil {
		s.hook(displayID, rv)
	}

	board := renderBoard(def, snap)
	ds.entry.Store(&cacheEntry{dataVersion: rv, board: board})
	return board, maxU64(v, rv), nil
}

package menuboard

import "fmt"

// getOrCreateDisplay returns the display state, creating it if needed.
// Callers hold dataMu (write) or are otherwise serialized.
func (s *Store) getOrCreateDisplay(displayID string) *displayState {
	if ds, ok := s.displays.Load(displayID); ok {
		return ds.(*displayState)
	}
	ds := &displayState{}
	actual, _ := s.displays.LoadOrStore(displayID, ds)
	return actual.(*displayState)
}

func copyDef(def BoardDef) *BoardDef {
	out := &BoardDef{Sections: make([]SectionDef, len(def.Sections))}
	for i, sec := range def.Sections {
		ids := make([]string, len(sec.ItemIDs))
		copy(ids, sec.ItemIDs)
		out.Sections[i] = SectionDef{ItemIDs: ids}
	}
	return out
}

func validateDef(displayID string, def BoardDef) error {
	if displayID == "" {
		return ErrEmptyDisplayID
	}
	if len(def.Sections) > MaxSections {
		return fmt.Errorf("%w: %d > %d", ErrTooManySections, len(def.Sections), MaxSections)
	}
	for _, sec := range def.Sections {
		if len(sec.ItemIDs) > MaxSectionSize {
			return fmt.Errorf("%w: %d > %d", ErrSectionTooLarge, len(sec.ItemIDs), MaxSectionSize)
		}
	}
	return nil
}

func validateChangeFields(ch Change) error {
	if ch.ID == "" {
		return ErrEmptyItemID
	}
	if ch.Delete {
		if ch.Name != nil || ch.Regular != nil || ch.Combo != nil || ch.Large != nil ||
			ch.SoldOut != nil || ch.ClearCombo || ch.ClearLarge {
			return fmt.Errorf("%w: item %q", ErrDeleteWithSet, ch.ID)
		}
	}
	return nil
}

// applyUpsert returns the item resulting from applying ch on top of cur
// (cur may be nil for a not-yet-existing item).
func applyUpsert(cur *Item, ch Change) (Item, error) {
	var it Item
	if cur != nil {
		it = *cur
	} else {
		if ch.Name == nil {
			return it, fmt.Errorf("%w: item %q", ErrMissingName, ch.ID)
		}
		if ch.Regular == nil {
			return it, fmt.Errorf("%w: item %q", ErrMissingRegular, ch.ID)
		}
		it.ID = ch.ID
	}
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
	return it, nil
}

func priceEq(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// itemsEqual reports whether two item states are field-by-field identical.
// Either may be nil (absent).
func itemsEqual(a, b *Item) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ID == b.ID &&
		a.Name == b.Name &&
		a.Regular == b.Regular &&
		priceEq(a.Combo, b.Combo) &&
		priceEq(a.Large, b.Large) &&
		a.SoldOut == b.SoldOut
}

func (s *Store) addRef(itemID, displayID string) {
	set := s.refs[itemID]
	if set == nil {
		set = make(map[string]struct{})
		s.refs[itemID] = set
	}
	set[displayID] = struct{}{}
}

func (s *Store) removeRef(itemID, displayID string) {
	set := s.refs[itemID]
	if set == nil {
		return
	}
	delete(set, displayID)
	if len(set) == 0 {
		delete(s.refs, itemID)
	}
}

// bumpVersionLocked advances the global version. Callers hold writeMu and
// dataMu; affected displays' dirty flags must be stored before this returns
// so that a GetBoard observing the new version also observes the dirty flag.
func (s *Store) bumpVersionLocked(affected []*displayState) uint64 {
	v := s.global.Load() + 1
	for _, ds := range affected {
		ds.dirty.Store(v)
	}
	s.global.Store(v)
	return v
}

// DefineBoard creates or replaces the definition of one display and returns
// the new version.
func (s *Store) DefineBoard(displayID string, def BoardDef) (uint64, error) {
	if err := validateDef(displayID, def); err != nil {
		return 0, err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.dataMu.Lock()
	defer s.dataMu.Unlock()

	if old := s.defs[displayID]; old != nil {
		for _, sec := range old.Sections {
			for _, id := range sec.ItemIDs {
				s.removeRef(id, displayID)
			}
		}
	}
	defCopy := copyDef(def)
	s.defs[displayID] = defCopy
	for _, sec := range defCopy.Sections {
		for _, id := range sec.ItemIDs {
			s.addRef(id, displayID)
		}
	}

	ds := s.getOrCreateDisplay(displayID)
	return s.bumpVersionLocked([]*displayState{ds}), nil
}

// ApplyBatch atomically applies up to MaxBatchSize item changes and returns
// the new version. If any change is invalid the whole batch is rejected and
// no version is consumed.
func (s *Store) ApplyBatch(changes []Change) (uint64, error) {
	if len(changes) > MaxBatchSize {
		return 0, fmt.Errorf("%w: %d > %d", ErrTooManyChanges, len(changes), MaxBatchSize)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.dataMu.Lock()
	defer s.dataMu.Unlock()

	// Pass 1: validate and simulate on an overlay. Nothing is mutated, so an
	// invalid change rejects the whole batch without consuming a version.
	overlay := make(map[string]*Item, len(changes)) // nil value = deleted
	for _, ch := range changes {
		if err := validateChangeFields(ch); err != nil {
			return 0, err
		}
		cur, ok := overlay[ch.ID]
		if !ok {
			if it, exists := s.items[ch.ID]; exists {
				itCopy := it
				cur = &itCopy
			}
		}
		if ch.Delete {
			overlay[ch.ID] = nil
			continue
		}
		next, err := applyUpsert(cur, ch)
		if err != nil {
			return 0, err
		}
		overlay[ch.ID] = &next
	}

	// Pass 2: apply the overlay, collecting items whose fields actually
	// changed (create/delete count as changes).
	changed := make([]string, 0, len(overlay))
	for id, next := range overlay {
		var old *Item
		if it, exists := s.items[id]; exists {
			itCopy := it
			old = &itCopy
		}
		if itemsEqual(old, next) {
			continue
		}
		if next == nil {
			delete(s.items, id)
		} else {
			s.items[id] = *next
		}
		changed = append(changed, id)
	}

	// Affected displays are exactly those referencing a changed item.
	seen := make(map[*displayState]struct{})
	affected := make([]*displayState, 0)
	for _, id := range changed {
		for displayID := range s.refs[id] {
			ds := s.getOrCreateDisplay(displayID)
			if _, dup := seen[ds]; !dup {
				seen[ds] = struct{}{}
				affected = append(affected, ds)
			}
		}
	}

	return s.bumpVersionLocked(affected), nil
}

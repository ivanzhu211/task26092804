package menuboard

import (
	"sync"
	"sync/atomic"
)

// RenderHook is invoked once per actual render with the display ID and the
// version the render is based on. It may block arbitrarily long.
type RenderHook func(displayID string, version uint64)

// cacheEntry is an immutable rendered board plus the version of the data
// snapshot it was rendered from.
type cacheEntry struct {
	dataVersion uint64
	board       *Board
}

// displayState holds the per-display cache state. dirty is the latest
// version at which this display was affected by a write.
type displayState struct {
	dirty atomic.Uint64
	mu    sync.Mutex // per-display render singleflight
	entry atomic.Pointer[cacheEntry]
}

// Store is the in-memory materialized board cache.
type Store struct {
	hook RenderHook

	writeMu sync.Mutex   // serializes DefineBoard/ApplyBatch
	dataMu  sync.RWMutex // guards items, defs, refs
	global  atomic.Uint64

	items map[string]Item                // itemID -> item
	defs  map[string]*BoardDef           // displayID -> immutable def
	refs  map[string]map[string]struct{} // itemID -> set of displayID

	displays sync.Map // displayID -> *displayState
}

// New creates a Store. hook may be nil.
func New(hook RenderHook) *Store {
	return &Store{
		hook:  hook,
		items: make(map[string]Item),
		defs:  make(map[string]*BoardDef),
		refs:  make(map[string]map[string]struct{}),
	}
}

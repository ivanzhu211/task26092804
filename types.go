// Package menuboard implements an in-memory materialized-view cache for
// digital menu board displays.
package menuboard

import "errors"

// Field is an optionally-provided value in an upsert change.
// A zero Field (Set == false) leaves the current value untouched.
type Field[T any] struct {
	Set   bool
	Value T
}

// Provide returns a Field that is explicitly set to v.
func Provide[T any](v T) Field[T] { return Field[T]{Set: true, Value: v} }

// Item is a menu item. Prices are integer cents.
// MealPrice and LargePrice are optional (nil = not offered).
type Item struct {
	ID         string
	Name       string
	BasePrice  int64
	MealPrice  *int64
	LargePrice *int64
	SoldOut    bool
}

// ChangeKind selects upsert or delete for an ItemChange.
type ChangeKind int

const (
	// Upsert creates or partially updates an item.
	Upsert ChangeKind = iota
	// Delete removes an item.
	Delete
)

// ItemChange is a single change inside a batch.
// For Upsert, only fields with Set == true are applied.
// MealPrice/LargePrice use Field[*int64]: Provide(&v) sets a price,
// Provide(nil) clears it, zero Field leaves it unchanged.
type ItemChange struct {
	Kind       ChangeKind
	ID         string
	Name       Field[string]
	BasePrice  Field[int64]
	MealPrice  Field[*int64]
	LargePrice Field[*int64]
	SoldOut    Field[bool]
}

// BoardDef is the board definition of one display: up to 6 ordered
// sections, each an ordered list of up to 40 item IDs.
type BoardDef struct {
	Sections [][]string
}

// Board is the rendered board of one display: the items that exist,
// in definition order, with all their fields.
type Board struct {
	Sections [][]Item
}

// RenderHook is invoked once per actual render with the display ID and
// the version the render is based on. It may block arbitrarily long.
type RenderHook func(displayID string, version uint64)

var (
	// ErrBoardNotDefined is returned by GetBoard for a display without a definition.
	ErrBoardNotDefined = errors.New("menuboard: display has no board definition")
	// ErrEmptyBatch is returned by ApplyBatch for an empty batch.
	ErrEmptyBatch = errors.New("menuboard: empty batch")
	// ErrTooManyChanges is returned by ApplyBatch for more than 200 changes.
	ErrTooManyChanges = errors.New("menuboard: batch exceeds 200 changes")
	// ErrInvalidChange is returned by ApplyBatch when a change is invalid;
	// the whole batch is rejected and no version is consumed.
	ErrInvalidChange = errors.New("menuboard: invalid change")
	// ErrInvalidDef is returned by DefineBoard for a definition exceeding the limits.
	ErrInvalidDef = errors.New("menuboard: invalid board definition")
)

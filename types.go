package menuboard

import "errors"

const (
	MaxSections    = 6
	MaxSectionSize = 40
	MaxBatchSize   = 200
)

var (
	ErrBoardNotDefined = errors.New("menuboard: board not defined")
	ErrEmptyDisplayID  = errors.New("menuboard: empty display id")
	ErrTooManySections = errors.New("menuboard: too many sections")
	ErrSectionTooLarge = errors.New("menuboard: section has too many items")
	ErrTooManyChanges  = errors.New("menuboard: too many changes in batch")
	ErrEmptyItemID     = errors.New("menuboard: empty item id")
	ErrMissingName     = errors.New("menuboard: new item requires a name")
	ErrMissingRegular  = errors.New("menuboard: new item requires a regular price")
	ErrDeleteWithSet   = errors.New("menuboard: delete change must not set fields")
)

// Item is a menu item. Prices are integer cents. Combo and Large are
// optional; nil means the price is not offered.
type Item struct {
	ID      string
	Name    string
	Regular int64
	Combo   *int64
	Large   *int64
	SoldOut bool
}

// BoardDef is the definition of one display's board: an ordered list of
// sections, each an ordered list of item IDs.
type BoardDef struct {
	Sections []SectionDef
}

type SectionDef struct {
	ItemIDs []string
}

// Change is one entry of a batch. Delete removes the item; otherwise the
// change is an upsert that only touches the fields that are set.
// ClearCombo/ClearLarge reset the optional prices to nil.
type Change struct {
	ID         string
	Delete     bool
	Name       *string
	Regular    *int64
	Combo      *int64
	Large      *int64
	SoldOut    *bool
	ClearCombo bool
	ClearLarge bool
}

// Board is the rendered result for one display.
type Board struct {
	Sections []BoardSection
}

type BoardSection struct {
	Items []Item
}

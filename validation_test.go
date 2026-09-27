package menuboard

import (
	"errors"
	"testing"
)

func TestValidation(t *testing.T) {
	c := New(nil)

	// GetBoard on an undefined display.
	if _, _, err := c.GetBoard("nope"); !errors.Is(err, ErrBoardNotDefined) {
		t.Fatalf("GetBoard(undefined) err = %v, want ErrBoardNotDefined", err)
	}

	// Empty batch.
	if _, err := c.ApplyBatch(nil); !errors.Is(err, ErrEmptyBatch) {
		t.Fatalf("ApplyBatch(nil) err = %v, want ErrEmptyBatch", err)
	}

	// Oversized batch.
	big := make([]ItemChange, 201)
	for i := range big {
		big[i] = ItemChange{Kind: Delete, ID: "x"}
	}
	if _, err := c.ApplyBatch(big); !errors.Is(err, ErrTooManyChanges) {
		t.Fatalf("ApplyBatch(201) err = %v, want ErrTooManyChanges", err)
	}

	// New item without name / without base price.
	if _, err := c.ApplyBatch([]ItemChange{
		{Kind: Upsert, ID: "a", BasePrice: Provide(int64(1))},
	}); !errors.Is(err, ErrInvalidChange) {
		t.Fatalf("upsert without name err = %v, want ErrInvalidChange", err)
	}
	if _, err := c.ApplyBatch([]ItemChange{
		{Kind: Upsert, ID: "a", Name: Provide("a")},
	}); !errors.Is(err, ErrInvalidChange) {
		t.Fatalf("upsert without price err = %v, want ErrInvalidChange", err)
	}

	// Unknown change kind.
	if _, err := c.ApplyBatch([]ItemChange{
		{Kind: ChangeKind(99), ID: "a"},
	}); !errors.Is(err, ErrInvalidChange) {
		t.Fatalf("unknown kind err = %v, want ErrInvalidChange", err)
	}

	// One invalid change rejects the whole batch atomically.
	if _, err := c.ApplyBatch([]ItemChange{
		{Kind: Upsert, ID: "ok", Name: Provide("ok"), BasePrice: Provide(int64(5))},
		{Kind: Upsert, ID: "bad", Name: Provide("bad")}, // missing price
	}); !errors.Is(err, ErrInvalidChange) {
		t.Fatalf("mixed batch err = %v, want ErrInvalidChange", err)
	}

	// Definition limits.
	seven := make([][]string, 7)
	if _, err := c.DefineBoard("d1", BoardDef{Sections: seven}); !errors.Is(err, ErrInvalidDef) {
		t.Fatalf("7 sections err = %v, want ErrInvalidDef", err)
	}
	fortyOne := make([]string, 41)
	if _, err := c.DefineBoard("d1", BoardDef{Sections: [][]string{fortyOne}}); !errors.Is(err, ErrInvalidDef) {
		t.Fatalf("41 items err = %v, want ErrInvalidDef", err)
	}

	// None of the rejected operations consumed a version: the next valid
	// write must get version 1.
	v, err := c.DefineBoard("d1", BoardDef{Sections: [][]string{{"ok"}}})
	if err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Fatalf("version after rejected ops = %d, want 1", v)
	}

	// The rejected batch applied nothing: "ok" must not exist.
	if _, err := c.ApplyBatch([]ItemChange{
		{Kind: Upsert, ID: "ok", Name: Provide("ok"), BasePrice: Provide(int64(5))},
	}); err != nil {
		t.Fatal(err)
	}
	b, _, err := c.GetBoard("d1")
	if err != nil {
		t.Fatal(err)
	}
	if it := findItem(t, b, "ok"); it.BasePrice != 5 {
		t.Fatalf("ok price = %d, want 5", it.BasePrice)
	}

	// Repeated changes to the same item stack in order within one batch.
	if _, err := c.ApplyBatch([]ItemChange{
		{Kind: Upsert, ID: "ok", BasePrice: Provide(int64(50))},
		{Kind: Upsert, ID: "ok", SoldOut: Provide(true)},
		{Kind: Upsert, ID: "ok", BasePrice: Provide(int64(55))},
	}); err != nil {
		t.Fatal(err)
	}
	b, _, err = c.GetBoard("d1")
	if err != nil {
		t.Fatal(err)
	}
	it := findItem(t, b, "ok")
	if it.BasePrice != 55 || !it.SoldOut || it.Name != "ok" {
		t.Fatalf("stacked upserts = %+v, want price 55, sold out, name ok", it)
	}

	// Delete then re-create within one batch.
	if _, err := c.ApplyBatch([]ItemChange{
		{Kind: Delete, ID: "ok"},
		{Kind: Upsert, ID: "ok", Name: Provide("ok2"), BasePrice: Provide(int64(7))},
	}); err != nil {
		t.Fatal(err)
	}
	b, _, err = c.GetBoard("d1")
	if err != nil {
		t.Fatal(err)
	}
	it = findItem(t, b, "ok")
	if it.BasePrice != 7 || it.SoldOut || it.Name != "ok2" {
		t.Fatalf("delete+recreate = %+v, want fresh item", it)
	}

	// Delete removes the item from the rendered board.
	if _, err := c.ApplyBatch([]ItemChange{{Kind: Delete, ID: "ok"}}); err != nil {
		t.Fatal(err)
	}
	b, _, err = c.GetBoard("d1")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Sections) != 1 || len(b.Sections[0]) != 0 {
		t.Fatalf("board after delete = %+v, want empty section", b)
	}
}

package menuboard

import (
	"errors"
	"testing"
)

func TestValidationAndAtomicRejection(t *testing.T) {
	st := New(nil)

	if _, _, err := st.GetBoard("nope"); !errors.Is(err, ErrBoardNotDefined) {
		t.Fatalf("GetBoard undefined: %v", err)
	}
	if _, err := st.DefineBoard("", BoardDef{}); !errors.Is(err, ErrEmptyDisplayID) {
		t.Fatalf("empty display id: %v", err)
	}
	big := BoardDef{Sections: make([]SectionDef, MaxSections+1)}
	if _, err := st.DefineBoard("d", big); !errors.Is(err, ErrTooManySections) {
		t.Fatalf("too many sections: %v", err)
	}
	bigSec := BoardDef{Sections: []SectionDef{{ItemIDs: make([]string, MaxSectionSize+1)}}}
	if _, err := st.DefineBoard("d", bigSec); !errors.Is(err, ErrSectionTooLarge) {
		t.Fatalf("section too large: %v", err)
	}
	if _, err := st.ApplyBatch(make([]Change, MaxBatchSize+1)); !errors.Is(err, ErrTooManyChanges) {
		t.Fatalf("too many changes: %v", err)
	}
	if _, err := st.ApplyBatch([]Change{{ID: "", Name: strp("x"), Regular: i64p(1)}}); !errors.Is(err, ErrEmptyItemID) {
		t.Fatalf("empty item id: %v", err)
	}
	if _, err := st.ApplyBatch([]Change{{ID: "a", Regular: i64p(1)}}); !errors.Is(err, ErrMissingName) {
		t.Fatalf("missing name: %v", err)
	}
	if _, err := st.ApplyBatch([]Change{{ID: "a", Name: strp("x")}}); !errors.Is(err, ErrMissingRegular) {
		t.Fatalf("missing regular: %v", err)
	}
	if _, err := st.ApplyBatch([]Change{{ID: "a", Delete: true, SoldOut: boolp(true)}}); !errors.Is(err, ErrDeleteWithSet) {
		t.Fatalf("delete with fields: %v", err)
	}

	// A batch with one invalid change is rejected as a whole and consumes
	// no version.
	v1, err := st.ApplyBatch([]Change{{ID: "a", Name: strp("A"), Regular: i64p(10)}})
	if err != nil {
		t.Fatalf("valid batch: %v", err)
	}
	if _, err := st.ApplyBatch([]Change{
		{ID: "a", Regular: i64p(20)},
		{ID: "b"}, // invalid: new item without name/regular
	}); !errors.Is(err, ErrMissingName) {
		t.Fatalf("invalid batch: %v", err)
	}

	// The rejected change to "a" must not be visible.
	vDef, err := st.DefineBoard("d", BoardDef{Sections: []SectionDef{{ItemIDs: []string{"a", "b"}}}})
	if err != nil {
		t.Fatalf("define: %v", err)
	}
	b, _, err := st.GetBoard("d")
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	if it := boardItem(b, "a"); it == nil || it.Regular != 10 {
		t.Fatalf("a = %+v, want regular 10 (rejected batch leaked)", it)
	}
	if boardItem(b, "b") != nil {
		t.Fatalf("b must not exist")
	}

	v2, err := st.ApplyBatch([]Change{{ID: "a", Regular: i64p(30)}})
	if err != nil {
		t.Fatalf("follow-up batch: %v", err)
	}
	if vDef != v1+1 || v2 != vDef+1 {
		t.Fatalf("rejected batch consumed a version: v1=%d vDef=%d v2=%d", v1, vDef, v2)
	}
}

func TestUpsertDeleteSemantics(t *testing.T) {
	st := New(nil)
	if _, err := st.ApplyBatch([]Change{
		{ID: "a", Name: strp("A"), Regular: i64p(10), Combo: i64p(15), Large: i64p(12)},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := st.DefineBoard("d", BoardDef{Sections: []SectionDef{{ItemIDs: []string{"a"}}}}); err != nil {
		t.Fatalf("define: %v", err)
	}

	// Same item twice in one batch: changes stack in order.
	if _, err := st.ApplyBatch([]Change{
		{ID: "a", Regular: i64p(20)},
		{ID: "a", Regular: i64p(25), ClearCombo: true},
	}); err != nil {
		t.Fatalf("stacked batch: %v", err)
	}
	b, _, err := st.GetBoard("d")
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	it := boardItem(b, "a")
	if it == nil || it.Regular != 25 || it.Name != "A" {
		t.Fatalf("a = %+v, want regular 25 name A", it)
	}
	if it.Combo != nil {
		t.Fatalf("combo = %v, want cleared", *it.Combo)
	}
	if it.Large == nil || *it.Large != 12 {
		t.Fatalf("large = %+v, want 12 (untouched)", it.Large)
	}

	// Delete removes the item from the board; re-create needs name+regular.
	if _, err := st.ApplyBatch([]Change{{ID: "a", Delete: true}}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	b, _, err = st.GetBoard("d")
	if err != nil {
		t.Fatalf("GetBoard after delete: %v", err)
	}
	if boardItem(b, "a") != nil {
		t.Fatalf("deleted item still on board")
	}
	if _, err := st.ApplyBatch([]Change{{ID: "a", Regular: i64p(1)}}); !errors.Is(err, ErrMissingName) {
		t.Fatalf("recreate without name: %v", err)
	}
}

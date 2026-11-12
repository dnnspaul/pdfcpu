/*
Copyright 2026 The pdfcpu Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pdfcpu

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestMergeXRefTablesReturnsCancellation(t *testing.T) {
	c, cancel := context.WithCancel(t.Context())
	cancel()

	if err := MergeXRefTables(c, "", nil, nil, false, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func testMergeContext(t *testing.T) *model.Context {
	t.Helper()
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	ctx, err := ReadFile(t.Context(), filepath.Join("..", "samples", "create", "primitives", "textAndAlignment.pdf"), conf)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestMergeXRefTablesPageTreeErrorIncludesMergeContext(t *testing.T) {
	src := testMergeContext(t)
	dest := testMergeContext(t)
	dest.PageCount++

	err := MergeXRefTables(t.Context(), "source.pdf", src, dest, false, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "merge page tree: append source pages") {
		t.Fatalf("expected merge page tree context, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "corrupt page node") {
		t.Fatalf("expected lower page tree context, got %q", err.Error())
	}
}

func TestMergeXRefTablesZipPageTreeErrorIncludesMergeContext(t *testing.T) {
	src := testMergeContext(t)
	dest := testMergeContext(t)
	dest.RootDict = nil
	dest.Root = nil

	err := MergeXRefTables(t.Context(), "source.pdf", src, dest, true, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "merge page tree: zip source pages") {
		t.Fatalf("expected zip merge page tree context, got %q", err.Error())
	}
}

func TestMergeXRefTablesNilContextErrors(t *testing.T) {
	err := MergeXRefTables(t.Context(), "source.pdf", nil, testMergeContext(t), false, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "merge: missing context") {
		t.Fatalf("expected missing context error, got %q", err.Error())
	}

	src := testMergeContext(t)
	src.Root = nil
	err = MergeXRefTables(t.Context(), "source.pdf", src, testMergeContext(t), false, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "missing source root") {
		t.Fatalf("expected missing source root error, got %q", err.Error())
	}
}

func TestEnsureOutlinesNilContextReturnsError(t *testing.T) {
	err := EnsureOutlines(t.Context(), nil, "source.pdf", false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "ensure outlines: missing context") {
		t.Fatalf("expected missing context error, got %q", err.Error())
	}
}

// TestRenameOrphanWidgetFieldIgnoresNonNameSubtype treats a dict whose Subtype is not a name as not a widget.
// The scan covers every source dict, eg. a Mac file information dict with an integer Subtype.
func TestRenameOrphanWidgetFieldIgnoresNonNameSubtype(t *testing.T) {
	ctx := testMergeContext(t)
	for _, subtype := range []types.Object{types.Integer(1413830740), types.StringLiteral("Widget")} {
		d := types.Dict{"Subtype": subtype, "T": types.StringLiteral("f")}
		if err := renameOrphanWidgetField(ctx, d, "0"); err != nil {
			t.Fatalf("Subtype %v: %v", subtype, err)
		}
		if d["T"] != types.StringLiteral("f") {
			t.Fatalf("Subtype %v: T renamed to %v", subtype, d["T"])
		}
	}

	d := types.Dict{"Subtype": types.Name("Widget"), "T": types.StringLiteral("f")}
	if err := renameOrphanWidgetField(ctx, d, "0"); err != nil {
		t.Fatal(err)
	}
	if d["T"] != types.StringLiteral("0.f") {
		t.Fatalf("widget T: got %v, want 0.f", d["T"])
	}
}

// TestMergeXRefTablesSourceWithIntegerSubtype merges a source holding a dict with an integer Subtype.
func TestMergeXRefTablesSourceWithIntegerSubtype(t *testing.T) {
	src, dest := testMergeContext(t), testMergeContext(t)
	// ReadFile leaves PageCount to validation. textAndAlignment.pdf has one page.
	src.PageCount, dest.PageCount = 1, 1
	if _, err := src.IndRefForNewObject(types.Dict{"Subtype": types.Integer(1413830740)}); err != nil {
		t.Fatal(err)
	}
	if err := MergeXRefTables(t.Context(), "source.pdf", src, dest, false, false); err != nil {
		t.Fatal(err)
	}
	if dest.PageCount != 2 {
		t.Fatalf("PageCount: got %d, want 2", dest.PageCount)
	}
}

// TestPatchObjectDanglingRefGetsFreshNumber checks that a reference to an object the source does
// not contain is renumbered to the fresh number collected for it, so it names no renumbered object.
func TestPatchObjectDanglingRefGetsFreshNumber(t *testing.T) {
	// 1 and 2 are source objects. 9 is dangling and gets the fresh number 7.
	refs := map[int]int{1: 5, 2: 6, 9: 7}
	d := types.Dict{
		"Valid":    *types.NewIndirectRef(1, 0),
		"Dangling": *types.NewIndirectRef(9, 0),
		"Arr":      types.Array{*types.NewIndirectRef(9, 0), *types.NewIndirectRef(2, 0)},
	}
	patchObject(d, refs)

	if got := d["Valid"]; got != *types.NewIndirectRef(5, 0) {
		t.Fatalf("Valid: got %v, want 5 0 R", got)
	}
	if got := d["Dangling"]; got != *types.NewIndirectRef(7, 0) {
		t.Fatalf("Dangling: got %v, want 7 0 R", got)
	}
	arr := d["Arr"].(types.Array)
	if arr[0] != *types.NewIndirectRef(7, 0) {
		t.Fatalf("Arr[0]: got %v, want 7 0 R", arr[0])
	}
	if arr[1] != *types.NewIndirectRef(6, 0) {
		t.Fatalf("Arr[1]: got %v, want 6 0 R", arr[1])
	}
}

// TestDanglingRefsAreCollected checks that danglingRefs collects the numbers of the references that
// name no source object, and leaves the object unchanged.
func TestDanglingRefsAreCollected(t *testing.T) {
	lookup := map[int]int{1: 5, 2: 6}
	d := types.Dict{
		"Valid":  *types.NewIndirectRef(1, 0),
		"Arr":    types.Array{*types.NewIndirectRef(9, 0), *types.NewIndirectRef(0, 0)},
		"Nested": types.Dict{"Kid": *types.NewIndirectRef(12, 3)},
		"Gen":    *types.NewIndirectRef(9, 2),
	}
	dangling := map[int]int{}
	danglingRefs(d, lookup, dangling)

	// 0 0 R is not a dangling reference. Number 9 is seen with generations 0 and 2 and keeps the lowest.
	if want := (map[int]int{9: 0, 12: 3}); !maps.Equal(dangling, want) {
		t.Fatalf("dangling: got %v, want %v", dangling, want)
	}
	if d["Valid"] != *types.NewIndirectRef(1, 0) {
		t.Fatalf("Valid: got %v, want 1 0 R", d["Valid"])
	}
	if arr := d["Arr"].(types.Array); arr[0] != *types.NewIndirectRef(9, 0) {
		t.Fatalf("Arr[0]: got %v, want 9 0 R", arr[0])
	}
	if kid := d["Nested"].(types.Dict)["Kid"]; kid != *types.NewIndirectRef(12, 3) {
		t.Fatalf("Nested Kid: got %v, want 12 3 R", kid)
	}
}

// TestPatchObjectKeepsScalarsAndPatchesBareReference checks that an object without references is
// returned unchanged, and that an object which is a bare reference is renumbered.
func TestPatchObjectKeepsScalarsAndPatchesBareReference(t *testing.T) {
	lookup := map[int]int{1: 5}
	if got := patchObject(types.Integer(7), lookup); got != types.Integer(7) {
		t.Fatalf("Integer: got %v, want 7", got)
	}
	if got := patchObject(*types.NewIndirectRef(1, 0), lookup); got != *types.NewIndirectRef(5, 0) {
		t.Fatalf("bare reference: got %v, want 5 0 R", got)
	}
}

// TestPatchSourceXRefEntriesPatchesBareReference checks that an entry whose object is a bare
// reference takes the renumbered reference, and that a scalar entry keeps its value.
func TestPatchSourceXRefEntriesPatchesBareReference(t *testing.T) {
	ctx := testMergeContext(t)
	ctx.Table[7] = model.NewXRefTableEntryGen0(*types.NewIndirectRef(1, 0))
	ctx.Table[8] = model.NewXRefTableEntryGen0(types.Integer(7))
	lookup := map[int]int{1: 50, 7: 51, 8: 52}

	if err := patchSourceXRefEntries(t.Context(), ctx, types.IntSet{7: true, 8: true}, lookup); err != nil {
		t.Fatal(err)
	}
	if got := ctx.Table[7].Object; got != *types.NewIndirectRef(50, 0) {
		t.Fatalf("entry 7: got %v, want 50 0 R", got)
	}
	if got := ctx.Table[8].Object; got != types.Integer(7) {
		t.Fatalf("entry 8: got %v, want 7", got)
	}
}

// TestMergeXRefTablesDanglingInfoFreesNothing checks that a source Info number the source does not
// contain is dropped, so merging does not free the destination object with that number.
func TestMergeXRefTablesDanglingInfoFreesNothing(t *testing.T) {
	src, dest := testMergeContext(t), testMergeContext(t)
	// ReadFile leaves PageCount to validation. textAndAlignment.pdf has one page.
	src.PageCount, dest.PageCount = 1, 1
	const stale = 500
	if _, ok := src.Table[stale]; ok {
		t.Fatalf("test premise: source has object %d", stale)
	}
	dest.Table[stale] = model.NewXRefTableEntryGen0(types.Integer(1))
	src.Info = types.NewIndirectRef(stale, 0)

	if err := MergeXRefTables(t.Context(), "source.pdf", src, dest, false, false); err != nil {
		t.Fatal(err)
	}
	if e := dest.Table[stale]; e == nil || e.Free {
		t.Fatalf("destination object %d was freed by a stale source Info", stale)
	}
}

// TestCollectDanglingRefsFromNameTrees checks that a dangling reference held by a cached name tree
// is collected with the dangling references of the objects, so that it gets a fresh number too.
func TestCollectDanglingRefsFromNameTrees(t *testing.T) {
	ctx := testMergeContext(t)
	n := &model.Node{}
	n.AppendToNames("dest", *types.NewIndirectRef(9999, 0))
	if ctx.Names == nil {
		ctx.Names = map[string]*model.Node{}
	}
	ctx.Names["Dests"] = n

	objNrs := objNrsIntSet(ctx)
	dangling, err := collectDanglingRefs(t.Context(), ctx, objNrs, lookupTable(objNrs, *ctx.Size))
	if err != nil {
		t.Fatal(err)
	}
	if want := (map[int]int{9999: 0}); !maps.Equal(dangling, want) {
		t.Fatalf("dangling: got %v, want %v", dangling, want)
	}
}

// TestLookupTableNumbersInKeyOrder checks that source objects get new numbers in ascending
// order of their old numbers, so that merged output numbering is reproducible.
func TestLookupTableNumbersInKeyOrder(t *testing.T) {
	want := map[int]int{5: 100, 10: 101, 20: 102, 30: 103}
	for range 50 {
		got := lookupTable(types.IntSet{30: true, 10: true, 20: true, 5: true}, 100)
		if !maps.Equal(got, want) {
			t.Fatalf("lookup: got %v, want %v", got, want)
		}
	}
}

// TestMergeXRefTablesRegistersDanglingNumbersAsNullEntries checks that a dangling reference of the source
// gets a fresh number that the destination registers as an in-use entry without an object, with the
// generation of the reference. The writer then writes it as a null object with an xref entry.
func TestMergeXRefTablesRegistersDanglingNumbersAsNullEntries(t *testing.T) {
	src, dest := testMergeContext(t), testMergeContext(t)
	// ReadFile leaves PageCount to validation. textAndAlignment.pdf has one page.
	src.PageCount, dest.PageCount = 1, 1
	const missing = 99999
	if _, ok := src.Table[missing]; ok {
		t.Fatalf("test premise: source has object %d", missing)
	}
	catalog := src.Table[int(src.Root.ObjectNumber)].Object.(types.Dict)
	catalog["Probe"] = *types.NewIndirectRef(missing, 3)

	if err := MergeXRefTables(t.Context(), "source.pdf", src, dest, false, false); err != nil {
		t.Fatal(err)
	}

	var fresh []int
	for k, e := range dest.Table {
		if e != nil && !e.Free && e.Object == nil {
			fresh = append(fresh, k)
		}
	}
	if len(fresh) != 1 {
		t.Fatalf("in-use entries without an object: got %v, want one", fresh)
	}
	e := dest.Table[fresh[0]]
	if *e.Generation != 3 {
		t.Fatalf("fresh number %d: generation %d, want 3", fresh[0], *e.Generation)
	}
	if fresh[0] >= *dest.Size {
		t.Fatalf("fresh number %d not below destination /Size %d", fresh[0], *dest.Size)
	}
}

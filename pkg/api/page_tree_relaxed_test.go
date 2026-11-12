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
package api

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// pageTreeFreeObject marks object objNr of data, a PDF built by failureTestPDF, as free in its
// cross-reference table.
func pageTreeFreeObject(t *testing.T, data []byte, objNr int) []byte {
	t.Helper()
	xref := bytes.LastIndex(data, []byte("\nxref\n"))
	if xref < 0 {
		t.Fatal("no xref section")
	}
	start := xref + len("\nxref\n")
	start += bytes.IndexByte(data[start:], '\n') + 1 + objNr*20
	out := bytes.Clone(data)
	copy(out[start:start+20], "0000000000 00001 f \n")
	return out
}

// pageTreeFreeList marks objNrs, objects of a PDF built by failureTestPDF, as free and chains them
// in the order given from object 0, the free list head, as the cross-reference table does.
func pageTreeFreeList(t *testing.T, data []byte, objNrs ...int) []byte {
	t.Helper()
	xref := bytes.LastIndex(data, []byte("\nxref\n"))
	if xref < 0 {
		t.Fatal("no xref section")
	}
	first := xref + len("\nxref\n")
	first += bytes.IndexByte(data[first:], '\n') + 1
	out := bytes.Clone(data)
	entry := func(objNr int) []byte { return out[first+objNr*20 : first+objNr*20+20] }
	for i, objNr := range objNrs {
		next := 0
		if i+1 < len(objNrs) {
			next = objNrs[i+1]
		}
		copy(entry(objNr), fmt.Sprintf("%010d 00001 f \n", next))
	}
	copy(entry(0), fmt.Sprintf("%010d 65535 f \n", objNrs[0]))
	return out
}

// TestReadPageBoundariesRelaxedPageTree pins that in relaxed mode ReadPageBoundaries tolerates the
// page tree defects relaxed validation repairs and returns the page count and boxes PageBoundaries
// returns after ReadAndValidate, and that strict mode still rejects them with ErrInvalidPDF.
func TestReadPageBoundariesRelaxedPageTree(t *testing.T) {
	const page = "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 400] >>"
	for _, tc := range []struct {
		name  string
		data  []byte
		pages int
	}{
		{"null kids", failureTestPDF("<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [null 3 0 R null] /Count 1 >>", page), 1},
		{"kid object 0", failureTestPDF("<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [0 0 R 3 0 R] /Count 1 >>", page), 1},
		{"missing kid first", failureTestPDF("<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [9 0 R 3 0 R] /Count 2 >>", page), 2},
		{"missing kid inherits", failureTestPDF("<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [9 0 R 3 0 R] /Count 2 /MediaBox [0 0 100 200] /CropBox [10 10 90 190] /Rotate 90 >>", page), 2},
		{"missing kid between pages", failureTestPDF("<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R 9 0 R 4 0 R] /Count 3 >>", page,
			"<< /Type /Page /Parent 2 0 R /CropBox [5 5 50 50] >>"), 3},
		{"null object kid", failureTestPDF("<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [4 0 R 3 0 R] /Count 2 >>", page, "null"), 2},
		{"empty dict kid", failureTestPDF("<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [4 0 R 3 0 R] /Count 2 >>", page, "<< >>"), 2},
		{"repeated page", failureTestPDF("<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R 3 0 R 3 0 R 3 0 R 3 0 R] /Count 5 >>", page), 5},
		{"kids beyond size", failureTestPDF("<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>"), 2},
		{"kids beyond size reversed", failureTestPDF("<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [4 0 R 3 0 R] /Count 2 >>"), 2},
		{"free kid at size", pageTreeFreeObject(t, failureTestPDF("<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>", page), 3), 2},
		{"blank after sibling page box", failureTestPDF("<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R 9 0 R] /Count 2 /MediaBox [0 0 300 400] >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>"), 2},
		{"blank after empty subtree", failureTestPDF("<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [4 0 R 9 0 R 3 0 R] /Count 1 >>", page,
			"<< /Type /Pages /Parent 2 0 R /MediaBox [0 0 11 22] /Kids [] /Count 0 >>"), 2},
		{"blank under declared subtree", failureTestPDF("<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [4 0 R] /Count 1 >>", page,
			"<< /Type /Pages /Parent 2 0 R /MediaBox [0 0 100 200] /Kids [9 0 R 3 0 R] /Count 2 >>"), 2},
		{"negative kid", failureTestPDF("<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R -5 0 R] /Count 2 >>", page), 2},
		{"kid beyond object number limit", failureTestPDF("<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [8388608 0 R 3 0 R] /Count 2 >>", page), 2},
		{"unparsable MediaBox coordinates", failureTestPDF("<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 1e30 1e30] >>"), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pbs, err := ReadPageBoundaries(context.Background(), bytes.NewReader(tc.data), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(tc.data), model.NewTolerantConfiguration())
			if err != nil {
				t.Fatal(err)
			}
			want, err := ctx.PageBoundaries(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(pbs) != tc.pages || ctx.PageCount != tc.pages {
				t.Fatalf("got %d pages, validation %d, want %d", len(pbs), ctx.PageCount, tc.pages)
			}
			if !reflect.DeepEqual(pbs, want) {
				t.Fatalf("got %+v, validation %+v", pbs, want)
			}
			if pbs, err = ReadPageBoundaries(context.Background(), bytes.NewReader(tc.data), []string{"1"}, nil); err != nil || !reflect.DeepEqual(pbs[0], want[0]) {
				t.Fatalf("page 1: got %+v, %v, want %+v", pbs, err, want[0])
			}
			if tc.name == "repeated page" {
				// Strict mode accepts at most one page per cross-reference table entry; 5 pages fit in 4 entries.
				return
			}
			strict := model.NewTolerantConfiguration()
			strict.ValidationMode = model.ValidationStrict
			_, err = ReadPageBoundaries(context.Background(), bytes.NewReader(tc.data), nil, strict)
			var invalidErr *InvalidPDFError
			if !errors.As(err, &invalidErr) {
				t.Fatalf("strict: got %v (%T), want *InvalidPDFError", err, err)
			}
		})
	}
}

// TestReadPageBoundariesRelaxedFreeKid pins that in relaxed mode a Kids reference to a free object
// counts as a blank page, as for a missing object, that relaxed validation agrees with it, and that
// strict mode rejects it.
func TestReadPageBoundariesRelaxedFreeKid(t *testing.T) {
	data := pageTreeFreeObject(t, failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [4 0 R 3 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 400] >>",
		"<< /Type /Page /Parent 2 0 R >>",
	), 4)
	pbs, err := ReadPageBoundaries(context.Background(), bytes.NewReader(data), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pbs) != 2 || !reflect.DeepEqual(pbs[0], model.PageBoundaries{Media: &model.Box{Rect: types.RectForFormat("A4")}}) {
		t.Fatalf("got %+v, want an A4 blank page and a second page", pbs)
	}
	ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(data), model.NewTolerantConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	want, err := ctx.PageBoundaries(context.Background(), nil)
	if err != nil || !reflect.DeepEqual(pbs, want) {
		t.Fatalf("got %+v, validation %+v, %v", pbs, want, err)
	}
	strict := model.NewTolerantConfiguration()
	strict.ValidationMode = model.ValidationStrict
	_, err = ReadPageBoundaries(context.Background(), bytes.NewReader(data), nil, strict)
	var invalidErr *InvalidPDFError
	if !errors.As(err, &invalidErr) {
		t.Fatalf("strict: got %v (%T), want *InvalidPDFError", err, err)
	}
}

// TestReadPageBoundariesRelaxedPageLimit pins that repeated and blank pages count toward
// MaxPageCount in relaxed mode, so a page bomb of repeated or missing kids stops with a
// ResourceLimitError in ReadPageBoundaries and in validation.
func TestReadPageBoundariesRelaxedPageLimit(t *testing.T) {
	const limit = 100
	pbs, err := ReadPageBoundaries(context.Background(), bytes.NewReader(pageLimitRepeatedPDF(limit)), nil, pageLimitConf(model.ValidationRelaxed, limit))
	if err != nil || len(pbs) != limit {
		t.Fatalf("at the limit: got %v, %d pages", err, len(pbs))
	}
	_, err = ReadPageBoundaries(context.Background(), bytes.NewReader(pageLimitRepeatedPDF(limit+1)), nil, pageLimitConf(model.ValidationRelaxed, limit))
	checkPageLimitError(t, err, limit)

	kids := ""
	for i := range limit + 1 {
		kids += fmt.Sprintf("%d 0 R ", 100+i)
	}
	missing := failureTestPDF("<< /Type /Catalog /Pages 2 0 R >>", fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, limit+1))
	_, err = ReadPageBoundaries(context.Background(), bytes.NewReader(missing), nil, pageLimitConf(model.ValidationRelaxed, limit))
	checkPageLimitError(t, err, limit)
	_, err = ReadAndValidate(context.Background(), bytes.NewReader(missing), pageLimitConf(model.ValidationRelaxed, limit))
	checkPageLimitError(t, err, limit)
}

// pageTreeKidsPDF returns a PDF with the page tree node 2 listing kids, followed by the objects pages.
func pageTreeKidsPDF(kids string, pages ...string) []byte {
	objects := append([]string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [" + kids + "] /Count 2 >>"}, pages...)
	return failureTestPDF(objects...)
}

// TestReadAndValidateRelaxedBlankPageKids pins that relaxed validation repairs page tree kids that
// are beyond the table size or free, including a free list head, as pages without /Contents, and
// that the boundaries agree with ReadPageBoundaries.
func TestReadAndValidateRelaxedBlankPageKids(t *testing.T) {
	const page = "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 400] >>"
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"kids beyond size", pageTreeKidsPDF("3 0 R 4 0 R")},
		{"kids beyond size reversed", pageTreeKidsPDF("4 0 R 3 0 R")},
		{"null kid at size", pageTreeKidsPDF("3 0 R 4 0 R", "null")},
		{"free kid at size", pageTreeFreeObject(t, pageTreeKidsPDF("3 0 R 4 0 R", page), 3)},
		{"free kids in list head order", pageTreeFreeList(t, pageTreeKidsPDF("4 0 R 3 0 R", page, page), 4, 3)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(tc.data), model.NewTolerantConfiguration())
			if err != nil {
				t.Fatal(err)
			}
			if ctx.PageCount != 2 {
				t.Fatalf("got %d pages, want 2", ctx.PageCount)
			}
			// A kid at or beyond Size may get a new object number, so the pages are
			// found through the Kids entries of the page tree node 2.
			node, err := ctx.DereferenceDict(*types.NewIndirectRef(2, 0))
			if err != nil || node == nil {
				t.Fatalf("page tree node: %v", err)
			}
			for _, o := range node.ArrayEntry("Kids") {
				indRef, ok := o.(types.IndirectRef)
				if !ok {
					t.Fatalf("kid %v is not an indirect reference", o)
				}
				d, err := ctx.DereferenceDict(indRef)
				if err != nil || d == nil {
					t.Fatalf("page obj#%d: %v", indRef.ObjectNumber.Value(), err)
				}
				if _, found := d.Find("Contents"); found {
					t.Fatalf("page obj#%d: blank page has a /Contents entry", indRef.ObjectNumber.Value())
				}
			}
			want, err := ctx.PageBoundaries(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			pbs, err := ReadPageBoundaries(context.Background(), bytes.NewReader(tc.data), nil, nil)
			if err != nil || !reflect.DeepEqual(pbs, want) {
				t.Fatalf("ReadPageBoundaries %+v, %v; validation %+v", pbs, err, want)
			}
		})
	}
}

// pageLimitFlateKidsPDF builds a PDF whose page tree node and page are in a Flate compressed object
// stream, and whose Kids array lists object 3 kids times, a page tree of a few hundred bytes.
func pageLimitFlateKidsPDF(kids int) []byte {
	pages := fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.TrimSpace(strings.Repeat("3 0 R ", kids)), kids)
	prolog := fmt.Sprintf("2 0 3 %d ", len(pages)+1)
	content := prolog + pages + " " + pageLimitPage
	var zbuf bytes.Buffer
	zw := zlib.NewWriter(&zbuf)
	if _, err := zw.Write([]byte(content)); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}

	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n%\xFF\xFF\xFF\xFF\n")
	offsets := map[int]int{}
	offsets[1] = b.Len()
	b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	offsets[4] = b.Len()
	fmt.Fprintf(&b, "4 0 obj\n<< /Type /ObjStm /N 2 /First %d /Length %d /Filter /FlateDecode >>\nstream\n", len(prolog), zbuf.Len())
	b.Write(zbuf.Bytes())
	b.WriteString("\nendstream\nendobj\n")

	xref := b.Len()
	var rows []byte
	row := func(typ byte, f2, f3 int) {
		rows = append(rows, typ, byte(f2>>24), byte(f2>>16), byte(f2>>8), byte(f2), byte(f3>>8), byte(f3))
	}
	row(0, 0, 65535)
	row(1, offsets[1], 0)
	row(2, 4, 0)
	row(2, 4, 1)
	row(1, offsets[4], 0)
	row(1, xref, 0)
	fmt.Fprintf(&b, "5 0 obj\n<< /Type /XRef /Size 6 /W [1 4 2] /Root 1 0 R /Length %d >>\nstream\n", len(rows))
	b.Write(rows)
	fmt.Fprintf(&b, "\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", xref)
	return b.Bytes()
}

// TestReadPageBoundariesRelaxedKidsWithoutPageLimit pins that relaxed ReadPageBoundaries with
// MaxPageCount 0 keeps the cross-reference table size as its bound: a Flate compressed Kids array
// of a million references in a small file is rejected, as on the commit before the page limit.
// With MaxPageCount set the walk stops with a LimitPages error instead.
func TestReadPageBoundariesRelaxedKidsWithoutPageLimit(t *testing.T) {
	data := pageLimitFlateKidsPDF(1_000_000)
	if len(data) > 16*1024 {
		t.Fatalf("test file is %d bytes, want a small file", len(data))
	}
	unlimited := model.NewDefaultConfiguration()
	unlimited.ValidationMode = model.ValidationRelaxed
	_, err := ReadPageBoundaries(context.Background(), bytes.NewReader(data), nil, unlimited)
	var limitErr *model.ResourceLimitError
	if err == nil || errors.As(err, &limitErr) {
		t.Fatalf("MaxPageCount 0: got %v, want the table size bound to reject the walk", err)
	}
	if !strings.Contains(err.Error(), "more pages than the") {
		t.Fatalf("MaxPageCount 0: got %v, want the object count cap", err)
	}
	_, err = ReadPageBoundaries(context.Background(), bytes.NewReader(data), nil, pageLimitConf(model.ValidationRelaxed, 100))
	checkPageLimitError(t, err, 100)
}

// relaxedRoundTrip writes ctx and reads the output back in relaxed mode. A repaired page tree whose
// object numbers, Size or free list are wrong fails here.
func relaxedRoundTrip(t *testing.T, ctx *model.Context) *model.Context {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteContext(context.Background(), ctx, &buf); err != nil {
		t.Fatalf("write: %v", err)
	}
	back, err := ReadAndValidate(context.Background(), bytes.NewReader(buf.Bytes()), model.NewTolerantConfiguration())
	if err != nil {
		t.Fatalf("reread: %v", err)
	}
	return back
}

// TestReadAndValidateRelaxedKidsRoundTrip pins that a repaired page tree with kids at the object
// number limit writes a file that reads back with the same pages and boundaries, and with a positive
// Size. A kid beyond ResourceLimits.MaxObjectNumber gets a blank page like a missing kid.
func TestReadAndValidateRelaxedKidsRoundTrip(t *testing.T) {
	const limit = 8_388_607
	for _, tc := range []struct {
		name  string
		kids  string
		pages int
	}{
		{"kid at object number limit", "3 0 R 8388607 0 R", 2},
		{"kid at object number limit first", "8388606 0 R 3 0 R", 2},
		{"kid beyond object number limit", "3 0 R 9223372036854775807 0 R", 2},
		{"kid beyond object number limit first", "2147483647 0 R 3 0 R", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := pageTreeKidsPDF(tc.kids)
			ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(data), model.NewTolerantConfiguration())
			if err != nil {
				t.Fatal(err)
			}
			if ctx.PageCount != tc.pages {
				t.Fatalf("got %d pages, want %d", ctx.PageCount, tc.pages)
			}
			if *ctx.Size <= 0 || *ctx.Size > limit+1 {
				t.Fatalf("Size %d, want a positive size within the object number limit", *ctx.Size)
			}
			want, err := ctx.PageBoundaries(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			pbs, err := ReadPageBoundaries(context.Background(), bytes.NewReader(data), nil, nil)
			if err != nil || !reflect.DeepEqual(pbs, want) {
				t.Fatalf("ReadPageBoundaries %+v, %v; validation %+v", pbs, err, want)
			}
			back := relaxedRoundTrip(t, ctx)
			if back.PageCount != tc.pages {
				t.Fatalf("reread: got %d pages, want %d", back.PageCount, tc.pages)
			}
		})
	}
}

// TestReadAndValidateRelaxedCrossSubtreeKids pins that blank pages repaired in different Pages
// subtrees are distinct and survive a round trip. The root lists nodes 3 and 4 with /Size 5, and
// each node lists one missing object, 5 and 6.
func TestReadAndValidateRelaxedCrossSubtreeKids(t *testing.T) {
	data := failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
		"<< /Type /Pages /Parent 2 0 R /Kids [5 0 R] /Count 1 >>",
		"<< /Type /Pages /Parent 2 0 R /Kids [6 0 R] /Count 1 >>",
	)
	ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(data), model.NewTolerantConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	if ctx.PageCount != 2 {
		t.Fatalf("got %d pages, want 2", ctx.PageCount)
	}
	want, err := ctx.PageBoundaries(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	pbs, err := ReadPageBoundaries(context.Background(), bytes.NewReader(data), nil, nil)
	if err != nil || !reflect.DeepEqual(pbs, want) {
		t.Fatalf("ReadPageBoundaries %+v, %v; validation %+v", pbs, err, want)
	}
	back := relaxedRoundTrip(t, ctx)
	if back.PageCount != 2 {
		t.Fatalf("reread: got %d pages, want 2", back.PageCount)
	}
}

// freeListMembers returns the object numbers on the free list of xRefTable, from the head at object 0,
// sorted. It fails if an entry on the list is not free or the list does not end.
func freeListMembers(t *testing.T, ctx *model.Context) []int {
	t.Helper()
	xRefTable := ctx.XRefTable
	head, found := xRefTable.FindTableEntryLight(0)
	if !found || head.Offset == nil {
		t.Fatal("free list: missing head")
	}
	var members []int
	for cur := int(*head.Offset); cur != 0; {
		if len(members) > len(xRefTable.Table) {
			t.Fatal("free list: cycle")
		}
		entry, found := xRefTable.FindTableEntryLight(cur)
		if !found || !entry.Free || entry.Offset == nil {
			t.Fatalf("free list: obj#%d is on the list but not free", cur)
		}
		members = append(members, cur)
		cur = int(*entry.Offset)
	}
	sort.Ints(members)
	return members
}

// TestReadAndValidateRelaxedFreeListKid pins that a free page tree kid is repaired in place and
// leaves the free list valid. Objects 3, 5 and 7 are free, chained from the head in that order,
// and the kids are 4, 5 and 6. The blank page for 5 must not stay on the list, and no new object
// may take the number of a kid.
func TestReadAndValidateRelaxedFreeListKid(t *testing.T) {
	const page = "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 400] >>"
	data := pageTreeFreeList(t, failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [4 0 R 5 0 R 6 0 R] /Count 3 >>",
		"null", page, page, page, "null",
	), 3, 5, 7)
	ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(data), model.NewTolerantConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	if ctx.PageCount != 3 {
		t.Fatalf("got %d pages, want 3", ctx.PageCount)
	}
	if got := freeListMembers(t, ctx); !reflect.DeepEqual(got, []int{3, 7}) {
		t.Fatalf("free list %v, want [3 7]", got)
	}
	back := relaxedRoundTrip(t, ctx)
	if back.PageCount != 3 {
		t.Fatalf("reread: got %d pages, want 3", back.PageCount)
	}
	// The writer takes free numbers for its own objects, so the reread list can be shorter. It must
	// be valid, which freeListMembers checks, and it must not hold a page.
	for _, n := range freeListMembers(t, back) {
		if n == 4 || n == 5 || n == 6 {
			t.Fatalf("reread free list holds page obj#%d", n)
		}
	}
	for i := 0; i < 4; i++ {
		indRef, err := ctx.IndRefForNewObject(types.Integer(i))
		if err != nil {
			t.Fatal(err)
		}
		if n := indRef.ObjectNumber.Value(); n == 4 || n == 5 || n == 6 || n == 2 || n == 1 {
			t.Fatalf("new object took obj#%d, which is a page or the page tree", n)
		}
	}
}

// cancelAfterContext reports context.Canceled from Err once it has been called more than limit times.
// A zero limit only counts the calls.
type cancelAfterContext struct {
	context.Context
	calls atomic.Int64
	limit int64
}

func (c *cancelAfterContext) Err() error {
	if n := c.calls.Add(1); c.limit > 0 && n > c.limit {
		return context.Canceled
	}
	return nil
}

// reverseFreeKidsPDF returns a PDF whose page tree node 2 lists the n free objects 3 to n+2 in reverse
// order of the free list, which chains them from object 0 in increasing order.
func reverseFreeKidsPDF(t *testing.T, n int) []byte {
	t.Helper()
	kids := make([]string, n)
	objNrs := make([]int, n)
	bodies := make([]string, n)
	for i := range n {
		kids[i] = fmt.Sprintf("%d 0 R", n+2-i)
		objNrs[i] = 3 + i
		bodies[i] = "null"
	}
	objects := append([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n),
	}, bodies...)
	return pageTreeFreeList(t, failureTestPDF(objects...), objNrs...)
}

// TestReadAndValidateRelaxedFreeKidsScale pins that free page tree kids in reverse free list order are
// repaired in linear time and that a cancelled context stops the repair. The file lists 50000 free
// objects, and validation must take under two seconds. Cancellation is checked at half the number of
// context checks of a complete run.
func TestReadAndValidateRelaxedFreeKidsScale(t *testing.T) {
	const n = 50_000
	data := reverseFreeKidsPDF(t, n)

	count := &cancelAfterContext{Context: context.Background()}
	start := time.Now()
	ctx, err := ReadAndValidate(count, bytes.NewReader(data), model.NewTolerantConfiguration())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.PageCount != n {
		t.Fatalf("got %d pages, want %d", ctx.PageCount, n)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("validation took %v, want under 2s", elapsed)
	}
	if got := freeListMembers(t, ctx); len(got) != 0 {
		t.Fatalf("free list %v, want empty: every free kid is a page now", got)
	}

	cancelled := &cancelAfterContext{Context: context.Background(), limit: count.calls.Load() / 2}
	_, err = ReadAndValidate(cancelled, bytes.NewReader(data), model.NewTolerantConfiguration())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled at half the checks: got %v, want context.Canceled", err)
	}
}

// TestReadAndValidateRelaxedMaxInt64KidUnlimited pins that without a MaxObjectNumber a kid at the
// largest int64 is repaired as a blank page, and that Size stays positive and the boundaries agree
// with ReadPageBoundaries. Nothing in the repair adds one to the kid's number.
func TestReadAndValidateRelaxedMaxInt64KidUnlimited(t *testing.T) {
	data := pageTreeKidsPDF("3 0 R 9223372036854775807 0 R")
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(data), conf)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.PageCount != 2 {
		t.Fatalf("got %d pages, want 2", ctx.PageCount)
	}
	if *ctx.Size <= 0 {
		t.Fatalf("Size %d, want a positive size", *ctx.Size)
	}
	want, err := ctx.PageBoundaries(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	pbs, err := ReadPageBoundaries(context.Background(), bytes.NewReader(data), nil, conf)
	if err != nil || !reflect.DeepEqual(pbs, want) {
		t.Fatalf("ReadPageBoundaries %+v, %v; validation %+v", pbs, err, want)
	}
}

// TestReadAndValidateRelaxedBlankPageNotices pins that every blank page of a repaired page tree records one repaired
// notice with its page number, and that null kids and references to object 0 record none.
func TestReadAndValidateRelaxedBlankPageNotices(t *testing.T) {
	data := failureTestPDF("<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 99 0 R -7 0 R 0 0 R null 8388608 0 R 4 0 R] /Count 7 /MediaBox [0 0 200 200] >>",
		"<< /Type /Page /Parent 2 0 R >>",
		"<< >>")
	ctx, _, err := ReadAndValidateWithReport(context.Background(), bytes.NewReader(data), nil)
	if err != nil {
		t.Fatalf("relaxed read: %v", err)
	}
	if ctx.PageCount != 5 {
		t.Fatalf("got %d pages, want 5", ctx.PageCount)
	}

	var blank []model.ValidationNotice
	for _, n := range ctx.ValidationReport().Notices() {
		if errors.Is(n.Cause, model.ErrMissingPageKid) {
			blank = append(blank, n)
		}
	}
	if len(blank) != 4 {
		t.Fatalf("got %d blank page notices, want 4: %v", len(blank), blank)
	}
	for i, n := range blank {
		if n.Disposition != model.NoticeRepaired || n.ObjectNumber != 2 || n.PageNumber != i+2 {
			t.Fatalf("notice %d: disposition %q, object %d, page %d; want repaired, object 2, page %d",
				i, n.Disposition, n.ObjectNumber, n.PageNumber, i+2)
		}
	}
	if want := "page tree: page 2: kid obj#99: missing page tree kid, replaced by a blank page"; blank[0].Message != want {
		t.Fatalf("first message %q, want %q", blank[0].Message, want)
	}
	if !strings.Contains(blank[1].Message, "kid obj#-7:") {
		t.Fatalf("second message %q does not name obj#-7", blank[1].Message)
	}

	pbs, err := ReadPageBoundaries(context.Background(), bytes.NewReader(data), nil, nil)
	if err != nil {
		t.Fatalf("page boundaries: %v", err)
	}
	want, err := ctx.XRefTable.PageBoundaries(context.Background(), nil)
	if err != nil {
		t.Fatalf("validated page boundaries: %v", err)
	}
	if !reflect.DeepEqual(pbs, want) {
		t.Fatalf("page boundaries %v, want %v", pbs, want)
	}
	for i := 1; i < len(pbs); i++ {
		if r := pbs[i].Media.Rect; r.Width() != 200 || r.Height() != 200 {
			t.Fatalf("blank page %d MediaBox %v, want [0 0 200 200]", i+1, r)
		}
	}

	back := relaxedRoundTrip(t, ctx)
	if back.PageCount != 5 {
		t.Fatalf("round trip: got %d pages, want 5", back.PageCount)
	}
	for _, n := range back.ValidationReport().Notices() {
		if errors.Is(n.Cause, model.ErrMissingPageKid) {
			t.Fatalf("round trip: blank page notice %q, want none", n.Message)
		}
	}
}

// TestReadAndValidateRelaxedBlankPageLimits pins that the limits bound the blank pages of a repaired page tree: a
// blank page beyond MaxObjectNumber is a resource limit error, and MaxPageCount stops a tree of many bogus kids.
func TestReadAndValidateRelaxedBlankPageLimits(t *testing.T) {
	catalog := "<< /Type /Catalog /Pages 2 0 R >>"

	t.Run("object number limit", func(t *testing.T) {
		conf := model.NewTolerantConfiguration()
		conf.Limits.MaxObjectNumber = 4
		data := failureTestPDF(catalog, "<< /Type /Pages /Kids [3 0 R -1 0 R] /Count 2 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 1 1] >>", "null")
		_, err := ReadAndValidate(context.Background(), bytes.NewReader(data), conf)
		var limitErr *model.ResourceLimitError
		if !errors.As(err, &limitErr) || limitErr.Kind != model.LimitObjectNumber || limitErr.Limit != 4 || limitErr.Value != 5 {
			t.Fatalf("got %v, want a LimitObjectNumber error with limit 4 and value 5", err)
		}
		if errors.Is(err, ErrInvalidPDF) {
			t.Fatalf("limit classified as invalid input: %v", err)
		}
	})

	t.Run("page count limit", func(t *testing.T) {
		conf := model.NewTolerantConfiguration()
		conf.Limits.MaxPageCount = 100
		kids := func(n int) string {
			var b strings.Builder
			for i := 1; i <= n; i++ {
				fmt.Fprintf(&b, "-%d 0 R ", i)
			}
			return b.String()
		}
		pagesNode := func(n int) []byte {
			return failureTestPDF(catalog,
				fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d /MediaBox [0 0 1 1] >>", kids(n), n))
		}

		over := pagesNode(101)
		_, err := ReadAndValidate(context.Background(), bytes.NewReader(over), conf)
		var limitErr *model.ResourceLimitError
		if !errors.As(err, &limitErr) || limitErr.Kind != model.LimitPages || limitErr.Limit != 100 || limitErr.Value != 101 {
			t.Fatalf("validation: got %v, want a LimitPages error with limit 100 and value 101", err)
		}
		_, err = ReadPageBoundaries(context.Background(), bytes.NewReader(over), nil, conf)
		if !errors.As(err, &limitErr) || limitErr.Kind != model.LimitPages || limitErr.Limit != 100 || limitErr.Value != 101 {
			t.Fatalf("page boundaries: got %v, want a LimitPages error with limit 100 and value 101", err)
		}

		ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(pagesNode(100)), conf)
		if err != nil {
			t.Fatalf("100 kids: %v", err)
		}
		if ctx.PageCount != 100 || *ctx.Size != 103 {
			t.Fatalf("100 kids: page count %d, size %d; want 100 and 103", ctx.PageCount, *ctx.Size)
		}
	})
}

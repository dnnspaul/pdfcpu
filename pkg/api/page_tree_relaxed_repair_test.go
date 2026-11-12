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
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// pageTreeNodePDF returns a PDF whose page tree node 2 lists kids with the given Count, followed by
// the objects pages.
func pageTreeNodePDF(kids string, count int, pages ...string) []byte {
	objects := append([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, count),
	}, pages...)
	return failureTestPDF(objects...)
}

// writtenXRef returns the /Size of the trailer and the object numbers listed by the cross-reference
// stream of data, a file pdfcpu wrote. It fails if a listed object number is negative or if data has
// no cross-reference stream.
func writtenXRef(t *testing.T, data []byte) (size int, objNrs []int) {
	t.Helper()
	typ := regexp.MustCompile(`/Type\s*/XRef\b`).FindIndex(data)
	if typ == nil {
		t.Fatal("written file has no cross-reference stream")
	}
	stream := bytes.Index(data[typ[0]:], []byte("stream"))
	if stream < 0 {
		t.Fatal("written file: cross-reference stream without stream keyword")
	}
	stream += typ[0]
	header := bytes.LastIndex(data[:typ[0]], []byte(" obj"))
	if header < 0 {
		t.Fatal("written file: cross-reference stream without object header")
	}
	dict := string(data[header:stream])

	matchInt := func(re string) string {
		m := regexp.MustCompile(re).FindStringSubmatch(dict)
		if m == nil {
			t.Fatalf("written xref dict has no match for %s: %s", re, dict)
		}
		return m[1]
	}
	size = atoiT(t, matchInt(`/Size\s+(-?\d+)`))
	length := atoiT(t, matchInt(`/Length\s+(\d+)`))
	wm := regexp.MustCompile(`/W\s*\[\s*(\d+)\s+(\d+)\s+(\d+)\s*\]`).FindStringSubmatch(dict)
	if wm == nil {
		t.Fatalf("written xref dict has no /W: %s", dict)
	}
	rowLen := atoiT(t, wm[1]) + atoiT(t, wm[2]) + atoiT(t, wm[3])
	index := strings.Fields(matchInt(`/Index\s*\[([^\]]*)\]`))

	body := stream + len("stream")
	if data[body] == '\r' {
		body++
	}
	if data[body] != '\n' {
		t.Fatal("written file: cross-reference stream without newline after stream keyword")
	}
	body++
	zr, err := zlib.NewReader(bytes.NewReader(data[body : body+length]))
	if err != nil {
		t.Fatalf("written xref stream: %v", err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("written xref stream: %v", err)
	}

	rows := 0
	for i := 0; i+1 < len(index); i += 2 {
		first, count := atoiT(t, index[i]), atoiT(t, index[i+1])
		for k := 0; k < count; k++ {
			if first+k < 0 {
				t.Fatalf("written xref lists negative object number %d", first+k)
			}
			objNrs = append(objNrs, first+k)
		}
		rows += count
	}
	if len(raw) != rows*rowLen {
		t.Fatalf("written xref stream has %d bytes, want %d rows of %d", len(raw), rows, rowLen)
	}
	return size, objNrs
}

func atoiT(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return n
}

// mergeRawPages merges inputs with MergeRaw under conf and returns the page count of the output
// read back with ReadAndValidate under conf.
func mergeRawPages(t *testing.T, conf *model.Configuration, inputs ...[]byte) int {
	t.Helper()
	rsc := make([]io.ReadSeeker, len(inputs))
	for i, data := range inputs {
		rsc[i] = bytes.NewReader(data)
	}
	var buf bytes.Buffer
	if err := MergeRaw(context.Background(), rsc, &buf, false, conf); err != nil {
		t.Fatalf("merge: %v", err)
	}
	ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(buf.Bytes()), conf)
	if err != nil {
		t.Fatalf("reread merged: %v", err)
	}
	return ctx.PageCount
}

// TestReadAndValidateRelaxedRepairedWriteAndMerge pins that a page tree repaired by relaxed
// validation writes a file whose trailer Size is above every object number the cross-reference
// stream lists and which has no negative object number, that the file rereads with the same pages,
// and that it merges as the first input, as the second and with itself. The shapes are kids at or
// beyond Size in both orders, kids at the object number limit, a kid beyond it without a limit, kids
// in different subtrees, kids on the free list and negative kids.
func TestReadAndValidateRelaxedRepairedWriteAndMerge(t *testing.T) {
	const (
		catalog = "<< /Type /Catalog /Pages 2 0 R >>"
		page    = "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 400] >>"
	)
	tolerant := model.NewTolerantConfiguration()
	defaults := model.NewDefaultConfiguration()
	defaults.ValidationMode = model.ValidationRelaxed

	for _, tc := range []struct {
		name  string
		data  []byte
		conf  *model.Configuration
		pages int
	}{
		{"kids at size", failureTestPDF(catalog, "<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>", "null"), tolerant, 2},
		{"kids at size reversed", failureTestPDF(catalog, "<< /Type /Pages /Kids [4 0 R 3 0 R] /Count 2 >>", "null"), tolerant, 2},
		{"kids at object number limit", pageTreeKidsPDF("3 0 R 8388607 0 R"), tolerant, 2},
		{"kids at object number limit reversed", pageTreeKidsPDF("8388606 0 R 3 0 R"), tolerant, 2},
		{"kid beyond size", pageTreeKidsPDF("3 0 R 10 0 R", page), tolerant, 2},
		{"max int64 kid without object number limit", pageTreeKidsPDF("3 0 R 9223372036854775807 0 R"), defaults, 2},
		{"kids in different subtrees", failureTestPDF(
			catalog,
			"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
			"<< /Type /Pages /Parent 2 0 R /Kids [5 0 R] /Count 1 >>",
			"<< /Type /Pages /Parent 2 0 R /Kids [6 0 R] /Count 1 >>",
		), tolerant, 2},
		{"free list kids", pageTreeFreeList(t, failureTestPDF(
			catalog,
			"<< /Type /Pages /Kids [4 0 R 5 0 R 6 0 R] /Count 3 >>",
			"null", page, page, page, "null",
		), 3, 5, 7), tolerant, 3},
		{"negative kid", pageTreeNodePDF("-5 0 R 3 0 R", 1, page), tolerant, 1},
		{"negative kid -1", pageTreeNodePDF("-1 0 R 3 0 R", 1, page), tolerant, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(tc.data), tc.conf)
			if err != nil {
				t.Fatal(err)
			}
			if ctx.PageCount != tc.pages {
				t.Fatalf("got %d pages, want %d", ctx.PageCount, tc.pages)
			}
			want, err := ctx.PageBoundaries(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			pbs, err := ReadPageBoundaries(context.Background(), bytes.NewReader(tc.data), nil, tc.conf)
			if err != nil || !reflect.DeepEqual(pbs, want) {
				t.Fatalf("ReadPageBoundaries %+v, %v; validation %+v", pbs, err, want)
			}

			var buf bytes.Buffer
			if err := WriteContext(context.Background(), ctx, &buf); err != nil {
				t.Fatalf("write: %v", err)
			}
			written := buf.Bytes()

			size, objNrs := writtenXRef(t, written)
			for _, n := range objNrs {
				if n >= size {
					t.Fatalf("written Size %d does not cover obj#%d", size, n)
				}
			}

			back, err := ReadAndValidate(context.Background(), bytes.NewReader(written), tc.conf)
			if err != nil {
				t.Fatalf("reread: %v", err)
			}
			if back.PageCount != tc.pages {
				t.Fatalf("reread: got %d pages, want %d", back.PageCount, tc.pages)
			}

			if got, want := mergeRawPages(t, tc.conf, written, failureTestValidPDF()), tc.pages+1; got != want {
				t.Fatalf("merge repaired first: got %d pages, want %d", got, want)
			}
			if got, want := mergeRawPages(t, tc.conf, failureTestValidPDF(), written), tc.pages+1; got != want {
				t.Fatalf("merge repaired second: got %d pages, want %d", got, want)
			}
			if got, want := mergeRawPages(t, tc.conf, written, written), 2*tc.pages; got != want {
				t.Fatalf("merge repaired twice: got %d pages, want %d", got, want)
			}
		})
	}
}

// TestReadAndValidateRelaxedBlankKidGeneration pins that a missing kid repaired in place is
// referenced with generation 0, the generation the blank page is written with, whatever generation
// the Kids entry named.
func TestReadAndValidateRelaxedBlankKidGeneration(t *testing.T) {
	const page = "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 400] >>"
	conf := model.NewTolerantConfiguration()
	ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(pageTreeNodePDF("3 0 R 4 5 R", 2, page, "null")), conf)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteContext(context.Background(), ctx, &buf); err != nil {
		t.Fatalf("write: %v", err)
	}
	back, err := ReadContext(context.Background(), bytes.NewReader(buf.Bytes()), conf)
	if err != nil {
		t.Fatalf("reread: %v", err)
	}
	pages, err := back.Pages()
	if err != nil {
		t.Fatal(err)
	}
	root, err := back.DereferenceDict(*pages)
	if err != nil {
		t.Fatal(err)
	}
	kids, err := back.DereferenceArray(root["Kids"])
	if err != nil {
		t.Fatal(err)
	}
	if len(kids) != 2 {
		t.Fatalf("got %d kids, want 2", len(kids))
	}
	for i, o := range kids {
		ir, ok := o.(types.IndirectRef)
		if !ok {
			t.Fatalf("kid %d: got %T, want an indirect reference", i, o)
		}
		if g := ir.GenerationNumber.Value(); g != 0 {
			t.Fatalf("kid %d: written as %s, want generation 0", i, ir)
		}
	}
}

// TestReadAndValidateStrictRejectsNegativeKid pins that strict validation keeps rejecting a negative
// kid object number, which relaxed validation drops.
func TestReadAndValidateStrictRejectsNegativeKid(t *testing.T) {
	const page = "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 400] >>"
	strict := model.NewDefaultConfiguration()
	strict.ValidationMode = model.ValidationStrict
	for _, kids := range []string{"-5 0 R 3 0 R", "-1 0 R 3 0 R"} {
		_, err := ReadAndValidate(context.Background(), bytes.NewReader(pageTreeNodePDF(kids, 1, page)), strict)
		if err == nil || !strings.Contains(err.Error(), "corrupt page") {
			t.Fatalf("kids [%s]: got %v, want the strict corrupt page error", kids, err)
		}
	}
}

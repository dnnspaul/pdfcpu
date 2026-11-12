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
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// mergeDanglingSourcePDF returns a one page PDF whose Square annotation has /Parent 6 0 R.
// Object 6 does not exist in the source. Merged after a two page PDF, whose /Size is 5,
// the source objects are renumbered 5..8, so 6 would name the source's Pages dict.
func mergeDanglingSourcePDF() []byte {
	return failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Annots [4 0 R] >>",
		"<< /Type /Annot /Subtype /Square /Rect [100 100 200 200] /F 4 /Parent 6 0 R >>",
	)
}

// mergeDanglingRun merges pdfs with MergeRaw and returns the output.
func mergeDanglingRun(t *testing.T, pdfs [][]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := MergeRaw(context.Background(), mergeTestReaders(pdfs...), &out, false, model.NewDefaultConfiguration()); err != nil {
		t.Fatalf("merge: %v", err)
	}
	return out.Bytes()
}

// pdfDatePattern matches a PDF date string. The Info dict stamps the time of the write.
var pdfDatePattern = regexp.MustCompile(`\(D:[^)]*\)`)

// mergeDanglingObjects returns the objects of out keyed by object number, one per line, in
// number order. Object streams and xref streams are left out: their packing and layout depend
// on the writer's traversal order, not on the merged content. Info dates are masked and the
// trailer /ID is not included.
func mergeDanglingObjects(t *testing.T, out []byte) string {
	t.Helper()
	ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(out), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatalf("re-read merged output: %v", err)
	}
	nums := make([]int, 0, len(ctx.Table))
	for k, e := range ctx.Table {
		if e != nil && !e.Free {
			nums = append(nums, k)
		}
	}
	slices.Sort(nums)
	var b strings.Builder
	for _, k := range nums {
		o, err := ctx.Dereference(*types.NewIndirectRef(k, 0))
		if err != nil {
			t.Fatalf("object %d: %v", k, err)
		}
		if o == nil {
			fmt.Fprintf(&b, "%d: null\n", k)
			continue
		}
		switch o.(type) {
		case types.ObjectStreamDict, types.XRefStreamDict:
			continue
		}
		fmt.Fprintf(&b, "%d: %s\n", k, pdfDatePattern.ReplaceAllString(o.PDFString(), "(D)"))
	}
	return b.String()
}

// requireDanglingParentDangling checks that every Square annotation of out has no /Parent, or a
// /Parent that is still a reference, resolving to null. A dangling reference must also have an
// in-use xref entry without an object, so that a later merge does not assign its number to another
// object. It also checks that there are squares annotations.
func requireDanglingParentDangling(t *testing.T, out []byte, squares int) {
	t.Helper()
	ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(out), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatalf("re-read merged output: %v", err)
	}
	found := 0
	for i := 1; i <= ctx.PageCount; i++ {
		pd, _, _, err := ctx.PageDict(context.Background(), i, false)
		if err != nil {
			t.Fatal(err)
		}
		if pd["Annots"] == nil {
			continue
		}
		arr, err := ctx.DereferenceArray(pd["Annots"])
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range arr {
			annot, err := ctx.DereferenceDict(o)
			if err != nil {
				t.Fatal(err)
			}
			if annot["Subtype"] != types.Name("Square") {
				continue
			}
			found++
			if p, ok := annot["Parent"]; ok && p != nil {
				ir, ok := p.(types.IndirectRef)
				if !ok {
					t.Fatalf("page %d: /Parent is %v, want a reference", i, p)
				}
				target, err := ctx.Dereference(ir)
				if err != nil {
					t.Fatal(err)
				}
				if target != nil {
					t.Fatalf("page %d: dangling /Parent %v resolves to %v, want null", i, ir, target)
				}
				if e := ctx.Table[ir.ObjectNumber.Value()]; e == nil || e.Free || e.Object != nil {
					t.Fatalf("page %d: dangling /Parent %v has no in-use xref entry without an object", i, ir)
				}
			}
		}
	}
	if found != squares {
		t.Fatalf("found %d square annotations, want %d", found, squares)
	}
}

// TestMergeRawDanglingRefStaysDanglingAndReproducible merges inputs whose source holds a dangling
// reference that lands in the renumbered range. Every merge must succeed, keep the reference
// dangling, and give every object the same number and content on every run.
func TestMergeRawDanglingRefStaysDanglingAndReproducible(t *testing.T) {
	two := mergeTestPDF(2, false)
	src := mergeDanglingSourcePDF()

	// The collision premise: the first input has /Size 5, so renumbered source objects start at 5.
	destCtx, err := ReadAndValidate(context.Background(), bytes.NewReader(two), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	if *destCtx.Size != 5 {
		t.Fatalf("destination /Size %d, want 5", *destCtx.Size)
	}

	const runs = 40
	cases := []struct {
		name    string
		pdfs    [][]byte
		squares int
	}{
		{"two,src", [][]byte{two, src}, 1},
		{"two,src,src", [][]byte{two, src, src}, 2},
		{"two,two,src", [][]byte{two, two, src}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := mergeDanglingRun(t, tc.pdfs)
			requireDanglingParentDangling(t, first, tc.squares)
			want := mergeDanglingObjects(t, first)
			for i := 1; i < runs; i++ {
				out := mergeDanglingRun(t, tc.pdfs)
				if got := mergeDanglingObjects(t, out); got != want {
					t.Fatalf("run %d: objects differ from run 0:\n%s\nwant:\n%s", i, got, want)
				}
			}
		})
	}
}

// mergeDanglingFormSourcePDF returns a one page PDF whose page /Contents, AcroForm /Fields and
// field /Kids hold references 97, 98 and 99, which do not exist.
func mergeDanglingFormSourcePDF() []byte {
	return failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R /AcroForm << /Fields [5 0 R 98 0 R] >> >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents [4 0 R 97 0 R] /Annots [6 0 R] >>",
		"<< /Length 4 >>\nstream\nq Q\nendstream",
		"<< /FT /Tx /T (f) /DA (/Helv 0 Tf 0 g) /Kids [6 0 R 99 0 R] >>",
		"<< /Type /Annot /Subtype /Widget /Rect [100 100 200 200] /F 4 /Parent 5 0 R >>",
	)
}

// requireMergeDanglingReadValid re-reads out in relaxed mode, which must validate it.
func requireMergeDanglingReadValid(t *testing.T, out []byte) {
	t.Helper()
	if _, err := ReadAndValidate(context.Background(), bytes.NewReader(out), model.NewDefaultConfiguration()); err != nil {
		t.Fatalf("re-read merged output: %v", err)
	}
}

// TestMergeRawDanglingFormRefsStayValid merges a source whose page /Contents, AcroForm /Fields and
// field /Kids hold dangling references. The merged output must validate and merge again.
func TestMergeRawDanglingFormRefsStayValid(t *testing.T) {
	two := mergeTestPDF(2, false)
	out := mergeDanglingRun(t, [][]byte{two, mergeDanglingFormSourcePDF()})
	requireMergeDanglingReadValid(t, out)

	again := mergeDanglingRun(t, [][]byte{out, two})
	requireMergeDanglingReadValid(t, again)
}

// TestMergeRawDanglingRefSurvivesSecondMerge merges a source with a dangling /Parent, then merges the
// output again. The reference must still resolve to null after the second merge, whatever the
// xref and object stream settings. Object streams imply an xref stream, see model.Configuration.
func TestMergeRawDanglingRefSurvivesSecondMerge(t *testing.T) {
	two := mergeTestPDF(2, false)
	src := mergeDanglingSourcePDF()
	cases := []struct {
		name       string
		xRefStream bool
		objStream  bool
	}{
		{"classic,noobjstm", false, false},
		{"xrefstm,noobjstm", true, false},
		{"classic,objstm", false, true},
		{"xrefstm,objstm", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conf := model.NewDefaultConfiguration()
			conf.WriteXRefStream = tc.xRefStream
			conf.WriteObjectStream = tc.objStream

			var first bytes.Buffer
			if err := MergeRaw(context.Background(), mergeTestReaders(two, src), &first, false, conf); err != nil {
				t.Fatalf("first merge: %v", err)
			}
			requireDanglingParentDangling(t, first.Bytes(), 1)

			var again bytes.Buffer
			if err := MergeRaw(context.Background(), mergeTestReaders(first.Bytes(), two), &again, false, conf); err != nil {
				t.Fatalf("second merge: %v", err)
			}
			requireDanglingParentDangling(t, again.Bytes(), 1)
		})
	}
}

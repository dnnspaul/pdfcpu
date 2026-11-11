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
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// structTreeChainPDF builds a catalog whose StructTreeRoot reaches n structure elements through
// K, or through the IDTree when viaIDTree is set. The last element points back to the first one
// when closed is set and has no K otherwise. lastBody, if not empty, replaces the last element.
// parentTree is the ParentTree object of the K variant.
func structTreeChainPDF(n int, viaIDTree, resources, closed bool, parentTree, lastBody string) []byte {
	page := "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /StructParents 0 >>"
	if resources {
		page = "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /StructParents 0 /Resources << >> >>"
	}
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R /StructTreeRoot 4 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		page,
	}
	if viaIDTree {
		objects = append(objects, "<< /Type /StructTreeRoot /IDTree 5 0 R >>", "<< /Names [(a) 6 0 R] >>")
	} else {
		objects = append(objects, "<< /Type /StructTreeRoot /K [6 0 R] /ParentTree 5 0 R >>", parentTree)
	}
	objects = append(objects, structTreeElementObjects(n, closed, lastBody)...)
	return failureTestPDF(objects...)
}

// structTreeElementObjects returns n structure element objects numbered from 6, linked through K arrays.
// The last element links back to the first one when closed is set. lastBody, if not empty, replaces it.
func structTreeElementObjects(n int, closed bool, lastBody string) []string {
	objects := make([]string, 0, n)
	for i := range n {
		k := ""
		switch {
		case i < n-1:
			k = fmt.Sprintf(" /K [%d 0 R]", 7+i)
		case closed:
			k = " /K [6 0 R]"
		}
		body := fmt.Sprintf("<< /Type /StructElem /S /P /P 4 0 R /Pg 3 0 R%s >>", k)
		if i == n-1 && lastBody != "" {
			body = lastBody
		}
		objects = append(objects, body)
	}
	return objects
}

// structTreeParentTreeCyclePDF builds a closed chain of n structure elements that only the ParentTree
// reaches. The StructTreeRoot has no K, so validateStructureHierarchy does not walk the chain.
func structTreeParentTreeCyclePDF(n int) []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R /StructTreeRoot 4 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /StructParents 0 >>",
		"<< /Type /StructTreeRoot /ParentTree 5 0 R >>",
		"<< /Nums [0 6 0 R] >>",
	}
	return failureTestPDF(append(objects, structTreeElementObjects(n, true, "")...)...)
}

// structTreeSingleRefCyclePDF builds a closed chain of n structure elements linked by single K
// references instead of K arrays.
func structTreeSingleRefCyclePDF(n int) []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R /StructTreeRoot 4 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /StructParents 0 >>",
		"<< /Type /StructTreeRoot /K 6 0 R /ParentTree 5 0 R >>",
		"<< /Nums [] >>",
	}
	for i := range n {
		next := 6 + (i+1)%n
		objects = append(objects, fmt.Sprintf("<< /Type /StructElem /S /P /P 4 0 R /Pg 3 0 R /K %d 0 R >>", next))
	}
	return failureTestPDF(objects...)
}

// structTreeCyclePDF builds a closed chain of n structure elements, see structTreeChainPDF.
func structTreeCyclePDF(n int, viaIDTree, resources bool) []byte {
	return structTreeChainPDF(n, viaIDTree, resources, true, "<< /Nums [] >>", "")
}

// structTreeDeepPDF builds an acyclic chain of n structure elements whose last element is lastBody.
func structTreeDeepPDF(n int, lastBody string) []byte {
	return structTreeChainPDF(n, false, false, false, "<< /Nums [] >>", lastBody)
}

// structTreeRelaxedConfiguration returns the default relaxed configuration with the struct tree flag set.
func structTreeRelaxedConfiguration(remove bool) *model.Configuration {
	conf := model.NewDefaultConfiguration()
	conf.RemoveInvalidStructTree = remove
	return conf
}

// TestStructTreeParentTreeWithoutKidsOrNums verifies a ParentTree without Kids or Nums is a notice in relaxed mode.
func TestStructTreeParentTreeWithoutKidsOrNums(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(fmt.Sprintf("remove=%t", remove), func(t *testing.T) {
			ctx, err := ReadAndValidate(context.Background(),
				bytes.NewReader(failureTestStructTreePDF(3, "<< /Limits [0 1] >>")), structTreeRelaxedConfiguration(remove))
			if err != nil {
				t.Fatalf("got %v, want no error", err)
			}
			report := ctx.ValidationReport()
			if report.Len() != 1 {
				t.Fatalf("got %d notices, want 1", report.Len())
			}
			n := report.Notices()[0]
			if n.Message != "structure tree ParentTree: missing Kids or Nums" {
				t.Fatalf("got message %q", n.Message)
			}
			if n.Disposition != model.NoticeSkipped || n.ObjectNumber != 5 {
				t.Fatalf("got disposition %q object %d, want skipped object 5", n.Disposition, n.ObjectNumber)
			}
			if ctx.XRefTable.StructTreeRemoved {
				t.Fatal("structure tree removed for a notice")
			}
			if _, ok := ctx.XRefTable.RootDict.Find("StructTreeRoot"); !ok {
				t.Fatal("StructTreeRoot missing from catalog")
			}
		})
	}
}

// TestStructTreeLongCycles verifies a relaxed cycle of any length is one notice and keeps the tree.
func TestStructTreeLongCycles(t *testing.T) {
	cases := []struct {
		name      string
		data      []byte
		wantNotes int
	}{
		{name: "K cycle 101", data: structTreeCyclePDF(101, false, false), wantNotes: 1},
		{name: "K cycle 1000", data: structTreeCyclePDF(1000, false, false), wantNotes: 1},
		{name: "IDTree cycle 101", data: structTreeCyclePDF(101, true, false), wantNotes: 1},
		{name: "IDTree cycle 1000", data: structTreeCyclePDF(1000, true, false), wantNotes: 1},
		{name: "IDTree cycle 2", data: structTreeCyclePDF(2, true, false), wantNotes: 1},
		{name: "ParentTree cycle 101", data: structTreeParentTreeCyclePDF(101), wantNotes: 1},
		{name: "ParentTree cycle 1000", data: structTreeParentTreeCyclePDF(1000), wantNotes: 1},
		{name: "single ref cycle 3", data: structTreeSingleRefCyclePDF(3), wantNotes: 1},
		{name: "single ref cycle 150", data: structTreeSingleRefCyclePDF(150), wantNotes: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(tc.data), structTreeRelaxedConfiguration(true))
			if err != nil {
				t.Fatalf("got %v, want no error", err)
			}
			report := ctx.ValidationReport()
			if report.Len() != tc.wantNotes {
				t.Fatalf("got %d notices, want %d: %v", report.Len(), tc.wantNotes, report.Notices())
			}
			if cause := report.Notices()[0].Cause; !errors.Is(cause, model.ErrStructureTreeCycle) {
				t.Fatalf("got cause %v, want ErrStructureTreeCycle", cause)
			}
			if ctx.XRefTable.StructTreeRemoved {
				t.Fatal("cycle removed the structure tree")
			}
		})
	}
}

// countingContext counts the context checks, which call Err, and never reports an error.
type countingContext struct {
	context.Context
	checks int
}

// Err implements context.Context.
func (c *countingContext) Err() error {
	c.checks++
	return nil
}

// TestStructTreeSharedChainWalkIsBounded verifies that ParentTree values sharing one deep chain do not
// walk the chain again for each value. Without the explored set the cycle walks cost elements x elements.
func TestStructTreeSharedChainWalkIsBounded(t *testing.T) {
	const elements = 1000
	var nums strings.Builder
	nums.WriteString("<< /Nums [")
	for i := range elements {
		fmt.Fprintf(&nums, "%d 6 0 R ", i)
	}
	nums.WriteString("] >>")
	data := structTreeChainPDF(elements, false, false, false, nums.String(),
		"<< /Type /StructElem /S /P /P 4 0 R /Pg 3 0 R >>")

	c := &countingContext{Context: context.Background()}
	if _, err := ReadAndValidate(c, bytes.NewReader(data), structTreeRelaxedConfiguration(false)); err != nil {
		t.Fatalf("got %v, want no error", err)
	}
	// Each value descends about MaxRecursionDepth frames in the semantic pass. Measured: about
	// 0.34 million checks with the explored set, about 5.3 million without it.
	const bound = 10 * elements * 100
	t.Logf("context checks: %d, bound %d", c.checks, bound)
	if c.checks > bound {
		t.Fatalf("got %d context checks, want at most %d", c.checks, bound)
	}
}

// TestStructTreeDeepAcyclicRelaxed verifies a deep acyclic structure tree validates without notices.
func TestStructTreeDeepAcyclicRelaxed(t *testing.T) {
	deep := "<< /Type /StructElem /S /P /P 4 0 R /Pg 3 0 R >>"
	for _, viaIDTree := range []bool{false, true} {
		data := structTreeChainPDF(1000, viaIDTree, false, false, "<< /Nums [] >>", deep)
		ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(data), structTreeRelaxedConfiguration(false))
		if err != nil {
			t.Fatalf("viaIDTree %t: got %v, want no error", viaIDTree, err)
		}
		if n := ctx.ValidationReport().Len(); n != 0 {
			t.Fatalf("viaIDTree %t: got %d notices, want 0", viaIDTree, n)
		}
	}
}

// TestStructTreeDeepInvalidElementRelaxed verifies an invalid element below the recursion limit is still
// reported, so deferred validation is not silently skipped.
func TestStructTreeDeepInvalidElementRelaxed(t *testing.T) {
	bad := "<< /Type /StructElem /S /P /P 4 0 R /Pg 3 0 R /A 5 >>"
	data := structTreeDeepPDF(150, bad)
	_, err := ReadAndValidate(context.Background(), bytes.NewReader(data), structTreeRelaxedConfiguration(false))
	if !errors.Is(err, ErrInvalidStructTree) {
		t.Fatalf("got %v, want ErrInvalidStructTree", err)
	}
	if !strings.Contains(err.Error(), "obj#") {
		t.Fatalf("got %v, want an object number in the error", err)
	}
}

// TestStructTreeDeepHierarchyErrorIsBounded verifies a failure below the recursion limit keeps a bounded message.
func TestStructTreeDeepHierarchyErrorIsBounded(t *testing.T) {
	bad := "<< /Type 5 /S /P /P 4 0 R >>"
	_, err := ReadAndValidate(context.Background(), bytes.NewReader(structTreeDeepPDF(1000, bad)), structTreeRelaxedConfiguration(false))
	if !errors.Is(err, ErrInvalidStructTree) {
		t.Fatalf("got %v, want ErrInvalidStructTree", err)
	}
	if n := len(err.Error()); n > 16384 {
		t.Fatalf("error text is %d bytes, want at most 16384", n)
	}
}

// TestStructTreeStrictUnchanged verifies strict mode keeps its depth limit and cycle errors.
func TestStructTreeStrictUnchanged(t *testing.T) {
	strict := func() *model.Configuration {
		conf := model.NewDefaultConfiguration()
		conf.ValidationMode = model.ValidationStrict
		conf.RemoveInvalidStructTree = true
		return conf
	}
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{name: "K cycle 101", data: structTreeCyclePDF(101, false, true), want: "max recursion depth exceeded (depth 101, limit 100)"},
		{name: "K cycle 2", data: structTreeCyclePDF(2, false, true), want: "circular structure tree"},
		{
			name: "invalid ParentTree",
			data: failureTestPDF(
				"<< /Type /Catalog /Pages 2 0 R /StructTreeRoot 4 0 R >>",
				"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
				"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> >>",
				"<< /Type /StructTreeRoot /K [6 0 R] /ParentTree 5 0 R >>",
				"<< /Kids [99 0 R] >>",
				"<< /Type /StructElem /S /P /P 4 0 R /Pg 3 0 R >>",
			),
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadAndValidate(context.Background(), bytes.NewReader(tc.data), strict())
			if err == nil {
				t.Fatal("got no error")
			}
			if !errors.Is(err, ErrInvalidStructTree) {
				t.Fatalf("got %v, want ErrInvalidStructTree", err)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// structTreeMissingKidsPDF builds a catalog with the ParentTree "<< /Kids [99 0 R] >>" and one element.
func structTreeMissingKidsPDF() []byte {
	return failureTestStructTreePDF(3, "<< /Kids [99 0 R] >>")
}

// TestRemoveInvalidStructTree verifies the flag removes an invalid tree in relaxed mode and keeps it otherwise.
func TestRemoveInvalidStructTree(t *testing.T) {
	ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(structTreeMissingKidsPDF()), structTreeRelaxedConfiguration(true))
	if err != nil {
		t.Fatalf("got %v, want no error", err)
	}
	if !ctx.XRefTable.StructTreeRemoved {
		t.Fatal("StructTreeRemoved not set")
	}
	if _, ok := ctx.XRefTable.RootDict.Find("StructTreeRoot"); ok {
		t.Fatal("StructTreeRoot still in catalog")
	}
	notices := ctx.ValidationReport().Notices()
	if len(notices) != 1 {
		t.Fatalf("got %d notices, want 1: %v", len(notices), notices)
	}
	n := notices[0]
	if n.Path != "StructTreeRoot" || n.Disposition != model.NoticeRepaired {
		t.Fatalf("got path %q disposition %q", n.Path, n.Disposition)
	}
	const wantMessage = "StructTreeRoot: number tree StructTree Kids[0] obj#99: missing dict"
	if n.Message != wantMessage {
		t.Fatalf("got message %q, want %q", n.Message, wantMessage)
	}
	if !errors.Is(model.WithInvalidPDF(n.Cause, model.NoticePhaseValidate), model.ErrInvalidStructTree) {
		t.Fatalf("cause %v is not ErrInvalidStructTree", n.Cause)
	}

	_, err = ReadAndValidate(context.Background(), bytes.NewReader(structTreeMissingKidsPDF()), structTreeRelaxedConfiguration(false))
	if !errors.Is(err, ErrInvalidStructTree) {
		t.Fatalf("flag off: got %v, want ErrInvalidStructTree", err)
	}
}

// TestRemoveInvalidStructTreeMixedDefect verifies a removed tree does not hide a failure elsewhere.
func TestRemoveInvalidStructTreeMixedDefect(t *testing.T) {
	// relaxedFatalAnnot is an annotation defect that fails validation in relaxed mode.
	const relaxedFatalAnnot = "<< /Type /Annot /Subtype /Link /Rect 5 >>"
	mixed := func(parentTree string) []byte {
		return failureTestPDF(
			"<< /Type /Catalog /Pages 2 0 R /StructTreeRoot 4 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Annots [7 0 R] >>",
			"<< /Type /StructTreeRoot /K [6 0 R] /ParentTree 5 0 R >>",
			parentTree,
			"<< /Type /StructElem /S /P /P 4 0 R /Pg 3 0 R >>",
			relaxedFatalAnnot,
		)
	}
	if _, err := ReadAndValidate(context.Background(), bytes.NewReader(mixed("<< /Nums [] >>")), structTreeRelaxedConfiguration(true)); err == nil {
		t.Fatal("annotation alone: got no error, choose another relaxed-fatal annotation defect")
	}

	_, err := ReadAndValidate(context.Background(), bytes.NewReader(mixed("<< /Kids [99 0 R] >>")), structTreeRelaxedConfiguration(true))
	if !errors.Is(err, ErrInvalidPDF) {
		t.Fatalf("got %v, want ErrInvalidPDF", err)
	}
	if errors.Is(err, ErrInvalidStructTree) {
		t.Fatalf("annotation failure classified as structure tree: %v", err)
	}
	if strings.Contains(err.Error(), "StructTreeRoot") {
		t.Fatalf("annotation failure names StructTreeRoot: %v", err)
	}
}

// TestRemoveInvalidStructTreeDeadline verifies a deadline hit at any point is never reported as invalid input.
func TestRemoveInvalidStructTreeDeadline(t *testing.T) {
	data := failureTestStructTreePDF(50, "<< /Kids [99 0 R] >>")
	conf := structTreeRelaxedConfiguration(true)
	structTreeMessages := 0
	for checks := 0; ; checks++ {
		if checks > 100000 {
			t.Fatal("validation did not complete within 100000 context checks")
		}
		c := &deadlineAfterChecksContext{Context: context.Background(), checks: checks}
		ctx, err := ReadAndValidate(c, bytes.NewReader(data), conf)
		if err == nil {
			if !ctx.XRefTable.StructTreeRemoved {
				t.Fatalf("checks %d: validation completed without removing the tree", checks)
			}
			break
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("checks %d: got %v, want DeadlineExceeded", checks, err)
		}
		if errors.Is(err, ErrInvalidPDF) || errors.Is(err, ErrInvalidStructTree) || IsDeterministicFailure(err) {
			t.Fatalf("checks %d: deadline classified as invalid input: %v", checks, err)
		}
		if strings.Contains(err.Error(), "StructTreeRoot: ") {
			structTreeMessages++
		}
	}
	if structTreeMessages == 0 {
		t.Fatal("no deadline was hit below StructTreeRoot")
	}
}

// TestRemoveInvalidStructTreeContextCancelIsNotRemoved verifies a cancellation inside the tree stays a context error.
func TestRemoveInvalidStructTreeContextCancelIsNotRemoved(t *testing.T) {
	cause := errors.New("caller stopped")
	_, err := ReadAndValidate(failureTestCanceledContext(cause), bytes.NewReader(structTreeMissingKidsPDF()), structTreeRelaxedConfiguration(true))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrInvalidStructTree) || errors.Is(err, ErrInvalidPDF) {
		t.Fatalf("cancellation classified as invalid input: %v", err)
	}
}

// TestRemoveInvalidStructTreeReaderFailure verifies the flag never removes a tree on a reader failure, and that
// it does not change the outcome of reads that fail. Reads of the structure tree objects fail in the read phase,
// so no API input reaches the ReaderErr guard in removeInvalidStructTree; that guard is defensive.
func TestRemoveInvalidStructTreeReaderFailure(t *testing.T) {
	data := failureTestStructTreePDF(3, "<< /Nums [] >>")
	readErr := errors.New("connection reset by peer")
	reached := 0
	for lo := range len(data) {
		read := func() io.ReadSeeker {
			return &badRangeReader{Reader: bytes.NewReader(data), lo: int64(lo), hi: int64(lo) + 8, err: readErr}
		}
		_, errOff := ReadAndValidate(context.Background(), read(), structTreeRelaxedConfiguration(false))
		ctxOn, errOn := ReadAndValidate(context.Background(), read(), structTreeRelaxedConfiguration(true))
		if errOn == nil && ctxOn.XRefTable.StructTreeRemoved {
			t.Fatalf("offset %d: reader failure removed the structure tree", lo)
		}
		if fmt.Sprint(errOn) != fmt.Sprint(errOff) {
			t.Fatalf("offset %d: flag changed the outcome: got %v, want %v", lo, errOn, errOff)
		}
		if errOff != nil && !errors.Is(errOff, readErr) {
			reached++
		}
	}
	if reached == 0 {
		t.Fatal("relaxed reading never turned a failed read into a validation failure")
	}
}

// TestRemoveInvalidStructTreeNoticeLimit verifies a notice limit inside the tree stays a resource limit.
func TestRemoveInvalidStructTreeNoticeLimit(t *testing.T) {
	data := failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R /StructTreeRoot 4 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>",
		"<< /Type /StructTreeRoot /K [6 0 R 8 0 R 10 0 R] /ParentTree 5 0 R >>",
		"<< /Kids [99 0 R] >>",
		"<< /Type /StructElem /S /P /P 4 0 R /Pg 3 0 R /K [7 0 R] >>",
		"<< /Type /StructElem /S /P /P 4 0 R /Pg 3 0 R /K [6 0 R] >>",
		"<< /Type /StructElem /S /P /P 4 0 R /Pg 3 0 R /K [9 0 R] >>",
		"<< /Type /StructElem /S /P /P 4 0 R /Pg 3 0 R /K [8 0 R] >>",
		"<< /Type /StructElem /S /P /P 4 0 R /Pg 3 0 R /K [11 0 R] >>",
		"<< /Type /StructElem /S /P /P 4 0 R /Pg 3 0 R /K [10 0 R] >>",
	)
	conf := structTreeRelaxedConfiguration(true)
	conf.Limits.MaxNotices = 2
	_, err := ReadAndValidate(context.Background(), bytes.NewReader(data), conf)
	if !errors.Is(err, model.ErrResourceLimit) {
		t.Fatalf("got %v, want ErrResourceLimit", err)
	}
	var limitErr *model.ResourceLimitError
	if !errors.As(err, &limitErr) || limitErr.Kind != model.LimitNotices {
		t.Fatalf("got %v, want *ResourceLimitError of kind LimitNotices", err)
	}
	if errors.Is(err, ErrInvalidStructTree) {
		t.Fatalf("notice limit classified as structure tree: %v", err)
	}
}

// TestRemoveInvalidStructTreeFlagSurvivesCap verifies the removal flag is kept when notices are capped.
func TestRemoveInvalidStructTreeFlagSurvivesCap(t *testing.T) {
	data := structTreeChainPDF(2, false, false, true, "<< /Kids [99 0 R] >>", "")
	conf := structTreeRelaxedConfiguration(true)
	conf.Limits.MaxRetainedNotices = 1
	ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(data), conf)
	if err != nil {
		t.Fatalf("got %v, want no error", err)
	}
	report := ctx.ValidationReport()
	if report.Len() != 1 || report.Dropped() != 1 {
		t.Fatalf("got %d stored and %d dropped, want 1 and 1", report.Len(), report.Dropped())
	}
	if cause := report.Notices()[0].Cause; !errors.Is(cause, model.ErrStructureTreeCycle) {
		t.Fatalf("stored notice is %v, want the cycle", cause)
	}
	if !ctx.XRefTable.StructTreeRemoved {
		t.Fatal("removal flag lost with the dropped notice")
	}
}

// TestRemoveInvalidStructTreeRoundTrip verifies a document with a removed tree writes, merges and extracts.
func TestRemoveInvalidStructTreeRoundTrip(t *testing.T) {
	conf := structTreeRelaxedConfiguration(true)
	ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(structTreeMissingKidsPDF()), conf)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := WriteContext(context.Background(), ctx, &out); err != nil {
		t.Fatal(err)
	}
	written := out.Bytes()
	reread, err := ReadAndValidate(context.Background(), bytes.NewReader(written), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatalf("written file: %v", err)
	}
	if _, ok := reread.XRefTable.RootDict.Find("StructTreeRoot"); ok {
		t.Fatal("written file still has StructTreeRoot")
	}

	var merged bytes.Buffer
	inputs := []io.ReadSeeker{bytes.NewReader(written), bytes.NewReader(written)}
	if err := MergeRaw(context.Background(), inputs, &merged, false, model.NewDefaultConfiguration()); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if _, err := ReadAndValidate(context.Background(), bytes.NewReader(merged.Bytes()), model.NewDefaultConfiguration()); err != nil {
		t.Fatalf("merged file: %v", err)
	}

	digest := func(io.Reader, int) error { return nil }
	if err := ExtractPages(context.Background(), bytes.NewReader(written), []string{"1"}, digest, model.NewDefaultConfiguration()); err != nil {
		t.Fatalf("extract: %v", err)
	}
}

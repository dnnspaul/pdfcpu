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
	"reflect"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
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

// structTreeKeepConfiguration returns the default configuration with KeepInvalidStructTree set.
func structTreeKeepConfiguration() *model.Configuration {
	conf := model.NewDefaultConfiguration()
	conf.KeepInvalidStructTree = true
	return conf
}

// structTreeRemoveConfiguration is structTreeRelaxedConfiguration(true), with the tree kept if keep is set.
func structTreeRemoveConfiguration(keep bool) *model.Configuration {
	conf := structTreeRelaxedConfiguration(true)
	conf.KeepInvalidStructTree = keep
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
	for _, keep := range []bool{false, true} {
		t.Run(fmt.Sprintf("keep=%t", keep), func(t *testing.T) {
			removeInvalidStructTreeMixedDefect(t, keep)
		})
	}
}

func removeInvalidStructTreeMixedDefect(t *testing.T, keep bool) {
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
	if _, err := ReadAndValidate(context.Background(), bytes.NewReader(mixed("<< /Nums [] >>")), structTreeRemoveConfiguration(keep)); err == nil {
		t.Fatal("annotation alone: got no error, choose another relaxed-fatal annotation defect")
	}

	_, err := ReadAndValidate(context.Background(), bytes.NewReader(mixed("<< /Kids [99 0 R] >>")), structTreeRemoveConfiguration(keep))
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
	for _, keep := range []bool{false, true} {
		t.Run(fmt.Sprintf("keep=%t", keep), func(t *testing.T) {
			removeInvalidStructTreeDeadline(t, keep)
		})
	}
}

func removeInvalidStructTreeDeadline(t *testing.T, keep bool) {
	data := failureTestStructTreePDF(50, "<< /Kids [99 0 R] >>")
	conf := structTreeRemoveConfiguration(keep)
	structTreeMessages := 0
	for checks := 0; ; checks++ {
		if checks > 100000 {
			t.Fatal("validation did not complete within 100000 context checks")
		}
		c := &deadlineAfterChecksContext{Context: context.Background(), checks: checks}
		ctx, err := ReadAndValidate(c, bytes.NewReader(data), conf)
		if err == nil {
			if !ctx.XRefTable.StructTreeInvalid || ctx.XRefTable.StructTreeRemoved != !keep {
				t.Fatalf("checks %d: validation completed with invalid %t and removed %t",
					checks, ctx.XRefTable.StructTreeInvalid, ctx.XRefTable.StructTreeRemoved)
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
	for _, keep := range []bool{false, true} {
		t.Run(fmt.Sprintf("keep=%t", keep), func(t *testing.T) {
			removeInvalidStructTreeContextCancelIsNotRemoved(t, keep)
		})
	}
}

func removeInvalidStructTreeContextCancelIsNotRemoved(t *testing.T, keep bool) {
	cause := errors.New("caller stopped")
	_, err := ReadAndValidate(failureTestCanceledContext(cause), bytes.NewReader(structTreeMissingKidsPDF()), structTreeRemoveConfiguration(keep))
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
	for _, keep := range []bool{false, true} {
		t.Run(fmt.Sprintf("keep=%t", keep), func(t *testing.T) {
			removeInvalidStructTreeReaderFailure(t, keep)
		})
	}
}

func removeInvalidStructTreeReaderFailure(t *testing.T, keep bool) {
	data := failureTestStructTreePDF(3, "<< /Nums [] >>")
	readErr := errors.New("connection reset by peer")
	reached := 0
	for lo := range len(data) {
		read := func() io.ReadSeeker {
			return &badRangeReader{Reader: bytes.NewReader(data), lo: int64(lo), hi: int64(lo) + 8, err: readErr}
		}
		_, errOff := ReadAndValidate(context.Background(), read(), structTreeRelaxedConfiguration(false))
		ctxOn, errOn := ReadAndValidate(context.Background(), read(), structTreeRemoveConfiguration(keep))
		if errOn == nil && ctxOn.XRefTable.StructTreeInvalid {
			t.Fatalf("offset %d: reader failure tolerated the structure tree", lo)
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
	for _, keep := range []bool{false, true} {
		t.Run(fmt.Sprintf("keep=%t", keep), func(t *testing.T) {
			removeInvalidStructTreeNoticeLimit(t, keep)
		})
	}
}

func removeInvalidStructTreeNoticeLimit(t *testing.T, keep bool) {
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
	conf := structTreeRemoveConfiguration(keep)
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
	for _, keep := range []bool{false, true} {
		t.Run(fmt.Sprintf("keep=%t", keep), func(t *testing.T) {
			removeInvalidStructTreeFlagSurvivesCap(t, keep)
		})
	}
}

func removeInvalidStructTreeFlagSurvivesCap(t *testing.T, keep bool) {
	data := structTreeChainPDF(2, false, false, true, "<< /Kids [99 0 R] >>", "")
	conf := structTreeRemoveConfiguration(keep)
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
	if !ctx.XRefTable.StructTreeInvalid || ctx.XRefTable.StructTreeRemoved != !keep {
		t.Fatalf("flags lost with the dropped notice: invalid %t, removed %t", ctx.XRefTable.StructTreeInvalid, ctx.XRefTable.StructTreeRemoved)
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

// requireKeptMissingKidsTree checks that ctx kept the tree of structTreeMissingKidsPDF, with one skipped notice.
func requireKeptMissingKidsTree(t *testing.T, ctx *model.Context) {
	t.Helper()
	if !ctx.XRefTable.StructTreeInvalid || ctx.XRefTable.StructTreeRemoved {
		t.Fatalf("StructTreeInvalid %t, StructTreeRemoved %t, want true and false",
			ctx.XRefTable.StructTreeInvalid, ctx.XRefTable.StructTreeRemoved)
	}
	root, found := ctx.XRefTable.RootDict.Find("StructTreeRoot")
	if !found || root != *types.NewIndirectRef(4, 0) {
		t.Fatalf("StructTreeRoot %v, want the kept reference 4 0 R", root)
	}
	notices := ctx.ValidationReport().Notices()
	if len(notices) != 1 {
		t.Fatalf("got %d notices, want 1: %v", len(notices), notices)
	}
	n := notices[0]
	if n.Path != "StructTreeRoot" || n.Disposition != model.NoticeSkipped {
		t.Fatalf("got path %q disposition %q", n.Path, n.Disposition)
	}
	const wantMessage = "StructTreeRoot: number tree StructTree Kids[0] obj#99: missing dict"
	if n.Message != wantMessage {
		t.Fatalf("got message %q, want %q", n.Message, wantMessage)
	}
	if !errors.Is(model.WithInvalidPDF(n.Cause, model.NoticePhaseValidate), model.ErrInvalidStructTree) {
		t.Fatalf("cause %v is not ErrInvalidStructTree", n.Cause)
	}
}

// TestKeepInvalidStructTree verifies KeepInvalidStructTree keeps an invalid tree in the catalog, with one skipped notice.
func TestKeepInvalidStructTree(t *testing.T) {
	ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(structTreeMissingKidsPDF()), structTreeKeepConfiguration())
	if err != nil {
		t.Fatalf("got %v, want no error", err)
	}
	requireKeptMissingKidsTree(t, ctx)
}

// TestKeepInvalidStructTreePrecedence verifies KeepInvalidStructTree takes precedence over RemoveInvalidStructTree.
func TestKeepInvalidStructTreePrecedence(t *testing.T) {
	conf := structTreeKeepConfiguration()
	conf.RemoveInvalidStructTree = true
	ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(structTreeMissingKidsPDF()), conf)
	if err != nil {
		t.Fatalf("got %v, want no error", err)
	}
	requireKeptMissingKidsTree(t, ctx)
}

// TestKeepInvalidStructTreeShapes verifies that each kept invalid tree is written unchanged, and that writing, merging,
// extracting, splitting and reading page boundaries succeed with it.
func TestKeepInvalidStructTreeShapes(t *testing.T) {
	const (
		catalogObj = "<< /Type /Catalog /Pages 2 0 R /StructTreeRoot 4 0 R >>"
		pagesObj   = "<< /Type /Pages /Kids [3 0 R] /Count 1 >>"
		pageObj    = "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /StructParents 0 >>"
	)
	shapes := []struct {
		name string
		data []byte
	}{
		{"missing kid", failureTestStructTreePDF(3, "<< /Kids [99 0 R] >>")},
		{"nums not an array", failureTestStructTreePDF(3, "<< /Nums 7 >>")},
		{"cycle and bad parent tree", structTreeChainPDF(2, false, false, true, "<< /Kids [99 0 R] >>", "")},
		{"deep chain", structTreeDeepPDF(150, "<< /Type /StructElem /S /P /P 4 0 R /Pg 3 0 R /A 5 >>")},
		{"deep chain with a non-name type", structTreeDeepPDF(1000, "<< /Type 5 /S /P /P 4 0 R >>")},
		{"root not a dict", failureTestPDF(catalogObj, pagesObj, pageObj, "5")},
		{"wrong root type", failureTestPDF(catalogObj, pagesObj, pageObj, "<< /Type /Foo /K 5 >>")},
		{"string element", failureTestPDF(catalogObj, pagesObj, pageObj,
			"<< /Type /StructTreeRoot /K [6 0 R] /ParentTree 5 0 R /RoleMap 7 >>",
			"<< /Nums [0 [6 0 R]] >>",
			"<< /Type /StructElem /S /P /P 4 0 R /Pg 3 0 R /K [7 0 R] >>",
			"<< /Type /StructElem /S 7 /P 6 0 R /Pg 3 0 R /K (x) >>")},
	}
	valid := failureTestValidPDF()
	for _, sh := range shapes {
		t.Run(sh.name, func(t *testing.T) {
			ctx, report, err := ReadAndValidateWithReport(context.Background(), bytes.NewReader(sh.data), nil)
			if err != nil {
				t.Fatalf("relaxed read: %v", err)
			}
			if !report.StructTreeInvalid || report.StructTreeRemoved {
				t.Fatalf("StructTreeInvalid %t, StructTreeRemoved %t, want true and false", report.StructTreeInvalid, report.StructTreeRemoved)
			}
			if _, found := ctx.RootDict.Find("StructTreeRoot"); !found {
				t.Fatal("StructTreeRoot removed, want it kept")
			}

			var out bytes.Buffer
			if err := WriteContext(context.Background(), ctx, &out); err != nil {
				t.Fatalf("write: %v", err)
			}
			reread, rereadReport, err := ReadAndValidateWithReport(context.Background(), bytes.NewReader(out.Bytes()), nil)
			if err != nil {
				t.Fatalf("tolerant re-read: %v", err)
			}
			if !rereadReport.StructTreeInvalid || reread.PageCount != 1 {
				t.Fatalf("re-read: StructTreeInvalid %t, page count %d, want true and 1", rereadReport.StructTreeInvalid, reread.PageCount)
			}
			if _, found := reread.RootDict.Find("StructTreeRoot"); !found {
				t.Fatal("re-read: StructTreeRoot not kept")
			}
			// The written tree is unchanged, so a default read still rejects it.
			if _, err := ReadAndValidate(context.Background(), bytes.NewReader(out.Bytes()), model.NewDefaultConfiguration()); !errors.Is(err, ErrInvalidStructTree) {
				t.Fatalf("default read of written output: got %v, want ErrInvalidStructTree", err)
			}

			for pos, inputs := range [][][]byte{{sh.data, valid}, {valid, sh.data}} {
				var merged bytes.Buffer
				mergeReport, err := MergeRawWithReport(context.Background(), mergeTestReaders(inputs...), &merged, nil, MergeOptions{})
				if err != nil {
					t.Fatalf("merge with the shape at %d: %v", pos, err)
				}
				if !mergeReport.Inputs[pos].StructTreeInvalid {
					t.Fatalf("merge: input %d StructTreeInvalid is false", pos)
				}
				mergedCtx, _, err := ReadAndValidateWithReport(context.Background(), bytes.NewReader(merged.Bytes()), nil)
				if err != nil {
					t.Fatalf("merge: tolerant re-read: %v", err)
				}
				if mergedCtx.PageCount != 2 {
					t.Fatalf("merge: page count %d, want 2", mergedCtx.PageCount)
				}
				_, kept := mergedCtx.RootDict.Find("StructTreeRoot")
				if kept != (pos == 0) {
					t.Fatalf("merge: StructTreeRoot kept %t at input %d, want %t", kept, pos, pos == 0)
				}
			}

			var pages [][]byte
			err = ExtractPagesContext(context.Background(), ctx, nil, func(r io.Reader, _ int) error {
				b, err := io.ReadAll(r)
				if err != nil {
					return err
				}
				pages = append(pages, b)
				return nil
			})
			if err != nil {
				t.Fatalf("extract: %v", err)
			}
			for _, page := range pages {
				if _, _, err := ReadAndValidateWithReport(context.Background(), bytes.NewReader(page), nil); err != nil {
					t.Fatalf("extracted page: %v", err)
				}
			}

			if _, err := SplitRaw(context.Background(), bytes.NewReader(sh.data), 1, model.NewTolerantConfiguration()); err != nil {
				t.Fatalf("split: %v", err)
			}

			pbs, err := ReadPageBoundaries(context.Background(), bytes.NewReader(sh.data), nil, nil)
			if err != nil {
				t.Fatalf("page boundaries: %v", err)
			}
			want, err := ctx.XRefTable.PageBoundaries(context.Background(), nil)
			if err != nil {
				t.Fatalf("validated page boundaries: %v", err)
			}
			if len(pbs) != 1 || !reflect.DeepEqual(pbs, want) {
				t.Fatalf("page boundaries %v, want %v", pbs, want)
			}
		})
	}
}

// structTreeObjStmPDF builds a struct tree whose elements 6 and 7 live in object stream 8.
func structTreeObjStmPDF(elem7, parentTree string) []byte {
	elem6 := "<< /Type /StructElem /S /P /P 4 0 R /Pg 3 0 R /K [7 0 R] >>"
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n%\xFF\xFF\xFF\xFF\n")
	offsets := map[int]int{}
	object := func(nr int, s string) {
		offsets[nr] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", nr, s)
	}
	object(1, "<< /Type /Catalog /Pages 2 0 R /StructTreeRoot 4 0 R >>")
	object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /StructParents 0 >>")
	object(4, "<< /Type /StructTreeRoot /K [6 0 R] /ParentTree 5 0 R >>")
	object(5, parentTree)
	prolog := fmt.Sprintf("6 0 7 %d ", len(elem6)+1)
	content := prolog + elem6 + " " + elem7 + " "
	object(8, fmt.Sprintf("<< /Type /ObjStm /N 2 /First %d /Length %d >>\nstream\n%s\nendstream", len(prolog), len(content), content))
	var rows []byte
	row := func(typ byte, f2, f3 int) {
		rows = append(rows, typ, byte(f2>>24), byte(f2>>16), byte(f2>>8), byte(f2), byte(f3>>8), byte(f3))
	}
	row(0, 0, 65535)
	for i := 1; i <= 5; i++ {
		row(1, offsets[i], 0)
	}
	row(2, 8, 0)
	row(2, 8, 1)
	row(1, offsets[8], 0)
	xref := b.Len()
	row(1, xref, 0)
	fmt.Fprintf(&b, "9 0 obj\n<< /Type /XRef /Size 10 /W [1 4 2] /Root 1 0 R /Length %d >>\nstream\n", len(rows))
	b.Write(rows)
	fmt.Fprintf(&b, "\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", xref)
	return b.Bytes()
}

// TestKeepInvalidStructTreeUnreadableObject verifies a tree with an object the writer cannot read is removed with a
// repaired notice, and that a tree of the same layout with readable objects is kept.
func TestKeepInvalidStructTreeUnreadableObject(t *testing.T) {
	ctx, report, err := ReadAndValidateWithReport(context.Background(),
		bytes.NewReader(structTreeObjStmPDF("<< /Length 5 >>", "<< /Nums [0 [6 0 R]] >>")), nil)
	if err != nil {
		t.Fatalf("got %v, want no error", err)
	}
	if !report.StructTreeInvalid || !report.StructTreeRemoved {
		t.Fatalf("StructTreeInvalid %t, StructTreeRemoved %t, want both true", report.StructTreeInvalid, report.StructTreeRemoved)
	}
	if _, found := ctx.RootDict.Find("StructTreeRoot"); found {
		t.Fatal("StructTreeRoot kept, want it removed")
	}
	notices := ctx.ValidationReport().Notices()
	if len(notices) != 1 || notices[0].Path != "StructTreeRoot" || notices[0].Disposition != model.NoticeRepaired ||
		!strings.Contains(notices[0].Message, "object stream contains stream object") {
		t.Fatalf("got notices %v, want one repaired StructTreeRoot notice about the stream object", notices)
	}
	var out bytes.Buffer
	if err := WriteContext(context.Background(), ctx, &out); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Control: the same layout with readable objects keeps the tree, so the test cannot pass by always removing it.
	ctx, report, err = ReadAndValidateWithReport(context.Background(),
		bytes.NewReader(structTreeObjStmPDF("<< /Type /StructElem /S /P /P 6 0 R /Pg 3 0 R >>", "<< /Kids [99 0 R] >>")), nil)
	if err != nil {
		t.Fatalf("control: got %v, want no error", err)
	}
	if !report.StructTreeInvalid || report.StructTreeRemoved || ctx.XRefTable.StructTreeRemoved {
		t.Fatalf("control: StructTreeInvalid %t, StructTreeRemoved %t, want true and false", report.StructTreeInvalid, report.StructTreeRemoved)
	}
	if err := WriteContext(context.Background(), ctx, &out); err != nil {
		t.Fatalf("control: write: %v", err)
	}
}

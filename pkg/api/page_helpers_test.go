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
	"regexp"
	"runtime"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// pageHelpersTreePDF builds a three page PDF whose boxes and rotation are inherited through two page tree levels.
// Page 1 inherits the root's MediaBox, page 2 inherits a CropBox and a rotation from an intermediate node, and
// page 3 has its own MediaBox and CropBox. The page with its own boxes comes last because the page tree walk
// of XRefTable.PageBoundaries passes boxes a node declares on to its later siblings.
func pageHelpersTreePDF() []byte {
	return failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R 6 0 R] /Count 3 /MediaBox [0 0 595 842] /Rotate 90 >>",
		"<< /Type /Page /Parent 2 0 R >>",
		"<< /Type /Pages /Parent 2 0 R /Kids [5 0 R] /Count 1 /CropBox [20 20 500 800] /Rotate 180 >>",
		"<< /Type /Page /Parent 4 0 R /TrimBox [30 30 400 700] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 400] /CropBox [10 10 290 390] >>",
	)
}

// pageHelpersNamedDestsPDF builds a three page PDF with one named destination per page.
func pageHelpersNamedDestsPDF() []byte {
	return failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R /Names << /Dests 6 0 R >> >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 /MediaBox [0 0 595 842] >>",
		"<< /Type /Page /Parent 2 0 R >>",
		"<< /Type /Page /Parent 2 0 R /CropBox [10 10 100 100] >>",
		"<< /Type /Page /Parent 2 0 R /Rotate 270 >>",
		"<< /Names [(a) [3 0 R /Fit] (b) [4 0 R /Fit] (c) [5 0 R /Fit]] >>",
	)
}

// pageHelpersRect returns the rectangle of b, or nil for a nil box.
func pageHelpersRect(b *model.Box) *types.Rectangle {
	if b == nil {
		return nil
	}
	return b.Rect
}

// TestReadPageBoundariesInheritance pins inherited MediaBox, CropBox and Rotate and a nil CropBox,
// and that the values equal Boxes on the validated PDF.
func TestReadPageBoundariesInheritance(t *testing.T) {
	data := pageHelpersTreePDF()
	pbs, err := ReadPageBoundaries(context.Background(), bytes.NewReader(data), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pbs) != 3 {
		t.Fatalf("got %d pages, want 3", len(pbs))
	}
	want := []struct {
		media, crop, trim *types.Rectangle
		mediaInherited    bool
		rot               int
	}{
		{types.NewRectangle(0, 0, 595, 842), nil, nil, true, 90},
		{types.NewRectangle(0, 0, 595, 842), types.NewRectangle(20, 20, 500, 800), types.NewRectangle(30, 30, 400, 700), true, 180},
		{types.NewRectangle(0, 0, 300, 400), types.NewRectangle(10, 10, 290, 390), nil, false, 90},
	}
	for i, w := range want {
		pb := pbs[i]
		if !reflect.DeepEqual(pageHelpersRect(pb.Media), w.media) || pb.Media.Inherited != w.mediaInherited {
			t.Errorf("page %d: MediaBox %v inherited %t, want %v inherited %t", i+1, pb.Media.Rect, pb.Media.Inherited, w.media, w.mediaInherited)
		}
		if !reflect.DeepEqual(pageHelpersRect(pb.Crop), w.crop) {
			t.Errorf("page %d: CropBox %v, want %v", i+1, pageHelpersRect(pb.Crop), w.crop)
		}
		if !reflect.DeepEqual(pageHelpersRect(pb.Trim), w.trim) {
			t.Errorf("page %d: TrimBox %v, want %v", i+1, pageHelpersRect(pb.Trim), w.trim)
		}
		if pb.Rot != w.rot {
			t.Errorf("page %d: Rotate %d, want %d", i+1, pb.Rot, w.rot)
		}
	}

	boxes, err := Boxes(context.Background(), bytes.NewReader(data), nil, model.NewDefaultConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pbs, boxes) {
		t.Errorf("ReadPageBoundaries %+v, Boxes %+v", pbs, boxes)
	}
}

// TestReadPageBoundariesSelection pins Boxes' selection semantics: one entry per page, unselected entries zero.
func TestReadPageBoundariesSelection(t *testing.T) {
	pbs, err := ReadPageBoundaries(context.Background(), bytes.NewReader(pageHelpersTreePDF()), []string{"3"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pbs) != 3 || pbs[0].Media != nil || pbs[1].Media != nil || pbs[2].Media == nil {
		t.Fatalf("got %+v, want only page 3 of 3", pbs)
	}
}

// TestReadPageBoundariesHugePage pins that an oversized MediaBox is reported, not rejected.
func TestReadPageBoundariesHugePage(t *testing.T) {
	data := failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 14400000 2000000000] >>",
	)
	pbs, err := ReadPageBoundaries(context.Background(), bytes.NewReader(data), []string{"1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if w, h := pbs[0].MediaBox().Width(), pbs[0].MediaBox().Height(); w != 14400000 || h != 2000000000 {
		t.Fatalf("got %gx%g, want 14400000x2000000000", w, h)
	}
}

// TestReadPageBoundariesSkipsStructTree pins that a structure tree ReadAndValidate rejects is never looked at.
func TestReadPageBoundariesSkipsStructTree(t *testing.T) {
	data := failureTestStructTreePDF(3, "<< /Kids [99 0 R] >>")
	conf := model.NewDefaultConfiguration()
	if _, err := ReadAndValidate(context.Background(), bytes.NewReader(data), conf); !errors.Is(err, ErrInvalidStructTree) {
		t.Fatalf("ReadAndValidate: got %v, want ErrInvalidStructTree", err)
	}
	pbs, err := ReadPageBoundaries(context.Background(), bytes.NewReader(data), []string{"1"}, conf)
	if err != nil {
		t.Fatal(err)
	}
	if len(pbs) != 1 || pbs[0].MediaBox().Width() != 595 {
		t.Fatalf("got %+v, want one A4 page", pbs)
	}
}

// TestReadPageBoundariesHostileCount pins that the declared Count neither sizes the result nor its allocation.
func TestReadPageBoundariesHostileCount(t *testing.T) {
	data := failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 2147483647 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>",
	)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	pbs, err := ReadPageBoundaries(context.Background(), bytes.NewReader(data), []string{"1"}, nil)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if len(pbs) != 1 {
		t.Fatalf("got %d pages, want 1", len(pbs))
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 64<<20 {
		t.Fatalf("allocated %d bytes, want at most 64 MiB", allocated)
	}
}

// TestReadPageBoundariesInvalidPageTree pins ErrInvalidPDF for page trees a walk without repairs cannot use.
func TestReadPageBoundariesInvalidPageTree(t *testing.T) {
	deep := []string{"<< /Type /Catalog /Pages 2 0 R >>"}
	for i := 2; i < 12; i++ {
		deep = append(deep, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", i+1))
	}
	deep = append(deep, "<< /Type /Page /MediaBox [0 0 595 842] >>")
	shallow := model.NewTolerantConfiguration()
	shallow.Limits.MaxRecursionDepth = 5

	for _, tc := range []struct {
		name   string
		data   []byte
		conf   *model.Configuration
		target error
	}{
		{"cycle", failureTestPDF(
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R 2 0 R] /Count 2 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>",
		), nil, model.ErrPageTreeCycle},
		{"duplicate node", failureTestPDF(
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R 3 0 R] /Count 2 >>",
			"<< /Type /Pages /Kids [4 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 3 0 R /MediaBox [0 0 595 842] >>",
		), nil, model.ErrPageTreeDuplicate},
		{"depth", failureTestPDF(deep...), shallow, model.ErrMaxRecursionDepthExceeded},
		{"more pages than objects", failureTestPDF(
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R 3 0 R 3 0 R 3 0 R 3 0 R] /Count 5 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>",
		), nil, nil},
		{"null kid", failureTestPDF(
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R null] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>",
		), nil, nil},
		{"short MediaBox", failureTestPDF(
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0] >>",
		), nil, nil},
		{"missing MediaBox everywhere", failureTestPDF(
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 /CropBox [0 0 100 100] >>",
			"<< /Type /Page /Parent 2 0 R >>",
		), nil, nil},
		{"missing Pages", failureTestPDF("<< /Type /Catalog >>"), nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pbs, err := ReadPageBoundaries(context.Background(), bytes.NewReader(tc.data), nil, tc.conf)
			var invalidErr *InvalidPDFError
			if !errors.As(err, &invalidErr) {
				t.Fatalf("got %v (%T), want *InvalidPDFError", err, err)
			}
			if tc.target != nil && !errors.Is(err, tc.target) {
				t.Errorf("got %v, want %v", err, tc.target)
			}
			if pbs != nil {
				t.Errorf("got %d boundaries with an error", len(pbs))
			}
		})
	}
}

// TestReadPageBoundariesErrors pins the classification of errors that are not caused by the page tree.
func TestReadPageBoundariesErrors(t *testing.T) {
	cause := errors.New("caller gave up")
	c := failureTestCanceledContext(cause)
	if _, err := ReadPageBoundaries(c, bytes.NewReader(pageHelpersTreePDF()), nil, nil); !errors.Is(err, context.Canceled) || context.Cause(c) != cause {
		t.Errorf("cancelled: got %v", err)
	}
	if _, err := ReadPageBoundaries(context.Background(), bytes.NewReader(failureTestEncryptedPDF(t)), nil, nil); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("encrypted: got %v, want ErrWrongPassword", err)
	}
	if _, err := ReadPageBoundaries(context.Background(), nil, nil, nil); !errors.Is(err, ErrMissingPDFReadSeeker) {
		t.Errorf("nil reader: got %v", err)
	}
	if _, err := ReadPageBoundaries(context.Background(), bytes.NewReader([]byte("%PDF-1.7\nnot a PDF body\n")), nil, nil); !errors.Is(err, ErrInvalidPDF) {
		t.Errorf("garbage: got %v, want ErrInvalidPDF", err)
	}
	conf := model.NewTolerantConfiguration()
	conf.Limits.MaxObjectNumber = 2
	if _, err := ReadPageBoundaries(context.Background(), bytes.NewReader(pageHelpersTreePDF()), nil, conf); !errors.Is(err, model.ErrResourceLimit) || errors.Is(err, ErrInvalidPDF) {
		t.Errorf("limit: got %v, want ErrResourceLimit only", err)
	}
}

// TestReadPageBoundariesNilConf pins that a nil conf reads with the tolerant limits and that a
// default configuration does not: object 8388608 is above the tolerant MaxObjectNumber only.
func TestReadPageBoundariesNilConf(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n%\xFF\xFF\xFF\xFF\n")
	bodies := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>",
	}
	offsets := make([]int, len(bodies))
	for i, body := range bodies {
		offsets[i] = appendValidationTestObject(&buf, i+1, body)
	}
	const highObjNr = 8_388_608
	highOffset := appendValidationTestObject(&buf, highObjNr, "<< /Type /Unused >>")
	xrefOffset := buf.Len()
	buf.WriteString("xref\n0 4\n0000000000 65535 f \n")
	for _, offset := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&buf, "%d 1\n%010d 00000 n \n", highObjNr, highOffset)
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", highObjNr+1, xrefOffset)
	data := buf.Bytes()

	pbs, err := ReadPageBoundaries(context.Background(), bytes.NewReader(data), nil, nil)
	if !errors.Is(err, model.ErrResourceLimit) || errors.Is(err, ErrInvalidPDF) {
		t.Errorf("nil conf: got %v, want ErrResourceLimit only", err)
	}
	if pbs != nil {
		t.Errorf("nil conf: got %d boundaries with an error", len(pbs))
	}

	pbs, err = ReadPageBoundaries(context.Background(), bytes.NewReader(data), nil, model.NewDefaultConfiguration())
	if err != nil {
		t.Fatalf("default conf: %v", err)
	}
	if len(pbs) != 1 || !reflect.DeepEqual(pageHelpersRect(pbs[0].Media), types.NewRectangle(0, 0, 595, 842)) {
		t.Errorf("default conf: got %+v, want one page with MediaBox 0 0 595 842", pbs)
	}
}

// TestReadPageBoundariesDeadlineIsNeverInvalidPDF stops the read and the page tree walk at every checkpoint.
func TestReadPageBoundariesDeadlineIsNeverInvalidPDF(t *testing.T) {
	data := pageHelpersTreePDF()
	for checks := 0; ; checks++ {
		c := &deadlineAfterChecksContext{Context: context.Background(), checks: checks}
		_, err := ReadPageBoundaries(c, bytes.NewReader(data), nil, nil)
		if err == nil {
			if checks == 0 {
				t.Fatal("no checkpoint")
			}
			return
		}
		if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrInvalidPDF) {
			t.Fatalf("checks %d: got %v, want DeadlineExceeded only", checks, err)
		}
	}
}

// pageHelpersPanickingReader panics on the first Read.
type pageHelpersPanickingReader struct{ *bytes.Reader }

// Read implements io.Reader.
func (r *pageHelpersPanickingReader) Read([]byte) (int, error) { panic("secret panic text") }

// TestReadPageBoundariesPanic pins that a panic becomes a *PanicError.
func TestReadPageBoundariesPanic(t *testing.T) {
	pbs, err := ReadPageBoundaries(context.Background(), &pageHelpersPanickingReader{bytes.NewReader(pageHelpersTreePDF())}, nil, nil)
	var panicErr *PanicError
	if !errors.As(err, &panicErr) || panicErr.Value() != "secret panic text" || pbs != nil {
		t.Fatalf("got %v, %v", pbs, err)
	}
}

var (
	pageHelpersIDPattern   = regexp.MustCompile(`/ID\[<[0-9A-F]+> <[0-9A-F]+>\]`)
	pageHelpersDatePattern = regexp.MustCompile(`\(D:[0-9]{14}[^)]*\)`)
)

// pageHelpersStable removes the file identifier and the dates the writer derives from the clock.
func pageHelpersStable(b []byte) []byte {
	b = pageHelpersIDPattern.ReplaceAll(b, []byte("/ID[]"))
	return pageHelpersDatePattern.ReplaceAll(b, []byte("(D:)"))
}

// pageHelpersRead returns the validated context of data.
func pageHelpersRead(t *testing.T, data []byte) *model.Context {
	t.Helper()
	ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(data), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

// TestExtractPagesContextMatchesExtractPage pins that the output equals a loop over ExtractPage on the
// same context, byte for byte apart from the clock-derived ID and dates, including the order.
// The inputs have no resources, so the writer's object numbering is deterministic.
func TestExtractPagesContextMatchesExtractPage(t *testing.T) {
	for _, data := range [][]byte{pageHelpersTreePDF(), pageHelpersNamedDestsPDF(), failureTestValidPDF()} {
		var got [][]byte
		var gotPages []int
		ctx := pageHelpersRead(t, data)
		err := ExtractPagesContext(context.Background(), ctx, nil, func(r io.Reader, pageNr int) error {
			b, err := io.ReadAll(r)
			got = append(got, b)
			gotPages = append(gotPages, pageNr)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}

		loopCtx := pageHelpersRead(t, data)
		if len(got) != loopCtx.PageCount {
			t.Fatalf("got %d pages, want %d", len(got), loopCtx.PageCount)
		}
		for i := range loopCtx.PageCount {
			r, err := ExtractPage(context.Background(), loopCtx, i+1)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := io.ReadAll(r)
			if gotPages[i] != i+1 || !bytes.Equal(pageHelpersStable(got[i]), pageHelpersStable(want)) {
				t.Errorf("page %d (%d): output differs from ExtractPage", i+1, gotPages[i])
			}
		}
	}
}

// TestExtractPagesContextSelection pins ExtractPages' selection semantics and ascending order.
func TestExtractPagesContextSelection(t *testing.T) {
	data := pageHelpersNamedDestsPDF()
	for _, sel := range [][]string{{"3", "1"}, {"2-"}, {"!2"}, {"9"}} {
		var got, want []int
		if err := ExtractPagesContext(context.Background(), pageHelpersRead(t, data), sel, func(_ io.Reader, pageNr int) error {
			got = append(got, pageNr)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := ExtractPages(context.Background(), bytes.NewReader(data), sel, func(_ io.Reader, pageNr int) error {
			want = append(want, pageNr)
			return nil
		}, nil); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("selection %v: got %v, want %v", sel, got, want)
		}
	}
}

// TestExtractPagesContextStops pins that a digest error, a cancellation and a panic stop the extraction.
func TestExtractPagesContextStops(t *testing.T) {
	digestErr := errors.New("storage full")
	var calls []int
	err := ExtractPagesContext(context.Background(), pageHelpersRead(t, pageHelpersTreePDF()), nil, func(_ io.Reader, pageNr int) error {
		calls = append(calls, pageNr)
		if pageNr == 2 {
			return digestErr
		}
		return nil
	})
	if !errors.Is(err, digestErr) || !reflect.DeepEqual(calls, []int{1, 2}) {
		t.Errorf("digest error: got %v after %v", err, calls)
	}

	c, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("caller gave up")
	calls = nil
	err = ExtractPagesContext(c, pageHelpersRead(t, pageHelpersTreePDF()), nil, func(_ io.Reader, pageNr int) error {
		calls = append(calls, pageNr)
		cancel(cause)
		return nil
	})
	if !errors.Is(err, context.Canceled) || context.Cause(c) != cause || !reflect.DeepEqual(calls, []int{1}) {
		t.Errorf("cancel: got %v after %v", err, calls)
	}

	err = ExtractPagesContext(context.Background(), pageHelpersRead(t, pageHelpersTreePDF()), nil, func(io.Reader, int) error {
		panic("secret panic text")
	})
	var panicErr *PanicError
	if !errors.As(err, &panicErr) || panicErr.Value() != "secret panic text" {
		t.Errorf("panic: got %v", err)
	}

	if err := ExtractPagesContext(context.Background(), nil, nil, func(io.Reader, int) error { return nil }); !errors.Is(err, ErrMissingPDFContext) {
		t.Errorf("nil context: got %v", err)
	}
	if err := ExtractPagesContext(context.Background(), pageHelpersRead(t, failureTestValidPDF()), nil, nil); !errors.Is(err, ErrMissingDigestFunction) {
		t.Errorf("nil digest: got %v", err)
	}
}

// TestExtractPagesContextReuse pins that the context stays usable for later extractions and a write.
func TestExtractPagesContextReuse(t *testing.T) {
	ctx := pageHelpersRead(t, pageHelpersNamedDestsPDF())
	for range 2 {
		n := 0
		if err := ExtractPagesContext(context.Background(), ctx, nil, func(io.Reader, int) error { n++; return nil }); err != nil || n != 3 {
			t.Fatalf("got %v after %d pages", err, n)
		}
	}
	var out bytes.Buffer
	if err := WriteContext(context.Background(), ctx, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAndValidate(context.Background(), bytes.NewReader(out.Bytes()), nil); err != nil {
		t.Fatal(err)
	}
}

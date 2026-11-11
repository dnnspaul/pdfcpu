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
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// isoMaxObjectNumber is the largest object number ISO 32000-1 Annex C allows.
const isoMaxObjectNumber = 8_388_607

// bug1980958Shape has the shape of pdf.js bug1980958.pdf: one object numbered 2^31-1 and no xref.
const bug1980958Shape = "%PDF-1.4\n2147483647 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 2147483647 0 R >>\n%%EOF\n"

type limitTestObject struct {
	nr   int
	body string
}

// limitTestCatalog returns the catalog and page tree objects every fixture starts with.
func limitTestCatalog() []limitTestObject {
	return []limitTestObject{
		{1, "<< /Type /Catalog /Pages 2 0 R >>"},
		{2, "<< /Type /Pages /Kids [] /Count 0 >>"},
	}
}

// classicLimitPDF writes objs with a classic xref table of one subsection per object.
// A non-negative subsectionStart replaces the start of the last subsection.
func classicLimitPDF(objs []limitTestObject, size, subsectionStart int) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", o.nr, o.body)
	}
	xref := b.Len()
	b.WriteString("xref\n0 1\n0000000000 65535 f \n")
	for i, o := range objs {
		start := o.nr
		if i == len(objs)-1 && subsectionStart >= 0 {
			start = subsectionStart
		}
		fmt.Fprintf(&b, "%d 1\n%010d 00000 n \n", start, offsets[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", size, xref)
	return b.Bytes()
}

// subsectionLimitPDF writes objs with a classic xref table and one subsection with the given
// header line, followed by an in-use entry line for each object index in entries.
func subsectionLimitPDF(objs []limitTestObject, size int, header string, entries []int) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", o.nr, o.body)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 1\n0000000000 65535 f \n%s\n", header)
	for _, i := range entries {
		fmt.Fprintf(&b, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", size, xref)
	return b.Bytes()
}

// xrefStreamLimitPDF writes objs and a cross-reference stream numbered streamNr with the given
// /Size and /Index. rows holds one {type, field2, field3} row per indexed object; a row with type 1
// and field2 < 0 points at the object with number -field2.
func xrefStreamLimitPDF(objs []limitTestObject, streamNr, size int, index string, rows [][3]int) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.5\n")
	offsets := map[int]int{}
	for _, o := range objs {
		offsets[o.nr] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", o.nr, o.body)
	}
	xref := b.Len()
	var data []byte
	for _, r := range rows {
		f2 := r[1]
		if r[0] == 1 && f2 < 0 {
			f2 = offsets[-f2]
		}
		data = append(data, byte(r[0]), byte(f2>>24), byte(f2>>16), byte(f2>>8), byte(f2), byte(r[2]>>8), byte(r[2]))
	}
	fmt.Fprintf(&b, "%d 0 obj\n<< /Type /XRef /Size %d /Index [%s] /W [1 4 2] /Root 1 0 R /Length %d >>\nstream\n", streamNr, size, index, len(data))
	b.Write(data)
	fmt.Fprintf(&b, "\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", xref)
	return b.Bytes()
}

// objectStreamLimitPDF stores the page tree, object 2, in object stream 3. The prolog of
// object stream 3 declares prologNr for it and the xref entry of object 2 names objStmNr
// as its object stream.
func objectStreamLimitPDF(prologNr, objStmNr int) []byte {
	pages := "<< /Type /Pages /Kids [] /Count 0 >>"
	prolog := fmt.Sprintf("%d 0 ", prologNr)
	content := prolog + pages
	objs := []limitTestObject{
		{1, "<< /Type /Catalog /Pages 2 0 R >>"},
		{3, fmt.Sprintf("<< /Type /ObjStm /N 1 /First %d /Length %d >>\nstream\n%s\nendstream", len(prolog), len(content), content)},
	}
	rows := [][3]int{{0, 0, 65535}, {1, -1, 0}, {2, objStmNr, 0}, {1, -3, 0}}
	return xrefStreamLimitPDF(objs, 99, 4, "0 4", rows)
}

func objectNumberLimitConf(mode, maxObjectNumber int) *model.Configuration {
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = mode
	conf.Limits.MaxObjectNumber = maxObjectNumber
	return conf
}

// TestReadObjectNumberLimit checks that MaxObjectNumber rejects each place an object number or
// a cross-reference /Size enters the reader with a ResourceLimitError, in strict and relaxed
// mode, that numbers at the limit and byte sizes named /Size pass, and that a zero limit keeps
// the outcome the reader has without the limit.
func TestReadObjectNumberLimit(t *testing.T) {
	atLimit := []limitTestObject{{isoMaxObjectNumber, "<< >>"}}
	overLimit := []limitTestObject{{isoMaxObjectNumber + 1, "<< >>"}}
	attachment := limitTestObject{3, "<< /Type /EmbeddedFile /Params << /Size 9437184 >> /Length 4 >>\nstream\nabcd\nendstream"}
	threeRows := [][3]int{{0, 0, 65535}, {1, -1, 0}, {1, -2, 0}}
	const strict, relaxed = model.ValidationStrict, model.ValidationRelaxed

	for _, tc := range []struct {
		name  string
		pdf   []byte
		value int64 // Value of the ResourceLimitError; 0 if the limit must not trip.
		// zeroFails lists the modes in which the PDF fails to read without the limit.
		zeroFails []int
	}{
		{"bug1980958 shape", []byte(bug1980958Shape), 2147483647, nil},
		{"object at the limit", classicLimitPDF(append(limitTestCatalog(), atLimit...), isoMaxObjectNumber+1, -1), 0, nil},
		{"object over the limit", classicLimitPDF(append(limitTestCatalog(), overLimit...), isoMaxObjectNumber+2, -1), isoMaxObjectNumber + 1, nil},
		{"huge trailer size", classicLimitPDF(limitTestCatalog(), 2147483647, -1), 2147483646, []int{strict}},
		{"huge xref subsection start", classicLimitPDF(limitTestCatalog(), 3, 2147483647), 2147483647, []int{strict, relaxed}},
		{"embedded file params size", classicLimitPDF(append(limitTestCatalog(), attachment), 4, -1), 0, nil},
		{"huge xref stream size", xrefStreamLimitPDF(limitTestCatalog(), 99, 2147483647, "0 3", threeRows), 2147483646, nil},
		{"huge xref stream index", xrefStreamLimitPDF(limitTestCatalog(), 99, 3, "0 3 2147483647 1", append(threeRows, [3]int{1, -1, 0})), 2147483647, nil},
		{"huge xref stream object number", xrefStreamLimitPDF(limitTestCatalog(), 2147483647, 3, "0 3", threeRows), 2147483647, nil},
		{"xref stream object at the limit", xrefStreamLimitPDF(append(limitTestCatalog(), atLimit...), 99, isoMaxObjectNumber+1,
			fmt.Sprintf("0 3 %d 1", isoMaxObjectNumber), append(threeRows, [3]int{1, -isoMaxObjectNumber, 0})), 0, nil},
		{"mixed xref subsection range", subsectionLimitPDF(limitTestCatalog(), isoMaxObjectNumber+1, "8388607 2", []int{0, 1}), isoMaxObjectNumber + 1, nil},
		{"xref subsection range without entries", subsectionLimitPDF(limitTestCatalog(), isoMaxObjectNumber+1, "8388607 2", nil), isoMaxObjectNumber + 1, []int{strict, relaxed}},
		{"xref entry past subsection count", subsectionLimitPDF(limitTestCatalog(), isoMaxObjectNumber+1, "8388607 1", []int{0, 0}), isoMaxObjectNumber + 1, nil},
		{"object stream object number at the limit", objectStreamLimitPDF(isoMaxObjectNumber, 3), 0, nil},
		{"huge object stream object number", objectStreamLimitPDF(2147483647, 3), 2147483647, nil},
		{"huge object stream reference", objectStreamLimitPDF(2, 2147483647), 2147483647, []int{strict, relaxed}},
	} {
		for _, mode := range []int{strict, relaxed} {
			t.Run(fmt.Sprintf("%s/mode=%d", tc.name, mode), func(t *testing.T) {
				_, err := Read(t.Context(), bytes.NewReader(tc.pdf), objectNumberLimitConf(mode, isoMaxObjectNumber))
				if tc.value == 0 {
					if err != nil {
						t.Fatalf("with limit: %v", err)
					}
				} else {
					var limitErr *model.ResourceLimitError
					if !errors.As(err, &limitErr) {
						t.Fatalf("with limit: got %v, want ResourceLimitError", err)
					}
					want := model.ResourceLimitError{Kind: model.LimitObjectNumber, Limit: isoMaxObjectNumber, Value: tc.value}
					if *limitErr != want {
						t.Fatalf("with limit: got %+v, want %+v", *limitErr, want)
					}
					if !errors.Is(err, model.ErrResourceLimit) || errors.Is(err, model.ErrInvalidPDF) {
						t.Fatalf("with limit: %v must match ErrResourceLimit and not ErrInvalidPDF", err)
					}
				}

				_, err = Read(t.Context(), bytes.NewReader(tc.pdf), objectNumberLimitConf(mode, 0))
				if errors.Is(err, model.ErrResourceLimit) {
					t.Fatalf("without limit: %v", err)
				}
				if wantFail := slices.Contains(tc.zeroFails, mode); (err != nil) != wantFail {
					t.Fatalf("without limit: got %v, want failure %t", err, wantFail)
				}
			})
		}
	}
}

// TestReadObjectNumberLimitRepairScan checks that the repair scan of object headers returns a
// ResourceLimitError for a header over the limit in relaxed mode, and reads the file without the limit.
func TestReadObjectNumberLimitRepairScan(t *testing.T) {
	pdf := []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n" +
		"2 0 obj\n<< /Type /Pages /Kids [] /Count 0 >>\nendobj\n" +
		"xref\n2147483647 0 obj << >> endobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n")
	_, err := Read(t.Context(), bytes.NewReader(pdf), objectNumberLimitConf(model.ValidationRelaxed, isoMaxObjectNumber))
	want := model.ResourceLimitError{Kind: model.LimitObjectNumber, Limit: isoMaxObjectNumber, Value: 2147483647}
	var limitErr *model.ResourceLimitError
	if !errors.As(err, &limitErr) || *limitErr != want {
		t.Fatalf("with limit: got %v, want %+v", err, want)
	}

	if _, err := Read(t.Context(), bytes.NewReader(pdf), objectNumberLimitConf(model.ValidationRelaxed, 0)); err != nil {
		t.Fatalf("without limit: %v", err)
	}
}

// TestReadObjectNumberLimitIsCheap checks that the limit stops the bug1980958 shape before
// the reader does real work.
func TestReadObjectNumberLimitIsCheap(t *testing.T) {
	pdf := []byte(bug1980958Shape)
	conf := objectNumberLimitConf(model.ValidationRelaxed, isoMaxObjectNumber)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	_, err := Read(t.Context(), bytes.NewReader(pdf), conf)
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, model.ErrResourceLimit) {
		t.Fatalf("got %v, want ErrResourceLimit", err)
	}
	// Measured: about 30 KiB and 50 µs; the bounds leave room for the race detector and slow runners.
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 1<<20 {
		t.Fatalf("allocated %d bytes, want at most 1 MiB", alloc)
	}
	if elapsed > time.Second {
		t.Fatalf("took %v, want at most 1s", elapsed)
	}
}

// TestRepairDoesNotSwallowObjectNumberLimit checks that the xref repair returns a limit error
// that triggered it, and that a relaxed repair scan does not skip an object over the limit as malformed.
func TestRepairDoesNotSwallowObjectNumberLimit(t *testing.T) {
	limitErr := &model.ResourceLimitError{Kind: model.LimitObjectNumber, Limit: 1, Value: 2}
	ctx, err := model.NewContext(bytes.NewReader(xrefRepairTestPDF(1)), model.NewStatelessConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	before := len(ctx.Table)
	if err := bypassXrefSection(t.Context(), ctx, 0, limitErr, 1); !errors.Is(err, limitErr) {
		t.Fatalf("got %v, want %v", err, limitErr)
	}
	if len(ctx.Table) != before {
		t.Fatalf("table has %d entries after a limit error, want %d", len(ctx.Table), before)
	}

	conf := objectNumberLimitConf(model.ValidationRelaxed, isoMaxObjectNumber)
	ctx, err = model.NewContext(bytes.NewReader([]byte(bug1980958Shape)), conf)
	if err != nil {
		t.Fatal(err)
	}
	var offset int64 = 9
	if _, _, err := processObject(t.Context(), ctx, "2147483647 0 obj", &offset, 1, nil); !errors.Is(err, model.ErrResourceLimit) {
		t.Fatalf("processObject: got %v, want ErrResourceLimit", err)
	}
}

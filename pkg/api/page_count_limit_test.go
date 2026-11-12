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
	"runtime"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// pageLimitPage is a page dict that passes strict validation.
const pageLimitPage = "<< /Type /Page /Parent 2 0 R /Resources << >> /MediaBox [0 0 595 842] >>"

// pageLimitRepeatedPDF builds a PDF whose page tree root lists the same page object pages times.
func pageLimitRepeatedPDF(pages int) []byte {
	kids := strings.TrimSpace(strings.Repeat("3 0 R ", pages))
	return failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, pages),
		pageLimitPage,
	)
}

// pageLimitObjStmPDF builds a PDF with pages distinct page objects, 3 to pages+2, stored in
// object stream pages+3 and listed in a cross-reference stream, the shape of an object stream page bomb.
func pageLimitObjStmPDF(pages int) []byte {
	var prolog, body bytes.Buffer
	kids := make([]string, pages)
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", i+3)
		fmt.Fprintf(&prolog, "%d %d ", i+3, body.Len())
		body.WriteString(pageLimitPage + " ")
	}
	objStmNr, xrefNr := pages+3, pages+4

	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n%\xFF\xFF\xFF\xFF\n")
	offsets := map[int]int{}
	object := func(nr int, s string) {
		offsets[nr] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", nr, s)
	}
	object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	object(2, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), pages))
	content := prolog.String() + body.String()
	object(objStmNr, fmt.Sprintf("<< /Type /ObjStm /N %d /First %d /Length %d >>\nstream\n%s\nendstream",
		pages, prolog.Len(), len(content), content))

	var rows []byte
	row := func(typ byte, f2, f3 int) {
		rows = append(rows, typ, byte(f2>>24), byte(f2>>16), byte(f2>>8), byte(f2), byte(f3>>8), byte(f3))
	}
	row(0, 0, 65535)
	row(1, offsets[1], 0)
	row(1, offsets[2], 0)
	for i := range pages {
		row(2, objStmNr, i)
	}
	row(1, offsets[objStmNr], 0)
	xref := b.Len()
	row(1, xref, 0)
	fmt.Fprintf(&b, "%d 0 obj\n<< /Type /XRef /Size %d /W [1 4 2] /Root 1 0 R /Length %d >>\nstream\n", xrefNr, xrefNr+1, len(rows))
	b.Write(rows)
	fmt.Fprintf(&b, "\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", xref)
	return b.Bytes()
}

// pageLimitConf returns the default configuration in mode with MaxPageCount maxPages.
func pageLimitConf(mode, maxPages int) *model.Configuration {
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = mode
	conf.Limits.MaxPageCount = maxPages
	return conf
}

// checkPageLimitError fails unless err is a LimitPages ResourceLimitError for one page past limit
// and not an ErrInvalidPDF.
func checkPageLimitError(t *testing.T, err error, limit int) {
	t.Helper()
	var limitErr *model.ResourceLimitError
	if !errors.As(err, &limitErr) || errors.Is(err, ErrInvalidPDF) {
		t.Fatalf("got %v (%T), want a ResourceLimitError only", err, err)
	}
	want := model.ResourceLimitError{Kind: model.LimitPages, Limit: int64(limit), Value: int64(limit) + 1}
	if *limitErr != want {
		t.Fatalf("got %+v, want %+v", *limitErr, want)
	}
}

// TestPageCountLimitValidation pins that validation stops one page past MaxPageCount in strict and
// relaxed mode, counts a repeated page each time, passes a page tree at the limit, and that a zero
// limit validates every page as before.
func TestPageCountLimitValidation(t *testing.T) {
	const limit = 100
	for _, shape := range []struct {
		name  string
		build func(int) []byte
	}{
		{"repeated page", pageLimitRepeatedPDF},
		{"object stream pages", pageLimitObjStmPDF},
	} {
		for _, mode := range []int{model.ValidationStrict, model.ValidationRelaxed} {
			t.Run(fmt.Sprintf("%s/mode %d", shape.name, mode), func(t *testing.T) {
				ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(shape.build(limit)), pageLimitConf(mode, limit))
				if err != nil || ctx.PageCount != limit {
					t.Fatalf("at the limit: got %v, %v pages", err, ctx)
				}
				_, err = ReadAndValidate(context.Background(), bytes.NewReader(shape.build(limit+1)), pageLimitConf(mode, limit))
				checkPageLimitError(t, err, limit)
				ctx, err = ReadAndValidate(context.Background(), bytes.NewReader(shape.build(limit+1)), pageLimitConf(mode, 0))
				if err != nil || ctx.PageCount != limit+1 {
					t.Fatalf("zero limit: got %v", err)
				}
			})
		}
	}
}

// TestPageCountLimitReadPageBoundaries pins that ReadPageBoundaries stops one page past MaxPageCount
// in strict and relaxed mode with a ResourceLimitError, and that a zero limit reads every page.
func TestPageCountLimitReadPageBoundaries(t *testing.T) {
	const limit = 100
	for _, mode := range []int{model.ValidationStrict, model.ValidationRelaxed} {
		t.Run(fmt.Sprintf("mode %d", mode), func(t *testing.T) {
			pbs, err := ReadPageBoundaries(context.Background(), bytes.NewReader(pageLimitObjStmPDF(limit)), nil, pageLimitConf(mode, limit))
			if err != nil || len(pbs) != limit {
				t.Fatalf("at the limit: got %v, %d pages", err, len(pbs))
			}
			_, err = ReadPageBoundaries(context.Background(), bytes.NewReader(pageLimitObjStmPDF(limit+1)), nil, pageLimitConf(mode, limit))
			checkPageLimitError(t, err, limit)
			if pbs, err = ReadPageBoundaries(context.Background(), bytes.NewReader(pageLimitObjStmPDF(limit+1)), nil, pageLimitConf(mode, 0)); err != nil || len(pbs) != limit+1 {
				t.Fatalf("zero limit: got %v, %d pages", err, len(pbs))
			}
		})
	}
}

// TestPageCountLimitBomb pins that a page bomb stops at the limit: validating 100,000 repeated
// pages with a limit of 100 allocates a small fraction of what validating all of them does.
func TestPageCountLimitBomb(t *testing.T) {
	const limit, pages = 100, 100_000
	data := pageLimitRepeatedPDF(pages)
	validate := func(maxPages int) (uint64, error) {
		ctx, err := ReadContext(context.Background(), bytes.NewReader(data), pageLimitConf(model.ValidationRelaxed, maxPages))
		if err != nil {
			t.Fatal(err)
		}
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		err = ValidateContext(context.Background(), ctx)
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc, err
	}
	limited, err := validate(limit)
	checkPageLimitError(t, err, limit)
	full, err := validate(0)
	if err != nil {
		t.Fatal(err)
	}
	if limited*20 > full {
		t.Fatalf("validation allocated %d bytes with the limit and %d without, want at most 5%%", limited, full)
	}
}

// TestPageCountLimitEnsurePageCount pins that a declared Count above MaxPageCount is a ResourceLimitError.
func TestPageCountLimitEnsurePageCount(t *testing.T) {
	ctx, err := ReadContext(context.Background(), bytes.NewReader(pageLimitRepeatedPDF(101)), pageLimitConf(model.ValidationRelaxed, 100))
	if err != nil {
		t.Fatal(err)
	}
	checkPageLimitError(t, ctx.EnsurePageCount(), 100)
}

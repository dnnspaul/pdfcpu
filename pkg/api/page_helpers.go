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
	"context"
	"fmt"
	"io"

	"github.com/pdfcpu/pdfcpu/internal/contextutil"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// ReadPageBoundaries reads rs and returns the page boundaries of the selected
// pages, without validating the PDF, and supports cancellation.
// A nil conf selects model.NewTolerantConfiguration(); a non-nil conf is used as is.
//
// The result is indexed like Boxes: entry i belongs to page i+1, and entries of
// pages that are not selected are zero. The page count is the number of pages
// reachable through the page tree; the declared Count entries are ignored.
// MediaBox, CropBox and Rotate are inherited through the page tree as in Boxes.
//
// Compared with ReadAndValidate, ReadPageBoundaries parses the input in the
// same way, with the same limits and cancellation checks, but skips
// validation: no catalog, structure tree, annotation, form, resource or
// content checks. In relaxed mode the page tree walk accepts some page tree
// defects that relaxed validation repairs, and counts pages as validation
// does: null entries in Kids, references to object 0, negative object numbers
// and references beyond conf.Limits.MaxObjectNumber are skipped, a reference to
// a missing, free or null object or to an empty dict is a blank page with the
// MediaBox that validation gives it or A4, a page listed more than once in Kids
// counts each time, and a null page boundary coordinate is 0. Without a
// MaxPageCount, relaxed mode keeps the cap of one page per cross-reference
// table entry, so a page tree with more pages fails with ErrInvalidPDF. In
// strict mode these defects fail with ErrInvalidPDF, except that a page
// repeated in Kids is accepted up to the number of objects in the PDF. More
// pages than conf.Limits.MaxPageCount fail with a *model.ResourceLimitError.
//
// Errors can be classified with errors.Is and errors.As as for
// ReadAndValidateWithReport: ErrWrongPassword, context errors with the
// caller's cause, *model.ResourceLimitError, *InvalidPDFError for unreadable
// input and broken page trees, and *PanicError for a recovered panic.
func ReadPageBoundaries(c context.Context, rs io.ReadSeeker, selectedPages []string, conf *model.Configuration) (pbs []model.PageBoundaries, err error) {
	defer func() {
		if err != nil {
			pbs = nil
		}
	}()
	defer catchPanic(&err)

	if err := contextutil.Check(c); err != nil {
		return nil, err
	}
	if rs == nil {
		return nil, ErrMissingPDFReadSeeker
	}
	if conf == nil {
		conf = model.NewTolerantConfiguration()
	}

	ctx, err := ReadContext(c, rs, conf)
	if err != nil {
		return nil, fmt.Errorf("read page boundaries: read context: %w", err)
	}

	pageCount, err := ctx.CountPageTreeLeaves(c)
	if err != nil {
		return nil, fmt.Errorf("read page boundaries: %w", pageTreeFailure(c, ctx, err))
	}
	// PageBoundaries sizes its result by PageCount, which nothing has set without validation.
	ctx.PageCount = pageCount

	pages, err := PagesForSelection(pageCount, selectedPages, true)
	if err != nil {
		return nil, fmt.Errorf("read page boundaries: parse page selection: %w", err)
	}
	if pbs, err = ctx.PageBoundaries(c, pages); err != nil {
		return nil, fmt.Errorf("read page boundaries: %w", pageTreeFailure(c, ctx, err))
	}
	return pbs, nil
}

// pageTreeFailure marks err, a failure to walk ctx's page tree, as an ErrInvalidPDF
// validation failure unless c is done or reading the input failed.
func pageTreeFailure(c context.Context, ctx *model.Context, err error) error {
	if c.Err() != nil || ctx.Read.ReaderErr() != nil {
		return err
	}
	return model.WithInvalidPDF(err, model.NoticePhaseValidate)
}

// ExtractPagesContext extracts the selected pages of ctx into single page PDFs,
// passes each to digestPage in ascending page order, and supports cancellation.
// ctx must be a validated context, e.g. from ReadAndValidateWithReport; it is
// not read again. Page selection and output are those of ExtractPages, and
// each page is extracted exactly as ExtractPage(c, ctx, pageNr) would.
//
// Extraction does not change the named destinations of ctx: every page keeps
// the named destinations that point to it, as when extracted from a fresh read.
//
// The first error of digestPage stops the extraction and is returned wrapped.
// digestPage is the caller's code and is not recovered: a panic in digestPage
// propagates to the caller unchanged and is not a *PanicError, so it does not
// make IsDeterministicFailure report the input as damaged. A panic in the
// extraction of pdfcpu is recovered and returned as *PanicError. ctx may be
// used by one goroutine at a time.
func ExtractPagesContext(c context.Context, ctx *model.Context, selectedPages []string, digestPage func(io.Reader, int) error) error {
	if err := contextutil.Check(c); err != nil {
		return err
	}
	if ctx == nil {
		return ErrMissingPDFContext
	}
	if digestPage == nil {
		return fmt.Errorf("extract pages: %w", ErrMissingDigestFunction)
	}

	pages, err := selectExtractPages(ctx, selectedPages)
	if err != nil {
		return err
	}

	for _, pageNr := range pages {
		if err := contextutil.Check(c); err != nil {
			return err
		}
		rd, err := extractPageRecovered(c, ctx, pageNr)
		if err != nil {
			return fmt.Errorf("extract pages: %w", err)
		}
		if err := digestPage(rd, pageNr); err != nil {
			return fmt.Errorf("extract pages: page %d: digest: %w", pageNr, err)
		}
	}

	return contextutil.Check(c)
}

// selectExtractPages returns the ascending page numbers selected for ExtractPagesContext.
// A panic is recovered and returned as *PanicError.
func selectExtractPages(ctx *model.Context, selectedPages []string) (pages []int, err error) {
	defer catchPanic(&err)

	selected, err := PagesForSelection(ctx.PageCount, selectedPages, true)
	if err != nil {
		return nil, fmt.Errorf("extract pages: parse page selection: %w", err)
	}
	return sortedPages(selected), nil
}

// extractPageFn is ExtractPage. Tests replace it to make the extraction panic.
var extractPageFn = ExtractPage

// extractPageRecovered extracts page pageNr of ctx like ExtractPage.
// A panic is recovered and returned as *PanicError. It does not cover the digest
// callback of ExtractPagesContext, which must not be recovered.
func extractPageRecovered(c context.Context, ctx *model.Context, pageNr int) (rd io.Reader, err error) {
	defer catchPanic(&err)

	return extractPageFn(c, ctx, pageNr)
}

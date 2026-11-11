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

package model

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"testing"
)

// TestWithInvalidPDFClassifiesByTypeNotText verifies classification ignores the error message.
func TestWithInvalidPDFClassifiesByTypeNotText(t *testing.T) {
	cause := errors.New("cause")
	marked := WithInvalidPDF(fmt.Errorf("any wording: %w", cause), NoticePhaseRead)
	if !errors.Is(marked, ErrInvalidPDF) {
		t.Fatalf("got %v, want ErrInvalidPDF", marked)
	}
	if marked.Error() != "any wording: cause" {
		t.Fatalf("got message %q", marked.Error())
	}
	if !errors.Is(marked, cause) {
		t.Fatalf("marked error does not wrap the cause")
	}
	var invalidErr *InvalidPDFError
	if !errors.As(marked, &invalidErr) || invalidErr.Phase() != NoticePhaseRead {
		t.Fatalf("got %v, want *InvalidPDFError with phase read", marked)
	}

	for _, err := range []error{
		errors.New("catalog: StructTreeRoot: missing Kids or Nums"),
		fmt.Errorf("validation error (obj#:1): catalog: StructTreeRoot: %w", cause),
	} {
		if errors.Is(err, ErrInvalidPDF) || errors.Is(err, ErrInvalidStructTree) {
			t.Fatalf("unmarked error %q classified by text", err)
		}
	}
}

// TestWithInvalidPDFLeavesOtherFailuresUnmarked verifies WithInvalidPDF returns non-input failures unchanged.
func TestWithInvalidPDFLeavesOtherFailuresUnmarked(t *testing.T) {
	marked := WithInvalidPDF(errors.New("already marked"), NoticePhaseValidate)
	pathErr := &fs.PathError{Op: "open", Path: "x.pdf", Err: errors.New("no such file")}
	limitErr := &ResourceLimitError{Kind: LimitNotices, Limit: 10, Value: 11}
	ctxErr := fmt.Errorf("catalog: %w", WithValidationErrorEntry(context.DeadlineExceeded, "StructTreeRoot"))

	for _, err := range []error{
		nil,
		context.Canceled,
		ctxErr,
		limitErr,
		pathErr,
		marked,
	} {
		if got := WithInvalidPDF(err, NoticePhaseRead); got != err {
			t.Fatalf("got %v, want %v unchanged", got, err)
		}
	}
}

// TestInvalidPDFErrorStructTreePath verifies struct tree failures expose the catalog entry and object number.
func TestInvalidPDFErrorStructTreePath(t *testing.T) {
	entry := WithValidationErrorEntry(errors.New("number tree StructTree: missing Kids or Nums"), "StructTreeRoot")
	if entry.Error() != "StructTreeRoot: number tree StructTree: missing Kids or Nums" {
		t.Fatalf("got message %q", entry.Error())
	}

	marked := WithInvalidPDF(WithValidationErrorObject(entry, 7), NoticePhaseValidate)
	if !errors.Is(marked, ErrInvalidPDF) || !errors.Is(marked, ErrInvalidStructTree) {
		t.Fatalf("got %v, want ErrInvalidPDF and ErrInvalidStructTree", marked)
	}
	var invalidErr *InvalidPDFError
	if !errors.As(marked, &invalidErr) {
		t.Fatalf("got %T, want *InvalidPDFError", marked)
	}
	if !slices.Equal(invalidErr.Path(), []string{"StructTreeRoot"}) {
		t.Fatalf("got path %v, want [StructTreeRoot]", invalidErr.Path())
	}
	if invalidErr.ObjectNumber() != 7 {
		t.Fatalf("got object number %d, want 7", invalidErr.ObjectNumber())
	}

	acroForm := WithInvalidPDF(WithValidationErrorEntry(errors.New("field"), "AcroForm"), NoticePhaseValidate)
	if !errors.Is(acroForm, ErrInvalidPDF) {
		t.Fatalf("got %v, want ErrInvalidPDF", acroForm)
	}
	if errors.Is(acroForm, ErrInvalidStructTree) {
		t.Fatalf("AcroForm failure classified as ErrInvalidStructTree")
	}
}

// TestResourceLimitError verifies limit failures match ErrResourceLimit and carry their details.
func TestResourceLimitError(t *testing.T) {
	err := &ResourceLimitError{Kind: LimitNotices, Limit: 10, Value: 11}
	if !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("got %v, want ErrResourceLimit", err)
	}
	if err.Error() != "validation notice limit 10 exceeded: 11" {
		t.Fatalf("got message %q", err.Error())
	}
	if errors.Is(err, ErrInvalidPDF) {
		t.Fatalf("resource limit error classified as ErrInvalidPDF")
	}

	cause := errors.New("limit cause")
	withCause := &ResourceLimitError{Kind: LimitObjectNumber, Limit: 1, Value: 2, Err: cause}
	if withCause.Error() != cause.Error() {
		t.Fatalf("got message %q, want %q", withCause.Error(), cause.Error())
	}
	if !errors.Is(withCause, cause) || !errors.Is(withCause, ErrResourceLimit) {
		t.Fatalf("got %v, want chain to cause and ErrResourceLimit", withCause)
	}
}

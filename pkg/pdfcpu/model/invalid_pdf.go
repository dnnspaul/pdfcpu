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
	"io/fs"
)

var (
	// ErrInvalidPDF signals that a PDF could not be read or failed validation.
	// Use errors.Is to identify it; errors.As with *InvalidPDFError yields details.
	ErrInvalidPDF = errors.New("invalid PDF")

	// ErrInvalidStructTree signals an ErrInvalidPDF failure below the document catalog's StructTreeRoot entry.
	ErrInvalidStructTree = errors.New("invalid structure tree")
)

// InvalidPDFError marks a read or validation failure caused by the PDF input.
// Its message is the message of the wrapped error.
type InvalidPDFError struct {
	phase NoticePhase
	err   error
}

// Error implements error.
func (e *InvalidPDFError) Error() string {
	return e.err.Error()
}

// Unwrap returns the underlying error.
func (e *InvalidPDFError) Unwrap() error {
	return e.err
}

// Is classifies this error as ErrInvalidPDF, and as ErrInvalidStructTree if the
// failure occurred below the document catalog's StructTreeRoot entry.
func (e *InvalidPDFError) Is(target error) bool {
	switch target {
	case ErrInvalidPDF:
		return true
	case ErrInvalidStructTree:
		path := e.Path()
		return len(path) > 0 && path[0] == "StructTreeRoot"
	}
	return false
}

// Phase returns the processing phase that failed: NoticePhaseRead or NoticePhaseValidate.
func (e *InvalidPDFError) Phase() NoticePhase {
	return e.phase
}

// ObjectNumber returns the indirect object attributed to the failure, or 0 if unknown.
func (e *InvalidPDFError) ObjectNumber() int {
	var validationErr *ValidationError
	if errors.As(e.err, &validationErr) {
		return validationErr.ObjectNumber()
	}
	return 0
}

// Path returns the dictionary entries the failure is attributed to, outermost first,
// starting with the document catalog entry, e.g. [StructTreeRoot].
// It is empty if the failure is not attributed to a catalog entry.
func (e *InvalidPDFError) Path() []string {
	return validationErrorPath(e.err)
}

// WithInvalidPDF marks err as an ErrInvalidPDF failure of phase.
// It returns err unchanged if err is nil, a context error, a resource limit error,
// a file system error or already marked.
func WithInvalidPDF(err error, phase NoticePhase) error {
	if err == nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrResourceLimit) {
		return err
	}
	var invalidErr *InvalidPDFError
	if errors.As(err, &invalidErr) {
		return err
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return err
	}
	return &InvalidPDFError{phase: phase, err: err}
}

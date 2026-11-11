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

import "errors"

// ValidationError attributes a validation failure to an indirect object.
type ValidationError struct {
	objNr int
	err   error
}

// Error implements error.
func (e *ValidationError) Error() string {
	return e.err.Error()
}

// ObjectNumber returns the indirect object attributed to the validation failure.
func (e *ValidationError) ObjectNumber() int {
	return e.objNr
}

// Unwrap returns the underlying validation error.
func (e *ValidationError) Unwrap() error {
	return e.err
}

// WithValidationErrorObject attributes err to objNr unless err already carries object attribution.
func WithValidationErrorObject(err error, objNr int) error {
	if err == nil || objNr <= 0 {
		return err
	}

	var validationErr *ValidationError
	if errors.As(err, &validationErr) {
		return err
	}

	return &ValidationError{objNr: objNr, err: err}
}

// validationEntryError attributes a validation failure to a named dictionary entry.
type validationEntryError struct {
	entry string
	err   error
}

// Error implements error.
func (e *validationEntryError) Error() string {
	return e.entry + ": " + e.err.Error()
}

// Unwrap returns the underlying validation error.
func (e *validationEntryError) Unwrap() error {
	return e.err
}

// WithValidationErrorEntry prefixes err with entry like fmt.Errorf("%s: %w", entry, err)
// and records entry for InvalidPDFError.Path.
func WithValidationErrorEntry(err error, entry string) error {
	if err == nil {
		return nil
	}
	return &validationEntryError{entry: entry, err: err}
}

// validationErrorPath returns the entries recorded by WithValidationErrorEntry on err's chain, outermost first.
func validationErrorPath(err error) []string {
	var path []string
	for ; err != nil; err = errors.Unwrap(err) {
		if e, ok := err.(*validationEntryError); ok {
			path = append(path, e.entry)
		}
	}
	return path
}

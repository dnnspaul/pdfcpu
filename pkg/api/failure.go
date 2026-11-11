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
	"errors"
	"runtime/debug"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/fault"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// PanicError reports a recovered panic.
// Its message does not include the panic value; use Value and Stack for diagnostics.
type PanicError struct {
	value any
	stack []byte
}

// Error implements error.
func (e *PanicError) Error() string {
	return "internal error: recovered from panic"
}

// Value returns the recovered panic value.
func (e *PanicError) Value() any {
	return e.value
}

// Stack returns the stack trace captured when the panic was recovered.
func (e *PanicError) Stack() []byte {
	return e.stack
}

// catchPanic recovers a panic and assigns it to err.
// Like fault.Catch it assigns a fault.Panic as is; any other panic becomes a *PanicError.
func catchPanic(err *error) {
	r := recover()
	if r == nil {
		return
	}
	if p, ok := r.(fault.Panic); ok {
		*err = p
		return
	}
	*err = &PanicError{value: r, stack: debug.Stack()}
}

// IsDeterministicFailure reports whether err will recur for the same input and configuration:
// a *PanicError, an ErrResourceLimit or an ErrInvalidPDF failure.
// It reports false for context errors and for missing or wrong passwords.
func IsDeterministicFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var panicErr *PanicError
	return errors.As(err, &panicErr) ||
		errors.Is(err, model.ErrResourceLimit) ||
		errors.Is(err, model.ErrInvalidPDF)
}

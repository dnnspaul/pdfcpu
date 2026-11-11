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
	"io"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// ReadReport describes how a PDF was read and validated: by
// ReadAndValidateWithReport, or as one input of MergeRawWithReport.
type ReadReport struct {
	// Encrypted reports that the input is encrypted. It was opened with the
	// configured passwords, by default the empty user password.
	Encrypted bool

	// StructTreeRemoved reports that an invalid structure tree was removed from
	// the in-memory catalog, see model.Configuration.RemoveInvalidStructTree.
	StructTreeRemoved bool

	// Notices is the validation report of the input as it was read and
	// validated. ReadAndValidateWithReport returns it with the context it
	// returns; MergeRawWithReport returns it per input, as no context is returned.
	Notices model.ValidationReport
}

// ReadAndValidateWithReport reads and validates rs like ReadAndValidate and
// reports how the PDF was accepted. A nil conf selects
// model.NewTolerantConfiguration(); a non-nil conf is used as is.
//
// Errors can be classified with errors.Is and errors.As:
//   - ErrWrongPassword: the input needs a user password that conf does not supply.
//   - context.Canceled or context.DeadlineExceeded: c was done; context.Cause(c)
//     is unchanged.
//   - *model.ResourceLimitError (ErrResourceLimit): a configured limit was exceeded.
//   - *InvalidPDFError (ErrInvalidPDF): the input is not a PDF that conf accepts.
//   - *PanicError: a panic was recovered. Error does not include the panic
//     value. Errors pdfcpu raises itself with fault.Fail, such as a
//     configuration problem, are returned as they are.
//
// On error the context is nil and the report is zero. The returned context and
// report may be used by one goroutine at a time; different contexts may be
// processed concurrently.
func ReadAndValidateWithReport(c context.Context, rs io.ReadSeeker, conf *model.Configuration) (ctx *model.Context, report ReadReport, err error) {
	defer func() {
		if err != nil {
			ctx, report = nil, ReadReport{}
		}
	}()
	defer catchPanic(&err)

	if conf == nil {
		conf = model.NewTolerantConfiguration()
	}
	if ctx, err = ReadAndValidate(c, rs, conf); err != nil {
		return nil, ReadReport{}, err
	}
	return ctx, newReadReport(ctx), nil
}

// newReadReport returns the read report of the validated context ctx.
func newReadReport(ctx *model.Context) ReadReport {
	return ReadReport{
		Encrypted:         ctx.XRefTable.Encrypt != nil,
		StructTreeRemoved: ctx.XRefTable.StructTreeRemoved,
		Notices:           ctx.ValidationReport(),
	}
}

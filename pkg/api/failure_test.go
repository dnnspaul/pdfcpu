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
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/fault"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// failureTestPDF builds a PDF from objects numbered 1..n with a classic xref table.
func failureTestPDF(objects ...string) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n%\xFF\xFF\xFF\xFF\n")
	offsets := make([]int, len(objects))
	for i, body := range objects {
		offsets[i] = appendValidationTestObject(&buf, i+1, body)
	}
	xrefOffset := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for _, offset := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefOffset)
	return buf.Bytes()
}

// failureTestValidPDF builds a PDF with one empty page.
func failureTestValidPDF() []byte {
	return failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>",
	)
}

// failureTestStructTreePDF builds a PDF whose StructTreeRoot has the given number of structure elements
// and the given ParentTree object.
func failureTestStructTreePDF(elements int, parentTree string) []byte {
	kids := make([]string, elements)
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R /StructTreeRoot 4 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>",
	}
	for i := range elements {
		kids[i] = fmt.Sprintf("%d 0 R", 6+i)
	}
	objects = append(objects,
		fmt.Sprintf("<< /Type /StructTreeRoot /K [%s] /ParentTree 5 0 R >>", strings.Join(kids, " ")),
		parentTree,
	)
	for range elements {
		objects = append(objects, "<< /Type /StructElem /S /P /P 4 0 R /Pg 3 0 R >>")
	}
	return failureTestPDF(objects...)
}

// failureTestEncryptedPDF builds an AES-256 encrypted copy of the valid test PDF.
func failureTestEncryptedPDF(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	conf := model.NewAESConfiguration("user", "owner", 256)
	if err := Encrypt(context.Background(), bytes.NewReader(failureTestValidPDF()), &out, conf); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// failureTestCanceledContext returns a context cancelled with cause.
func failureTestCanceledContext(cause error) context.Context {
	c, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	return c
}

// deadlineAfterChecksContext reports DeadlineExceeded once checks calls to Err have been made.
type deadlineAfterChecksContext struct {
	context.Context
	checks int
}

// Err implements context.Context.
func (c *deadlineAfterChecksContext) Err() error {
	if c.checks <= 0 {
		return context.DeadlineExceeded
	}
	c.checks--
	return nil
}

// recoverWithCatchPanic runs f and returns the error catchPanic assigns for a panic in f.
func recoverWithCatchPanic(f func()) (err error) {
	defer catchPanic(&err)
	f()
	return nil
}

// TestReadAndValidateFailureClassification verifies read and validation failures match the expected sentinels.
func TestReadAndValidateFailureClassification(t *testing.T) {
	cause := errors.New("caller stopped")
	cases := []struct {
		name          string
		input         []byte
		ctx           context.Context
		invalid       bool
		phase         model.NoticePhase
		structTree    bool
		wrongPassword bool
		canceled      bool
	}{
		{name: "valid", input: failureTestValidPDF(), ctx: context.Background()},
		{name: "empty input", input: []byte{}, ctx: context.Background(), invalid: true, phase: model.NoticePhaseRead},
		{name: "garbage body", input: []byte("%PDF-1.7\nnot a PDF body\n"), ctx: context.Background(), invalid: true, phase: model.NoticePhaseRead},
		{name: "validation failure", input: invalidValidationTestPDF(), ctx: context.Background(), invalid: true, phase: model.NoticePhaseValidate},
		{
			name:       "struct tree failure",
			input:      failureTestStructTreePDF(1, "<< /Nums 7 >>"),
			ctx:        context.Background(),
			invalid:    true,
			phase:      model.NoticePhaseValidate,
			structTree: true,
		},
		{name: "encrypted without password", input: failureTestEncryptedPDF(t), ctx: context.Background(), wrongPassword: true},
		{name: "cancelled context", input: failureTestValidPDF(), ctx: failureTestCanceledContext(cause), canceled: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadAndValidate(tc.ctx, bytes.NewReader(tc.input), model.NewDefaultConfiguration())
			if !tc.invalid && !tc.wrongPassword && !tc.canceled {
				if err != nil {
					t.Fatalf("got %v, want no error", err)
				}
				return
			}
			if err == nil {
				t.Fatal("got no error")
			}
			if got := errors.Is(err, ErrInvalidPDF); got != tc.invalid {
				t.Fatalf("ErrInvalidPDF: got %t, want %t (err: %v)", got, tc.invalid, err)
			}
			if got := errors.Is(err, ErrInvalidStructTree); got != tc.structTree {
				t.Fatalf("ErrInvalidStructTree: got %t, want %t (err: %v)", got, tc.structTree, err)
			}
			if got := errors.Is(err, ErrWrongPassword); got != tc.wrongPassword {
				t.Fatalf("ErrWrongPassword: got %t, want %t (err: %v)", got, tc.wrongPassword, err)
			}
			if got := errors.Is(err, context.Canceled); got != tc.canceled {
				t.Fatalf("context.Canceled: got %t, want %t (err: %v)", got, tc.canceled, err)
			}
			if tc.canceled && context.Cause(tc.ctx) != cause {
				t.Fatalf("got cause %v, want %v", context.Cause(tc.ctx), cause)
			}
			if got := IsDeterministicFailure(err); got != tc.invalid {
				t.Fatalf("IsDeterministicFailure: got %t, want %t", got, tc.invalid)
			}
			if tc.phase != "" {
				var invalidErr *InvalidPDFError
				if !errors.As(err, &invalidErr) {
					t.Fatalf("got %T, want *InvalidPDFError", err)
				}
				if invalidErr.Phase() != tc.phase {
					t.Fatalf("got phase %q, want %q", invalidErr.Phase(), tc.phase)
				}
			}
		})
	}
}

// TestMergeRawEncryptedInputIsNotInvalidPDF verifies encryption policy errors are not reported as invalid input.
func TestMergeRawEncryptedInputIsNotInvalidPDF(t *testing.T) {
	inputs := []io.ReadSeeker{
		bytes.NewReader(failureTestValidPDF()),
		bytes.NewReader(failureTestEncryptedPDF(t)),
	}
	var out bytes.Buffer
	err := MergeRaw(context.Background(), inputs, &out, false, model.NewDefaultConfiguration())
	if !errors.Is(err, pdfcpu.ErrEncrypted) {
		t.Fatalf("got %v, want ErrEncrypted", err)
	}
	if errors.Is(err, ErrInvalidPDF) {
		t.Fatalf("encrypted input classified as ErrInvalidPDF: %v", err)
	}
	if IsDeterministicFailure(err) {
		t.Fatalf("encrypted input classified as deterministic: %v", err)
	}
}

// TestReadAndValidateStructTreeFailureDetails verifies a struct tree failure names the catalog entry and object.
func TestReadAndValidateStructTreeFailureDetails(t *testing.T) {
	_, err := ReadAndValidate(context.Background(),
		bytes.NewReader(failureTestStructTreePDF(1, "<< /Nums 7 >>")), model.NewDefaultConfiguration())
	var invalidErr *InvalidPDFError
	if !errors.As(err, &invalidErr) {
		t.Fatalf("got %v, want *InvalidPDFError", err)
	}
	if got := strings.Join(invalidErr.Path(), "/"); got != "StructTreeRoot" {
		t.Fatalf("got path %q, want StructTreeRoot", got)
	}
	if invalidErr.ObjectNumber() != 5 {
		t.Fatalf("got object number %d, want 5", invalidErr.ObjectNumber())
	}
}

// TestReadAndValidateDeadlineIsNeverInvalidPDF verifies a deadline hit at any point is never classified as invalid input.
func TestReadAndValidateDeadlineIsNeverInvalidPDF(t *testing.T) {
	data := failureTestStructTreePDF(50, "<< /Nums [] >>")
	structTreeMessages := 0
	for checks := 0; ; checks++ {
		if checks > 100000 {
			t.Fatal("validation did not complete within 100000 context checks")
		}
		c := &deadlineAfterChecksContext{Context: context.Background(), checks: checks}
		_, err := ReadAndValidate(c, bytes.NewReader(data), model.NewDefaultConfiguration())
		if err == nil {
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

// failureTestPDFWithURILink builds a PDF whose page has a link annotation to uri.
func failureTestPDFWithURILink(uri string) []byte {
	return failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> /Annots [4 0 R] >>",
		fmt.Sprintf("<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /A << /S /URI /URI (%s) >> >>", uri),
	)
}

// TestExemptedFailuresAreNotInvalidPDF verifies failures exempted from ErrInvalidPDF stay unmarked through the API.
func TestExemptedFailuresAreNotInvalidPDF(t *testing.T) {
	// The link policy rejects loopback addresses before any connection is made.
	const loopbackURL = "http://127.0.0.1/"

	cases := []struct {
		name     string
		run      func(t *testing.T) error
		want     error
		wantText string
	}{
		{
			name: "not encrypted",
			run: func(t *testing.T) error {
				return Decrypt(context.Background(), bytes.NewReader(failureTestValidPDF()), io.Discard,
					model.NewDefaultConfiguration())
			},
			want: pdfcpu.ErrNotEncrypted,
		},
		{
			name: "owner password required",
			run: func(t *testing.T) error {
				return Encrypt(context.Background(), bytes.NewReader(failureTestValidPDF()), io.Discard,
					model.NewAESConfiguration("user", "", 256))
			},
			want: pdfcpu.ErrOwnerPasswordRequired,
		},
		{
			name: "permission denied",
			run: func(t *testing.T) error {
				var encrypted bytes.Buffer
				conf := model.NewAESConfiguration("user", "owner", 256)
				conf.Permissions = model.PermissionsNone
				if err := Encrypt(context.Background(), bytes.NewReader(failureTestValidPDF()), &encrypted, conf); err != nil {
					t.Fatal(err)
				}
				dec := model.NewDefaultConfiguration()
				dec.UserPW = "user"
				digest := func(io.Reader, int) error { return nil }
				return ExtractPages(context.Background(), bytes.NewReader(encrypted.Bytes()), []string{"1"}, digest, dec)
			},
			want: pdfcpu.ErrPermissionDenied,
		},
		{
			name: "link verification",
			run: func(t *testing.T) error {
				conf := model.NewDefaultConfiguration()
				conf.ValidateLinks = true
				conf.ValidationMode = model.ValidationStrict
				_, err := ReadAndValidate(context.Background(), bytes.NewReader(failureTestPDFWithURILink(loopbackURL)), conf)
				return err
			},
			wantText: "link verification failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(t)
			if err == nil {
				t.Fatal("got no error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if tc.wantText != "" && !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("got %v, want text %q", err, tc.wantText)
			}
			if errors.Is(err, ErrInvalidPDF) {
				t.Fatalf("classified as ErrInvalidPDF: %v", err)
			}
			if IsDeterministicFailure(err) {
				t.Fatalf("classified as deterministic: %v", err)
			}
		})
	}
}

// failingReader serves data and returns err for every Read at or after offset failAt.
type failingReader struct {
	data   []byte
	failAt int64
	err    error
	off    int64
}

// Read implements io.Reader.
func (r *failingReader) Read(p []byte) (int, error) {
	if r.off >= r.failAt {
		return 0, r.err
	}
	if r.off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	end := min(int64(len(r.data)), r.failAt)
	n := copy(p, r.data[r.off:end])
	r.off += int64(n)
	return n, nil
}

// Seek implements io.Seeker.
func (r *failingReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		r.off = offset
	case io.SeekCurrent:
		r.off += offset
	case io.SeekEnd:
		r.off = int64(len(r.data)) + offset
	}
	if r.off < 0 {
		return 0, errors.New("negative position")
	}
	return r.off, nil
}

// TestReadAndValidateReaderFailureIsNotInvalidPDF verifies an I/O error of the input reader is not reported as invalid input.
func TestReadAndValidateReaderFailureIsNotInvalidPDF(t *testing.T) {
	data := failureTestStructTreePDF(1, "<< /Nums 7 >>")
	readErr := errors.New("connection reset by peer")
	_, err := ReadAndValidate(context.Background(),
		&failingReader{data: data, failAt: int64(len(data) / 2), err: readErr}, model.NewDefaultConfiguration())
	if err == nil {
		t.Fatal("got no error")
	}
	if !errors.Is(err, readErr) {
		t.Fatalf("got %v, want reader error", err)
	}
	if errors.Is(err, ErrInvalidPDF) || errors.Is(err, ErrInvalidStructTree) {
		t.Fatalf("reader failure classified as invalid input: %v", err)
	}
	if IsDeterministicFailure(err) {
		t.Fatalf("reader failure classified as deterministic: %v", err)
	}
}

// badRangeReader serves data, but every Read that starts inside [lo, hi) fails with err.
type badRangeReader struct {
	*bytes.Reader
	lo, hi int64
	err    error
}

// Read implements io.Reader.
func (r *badRangeReader) Read(p []byte) (int, error) {
	off := r.Size() - int64(r.Len())
	if off >= r.lo && off < r.hi {
		return 0, r.err
	}
	if off < r.lo {
		p = p[:min(int64(len(p)), r.lo-off)]
	}
	return r.Reader.Read(p)
}

// TestReadAndValidateRelaxedReaderFailureIsNotInvalidPDF verifies a reader failure that relaxed reading skips,
// so that Read succeeds and validation fails, is not reported as invalid input.
func TestReadAndValidateRelaxedReaderFailureIsNotInvalidPDF(t *testing.T) {
	data := failureTestStructTreePDF(3, "<< /Nums [] >>")
	readErr := errors.New("connection reset by peer")
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	_, want := ReadAndValidate(context.Background(), bytes.NewReader(data), conf)
	skipped := 0
	for lo := range len(data) {
		r := &badRangeReader{Reader: bytes.NewReader(data), lo: int64(lo), hi: int64(lo) + 8, err: readErr}
		_, err := ReadAndValidate(context.Background(), r, conf)
		if err == nil || sameErrorText(err, want) {
			continue
		}
		if errors.Is(err, ErrInvalidPDF) || IsDeterministicFailure(err) {
			t.Fatalf("offset %d: reader failure classified as invalid input: %v", lo, err)
		}
		if !errors.Is(err, readErr) {
			skipped++
		}
	}
	if skipped == 0 {
		t.Fatal("relaxed reading never skipped a failed read into a validation failure")
	}
}

// seekFailingReader serves data, but every seek to a non-negative offset from the start at or after failAt fails with err.
type seekFailingReader struct {
	*bytes.Reader
	failAt int64
	err    error
}

// Seek implements io.Seeker.
func (r *seekFailingReader) Seek(offset int64, whence int) (int64, error) {
	if whence == io.SeekStart && offset >= r.failAt {
		return 0, r.err
	}
	return r.Reader.Seek(offset, whence)
}

// TestReadAndValidateSeekFailureIsNotInvalidPDF verifies a failing seek of the input reader is not reported as invalid input.
func TestReadAndValidateSeekFailureIsNotInvalidPDF(t *testing.T) {
	data := failureTestStructTreePDF(3, "<< /Nums [] >>")
	seekErr := errors.New("seek: connection reset by peer")
	failed := 0
	for _, mode := range []int{model.ValidationRelaxed, model.ValidationStrict} {
		conf := model.NewDefaultConfiguration()
		conf.ValidationMode = mode
		_, want := ReadAndValidate(context.Background(), bytes.NewReader(data), conf)
		for failAt := range len(data) + 1 {
			r := &seekFailingReader{Reader: bytes.NewReader(data), failAt: int64(failAt), err: seekErr}
			_, err := ReadAndValidate(context.Background(), r, conf)
			if err == nil || sameErrorText(err, want) {
				continue
			}
			if errors.Is(err, seekErr) {
				failed++
			}
			if errors.Is(err, ErrInvalidPDF) || IsDeterministicFailure(err) {
				t.Fatalf("mode %d, failAt %d: seek failure classified as invalid input: %v", mode, failAt, err)
			}
		}
	}
	if failed == 0 {
		t.Fatal("no seek failure reached the reader error")
	}
}

// sameErrorText reports whether err and want are both nil or have the same message.
func sameErrorText(err, want error) bool {
	if err == nil || want == nil {
		return err == want
	}
	return err.Error() == want.Error()
}

// cancelAfterChecksContext cancels its parent with cause once checks calls to Err have been made.
type cancelAfterChecksContext struct {
	context.Context
	cancel func(error)
	cause  error
	checks int
}

// Err implements context.Context.
func (c *cancelAfterChecksContext) Err() error {
	if c.checks <= 0 {
		c.cancel(c.cause)
	} else {
		c.checks--
	}
	return c.Context.Err()
}

// TestReadAndValidateCancelCauseIsNeverInvalidPDF verifies a cancellation with a custom cause at any point
// is never classified as invalid input and keeps its cause.
func TestReadAndValidateCancelCauseIsNeverInvalidPDF(t *testing.T) {
	cause := errors.New("caller stopped")
	data := failureTestStructTreePDF(50, "<< /Nums [] >>")
	structTreeMessages := 0
	for checks := 0; ; checks++ {
		if checks > 100000 {
			t.Fatal("validation did not complete within 100000 context checks")
		}
		parent, cancel := context.WithCancelCause(context.Background())
		c := &cancelAfterChecksContext{Context: parent, cancel: cancel, cause: cause, checks: checks}
		_, err := ReadAndValidate(c, bytes.NewReader(data), model.NewDefaultConfiguration())
		cancel(nil)
		if err == nil {
			break
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("checks %d: got %v, want Canceled", checks, err)
		}
		if got := context.Cause(parent); got != cause {
			t.Fatalf("checks %d: got cause %v, want %v", checks, got, cause)
		}
		if errors.Is(err, ErrInvalidPDF) || errors.Is(err, ErrInvalidStructTree) || IsDeterministicFailure(err) {
			t.Fatalf("checks %d: cancellation classified as invalid input: %v", checks, err)
		}
		if strings.Contains(err.Error(), "StructTreeRoot: ") {
			structTreeMessages++
		}
	}
	if structTreeMessages == 0 {
		t.Fatal("no cancellation was hit below StructTreeRoot")
	}
}

// TestCatchPanic verifies recovered panics keep their diagnostics out of the message.
func TestCatchPanic(t *testing.T) {
	err := recoverWithCatchPanic(func() { panic("secret panic text") })
	var panicErr *PanicError
	if !errors.As(err, &panicErr) {
		t.Fatalf("got %T, want *PanicError", err)
	}
	if strings.Contains(panicErr.Error(), "secret") {
		t.Fatalf("message leaks panic value: %q", panicErr.Error())
	}
	if panicErr.Value() != "secret panic text" {
		t.Fatalf("got value %v", panicErr.Value())
	}
	if !strings.Contains(string(panicErr.Stack()), "TestCatchPanic") {
		t.Fatalf("stack does not name TestCatchPanic:\n%s", panicErr.Stack())
	}
	if !IsDeterministicFailure(fmt.Errorf("wrapped: %w", err)) {
		t.Fatal("wrapped PanicError is not deterministic")
	}

	faultErr := errors.New("fault cause")
	err = recoverWithCatchPanic(func() { panic(fault.Panic{Err: faultErr}) })
	var faultPanic fault.Panic
	if !errors.As(err, &faultPanic) || faultPanic.Err != faultErr {
		t.Fatalf("got %v, want fault.Panic passed through", err)
	}
	if errors.As(err, &panicErr) {
		t.Fatal("fault.Panic was wrapped in PanicError")
	}

	if err := recoverWithCatchPanic(func() {}); err != nil {
		t.Fatalf("got %v, want nil without panic", err)
	}
}

// TestValidateCreatedContextWithoutPagesIsInvalidPDF verifies validating an invalid context built by
// CreateContextWithXRefTable fails with the message of earlier releases, marked ErrInvalidPDF.
// Such a context has no read context, so validation failures must not dereference one.
func TestValidateCreatedContextWithoutPagesIsInvalidPDF(t *testing.T) {
	ctx, err := pdfcpu.CreateContextWithXRefTable(model.NewDefaultConfiguration(), &types.Dim{Width: 595, Height: 842})
	if err != nil {
		t.Fatal(err)
	}
	rootDict, err := ctx.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	rootDict.Delete("Pages")

	err = ValidateContext(context.Background(), ctx)
	if err == nil {
		t.Fatal("got no error")
	}
	if want := `catalog: pages: page tree root: missing "Pages"`; err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, ErrInvalidPDF) {
		t.Fatalf("got %v, want ErrInvalidPDF", err)
	}
}

// TestIsDeterministicFailure verifies only panics, resource limits and invalid input are deterministic.
func TestIsDeterministicFailure(t *testing.T) {
	limitErr := &model.ResourceLimitError{Kind: model.LimitNotices, Limit: 10, Value: 11}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "other", err: errors.New("other"), want: false},
		{name: "deadline", err: context.DeadlineExceeded, want: false},
		{name: "wrong password", err: fmt.Errorf("wrapped: %w", pdfcpu.ErrWrongPassword), want: false},
		{name: "resource limit", err: fmt.Errorf("wrapped: %w", limitErr), want: true},
		{name: "invalid PDF", err: model.WithInvalidPDF(errors.New("bad"), model.NoticePhaseRead), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDeterministicFailure(tc.err); got != tc.want {
				t.Fatalf("got %t, want %t", got, tc.want)
			}
		})
	}
}

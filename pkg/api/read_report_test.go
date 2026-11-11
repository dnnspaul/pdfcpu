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
	"reflect"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// readReportBug1980958Shape has the shape of pdf.js bug1980958.pdf: one object numbered 2^31-1 and no xref.
const readReportBug1980958Shape = "%PDF-1.4\n2147483647 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 2147483647 0 R >>\n%%EOF\n"

// countingReadSeeker counts the bytes read from and the seeks on a bytes.Reader.
type countingReadSeeker struct {
	*bytes.Reader
	bytesRead int64
	seeks     int
}

// Read implements io.Reader.
func (r *countingReadSeeker) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytesRead += int64(n)
	return n, err
}

// Seek implements io.Seeker.
func (r *countingReadSeeker) Seek(offset int64, whence int) (int64, error) {
	r.seeks++
	return r.Reader.Seek(offset, whence)
}

// panickingReadSeeker panics with value on the first Read.
type panickingReadSeeker struct {
	*bytes.Reader
	value any
}

// Read implements io.Reader.
func (r *panickingReadSeeker) Read([]byte) (int, error) {
	panic(r.value)
}

// readReportOwnerOnlyPDF encrypts the valid test PDF with conf, which sets an owner password and an empty user password.
func readReportOwnerOnlyPDF(t *testing.T, conf *model.Configuration) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Encrypt(context.Background(), bytes.NewReader(failureTestValidPDF()), &out, conf); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// TestNewTolerantConfiguration verifies the tolerant configuration sets its policy and keeps the other defaults.
func TestNewTolerantConfiguration(t *testing.T) {
	conf := model.NewTolerantConfiguration()
	if conf.ValidationMode != model.ValidationRelaxed {
		t.Fatalf("got validation mode %v, want relaxed", conf.ValidationMode)
	}
	want := model.NewDefaultConfiguration().Limits
	want.MaxObjectNumber, want.MaxRetainedNotices, want.MaxNotices = 8_388_607, 1_000, 250_000
	if conf.Limits != want {
		t.Fatalf("got limits %+v, want %+v", conf.Limits, want)
	}
	if !conf.RemoveInvalidStructTree {
		t.Fatal("RemoveInvalidStructTree not set")
	}
	if model.NewDefaultConfiguration().RemoveInvalidStructTree {
		t.Fatal("default configuration removes invalid structure trees")
	}
	if model.NewTolerantConfiguration() == conf {
		t.Fatal("got the same configuration twice")
	}

	// Every other setting must equal the default configuration.
	wantConf := model.NewDefaultConfiguration()
	wantConf.ValidationMode = model.ValidationRelaxed
	wantConf.Limits = want
	wantConf.RemoveInvalidStructTree = true
	wantConf.CreationDate = conf.CreationDate
	if !reflect.DeepEqual(conf, wantConf) {
		t.Fatalf("got %#v, want %#v", conf, wantConf)
	}
}

// TestReadAndValidateWithReport verifies a nil conf reads with the tolerant configuration and reports its notices.
func TestReadAndValidateWithReport(t *testing.T) {
	ctx, report, err := ReadAndValidateWithReport(context.Background(), bytes.NewReader(noticeExplosionPDF(1, 3)), nil)
	if err != nil {
		t.Fatalf("got %v, want no error", err)
	}
	if ctx.Configuration.ValidationMode != model.ValidationRelaxed {
		t.Fatalf("got validation mode %v, want relaxed", ctx.Configuration.ValidationMode)
	}
	if ctx.Configuration.Limits.MaxNotices != 250_000 {
		t.Fatalf("got MaxNotices %d, want 250000", ctx.Configuration.Limits.MaxNotices)
	}
	if report.Encrypted || report.StructTreeRemoved {
		t.Fatalf("got encrypted %t and structTreeRemoved %t, want false and false", report.Encrypted, report.StructTreeRemoved)
	}
	if report.Notices.Len() == 0 {
		t.Fatal("no notices reported")
	}
	if !reflect.DeepEqual(report.Notices, ctx.ValidationReport()) {
		t.Fatal("report notices differ from the context's validation report")
	}
}

// TestReadAndValidateWithReportOneParse verifies that ReadAndValidateWithReport reads the input exactly as
// ReadContext does. Validation reads nothing from the input, so any extra bytes or seeks would be a second parse.
func TestReadAndValidateWithReportOneParse(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"struct tree", failureTestStructTreePDF(3, "<< /Nums [] >>")},
		{"notices", noticeExplosionPDF(2, 3)},
		{"encrypted", readReportOwnerOnlyPDF(t, model.NewAESConfiguration("", "owner", 256))},
		{"struct tree removed", failureTestStructTreePDF(3, "<< /Kids [99 0 R] >>")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			readOnly := &countingReadSeeker{Reader: bytes.NewReader(tc.data)}
			if _, err := ReadContext(context.Background(), readOnly, model.NewTolerantConfiguration()); err != nil {
				t.Fatalf("ReadContext: got %v, want no error", err)
			}
			withReport := &countingReadSeeker{Reader: bytes.NewReader(tc.data)}
			if _, _, err := ReadAndValidateWithReport(context.Background(), withReport, nil); err != nil {
				t.Fatalf("ReadAndValidateWithReport: got %v, want no error", err)
			}
			if withReport.bytesRead != readOnly.bytesRead || withReport.seeks != readOnly.seeks {
				t.Fatalf("got %d bytes and %d seeks, want %d and %d of one read",
					withReport.bytesRead, withReport.seeks, readOnly.bytesRead, readOnly.seeks)
			}
		})
	}
}

// TestReadAndValidateWithReportEncrypted verifies owner-only encryption opens with the empty user password and that
// a user password is reported as ErrWrongPassword.
func TestReadAndValidateWithReportEncrypted(t *testing.T) {
	cases := []struct {
		name string
		conf *model.Configuration
	}{
		{"AES-256", model.NewAESConfiguration("", "owner", 256)},
		{"AES-128", model.NewAESConfiguration("", "owner", 128)},
		{"RC4-128", model.NewRC4Configuration("", "owner", 128)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, report, err := ReadAndValidateWithReport(context.Background(), bytes.NewReader(readReportOwnerOnlyPDF(t, tc.conf)), nil)
			if err != nil {
				t.Fatalf("got %v, want no error", err)
			}
			if !report.Encrypted {
				t.Fatal("encryption not reported")
			}
			if ctx.PageCount != 1 {
				t.Fatalf("got %d pages, want 1", ctx.PageCount)
			}
			if _, err := ExtractPage(context.Background(), ctx, 1); err != nil {
				t.Fatalf("ExtractPage: got %v, want no error", err)
			}
		})
	}

	ctx, report, err := ReadAndValidateWithReport(context.Background(), bytes.NewReader(failureTestEncryptedPDF(t)), nil)
	if !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("got %v, want ErrWrongPassword", err)
	}
	if ctx != nil || !reflect.DeepEqual(report, ReadReport{}) {
		t.Fatal("got a context or report on error")
	}
	if errors.Is(err, ErrInvalidPDF) || IsDeterministicFailure(err) {
		t.Fatalf("got %v, want a wrong password error and not invalid input", err)
	}
}

// TestReadAndValidateWithReportPanic verifies a foreign panic is returned as *PanicError with its original frames.
func TestReadAndValidateWithReportPanic(t *testing.T) {
	for _, v := range []any{"secret panic text", errors.New("secret panic error")} {
		rs := &panickingReadSeeker{Reader: bytes.NewReader(failureTestValidPDF()), value: v}
		ctx, report, err := ReadAndValidateWithReport(context.Background(), rs, nil)
		var panicErr *PanicError
		if !errors.As(err, &panicErr) {
			t.Fatalf("got %v, want *PanicError", err)
		}
		if panicErr.Error() != "internal error: recovered from panic" {
			t.Fatalf("got error text %q, want the generic text", panicErr.Error())
		}
		if panicErr.Value() != v {
			t.Fatalf("got panic value %v, want %v", panicErr.Value(), v)
		}
		if !strings.Contains(string(panicErr.Stack()), "panickingReadSeeker).Read") {
			t.Fatal("stack does not contain the panicking Read frame")
		}
		if ctx != nil || !reflect.DeepEqual(report, ReadReport{}) {
			t.Fatal("got a context or report on error")
		}
		if !IsDeterministicFailure(err) {
			t.Fatal("panic not reported as deterministic")
		}
	}
}

// TestReadAndValidateWithReportErrors verifies the error classes of ReadAndValidateWithReport.
func TestReadAndValidateWithReportErrors(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		cause := errors.New("client gone")
		c := failureTestCanceledContext(cause)
		ctx, report, err := ReadAndValidateWithReport(c, bytes.NewReader(failureTestValidPDF()), nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
		if context.Cause(c) != cause {
			t.Fatal("cause not preserved")
		}
		if ctx != nil || !reflect.DeepEqual(report, ReadReport{}) {
			t.Fatal("got a context or report on error")
		}
		if IsDeterministicFailure(err) {
			t.Fatal("cancellation reported as deterministic")
		}
	})

	t.Run("object number limit", func(t *testing.T) {
		_, _, err := ReadAndValidateWithReport(context.Background(), strings.NewReader(readReportBug1980958Shape), nil)
		var limitErr *model.ResourceLimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("got %v, want *model.ResourceLimitError", err)
		}
		if limitErr.Kind != model.LimitObjectNumber || limitErr.Limit != 8_388_607 {
			t.Fatalf("got kind %v and limit %d, want object number and 8388607", limitErr.Kind, limitErr.Limit)
		}
		if errors.Is(err, ErrInvalidPDF) {
			t.Fatal("limit stop reported as invalid input")
		}
	})

	t.Run("garbage", func(t *testing.T) {
		_, _, err := ReadAndValidateWithReport(context.Background(), strings.NewReader("%PDF-1.7\nnot a PDF body\n"), nil)
		var invalid *InvalidPDFError
		if !errors.As(err, &invalid) {
			t.Fatalf("got %v, want *InvalidPDFError", err)
		}
	})

	t.Run("notice limit", func(t *testing.T) {
		conf := model.NewTolerantConfiguration()
		conf.Limits.MaxNotices = 2
		_, _, err := ReadAndValidateWithReport(context.Background(), bytes.NewReader(noticeExplosionPDF(1, 3)), conf)
		var limitErr *model.ResourceLimitError
		if !errors.As(err, &limitErr) || limitErr.Kind != model.LimitNotices {
			t.Fatalf("got %v, want a notices *model.ResourceLimitError", err)
		}
	})

	t.Run("nil reader", func(t *testing.T) {
		_, _, err := ReadAndValidateWithReport(context.Background(), nil, nil)
		if !errors.Is(err, ErrMissingPDFReadSeeker) {
			t.Fatalf("got %v, want ErrMissingPDFReadSeeker", err)
		}
	})
}

// TestReadAndValidateWithReportStructTreeRemoved verifies an invalid structure tree is removed and reported, and
// is an error when the configuration does not allow removal.
func TestReadAndValidateWithReportStructTreeRemoved(t *testing.T) {
	data := failureTestStructTreePDF(3, "<< /Kids [99 0 R] >>")
	ctx, report, err := ReadAndValidateWithReport(context.Background(), bytes.NewReader(data), nil)
	if err != nil {
		t.Fatalf("got %v, want no error", err)
	}
	if !report.StructTreeRemoved || !ctx.XRefTable.StructTreeRemoved {
		t.Fatalf("got report %t and context %t, want both true", report.StructTreeRemoved, ctx.XRefTable.StructTreeRemoved)
	}
	if _, ok := ctx.RootDict.Find("StructTreeRoot"); ok {
		t.Fatal("StructTreeRoot still in catalog")
	}
	if report.Notices.Len() != 1 {
		t.Fatalf("got %d notices, want 1", report.Notices.Len())
	}
	first, ok := report.Notices.First()
	if !ok || first.Path != "StructTreeRoot" || first.Disposition != model.NoticeRepaired {
		t.Fatalf("got notice %+v, want repaired StructTreeRoot", first)
	}
	if !reflect.DeepEqual(report.Notices, ctx.ValidationReport()) {
		t.Fatal("report notices differ from the context's validation report")
	}

	conf := model.NewTolerantConfiguration()
	conf.RemoveInvalidStructTree = false
	if _, _, err := ReadAndValidateWithReport(context.Background(), bytes.NewReader(data), conf); !errors.Is(err, ErrInvalidStructTree) {
		t.Fatalf("got %v, want ErrInvalidStructTree", err)
	}
}

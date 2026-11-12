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
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// mergeTestPDF builds a PDF with pages empty pages and, if annotNoAP, a Square
// annotation without /AP on page 1.
func mergeTestPDF(pages int, annotNoAP bool) []byte {
	kids := ""
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", ""}
	for i := range pages {
		kids += fmt.Sprintf(" %d 0 R", 3+i)
		annots := ""
		if i == 0 && annotNoAP {
			annots = fmt.Sprintf(" /Annots [%d 0 R]", 3+pages)
		}
		// Page widths differ by position, so fingerprints show the page order.
		objects = append(objects, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d 842] /Resources << >>%s >>", 500+10*pages+i, annots))
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s ] /Count %d >>", kids, pages)
	if annotNoAP {
		objects = append(objects, "<< /Type /Annot /Subtype /Square /Rect [100 100 200 200] /F 4 >>")
	}
	return failureTestPDF(objects...)
}

// mergeTestPDF20 returns pdf with its 1.7 header replaced by 2.0; offsets do not move.
func mergeTestPDF20(t *testing.T, pdf []byte) []byte {
	t.Helper()
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.7")) {
		t.Fatal("expected a PDF 1.7 header")
	}
	return append([]byte("%PDF-2.0"), pdf[len("%PDF-2.0"):]...)
}

// mergeTestEncrypt encrypts pdf with conf.
func mergeTestEncrypt(t *testing.T, pdf []byte, conf *model.Configuration) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Encrypt(context.Background(), bytes.NewReader(pdf), &out, conf); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func mergeTestReaders(pdfs ...[]byte) []io.ReadSeeker {
	rsc := make([]io.ReadSeeker, len(pdfs))
	for i, pdf := range pdfs {
		rsc[i] = bytes.NewReader(pdf)
	}
	return rsc
}

// requireMergedPDF re-reads out, checks its page count, that it is not encrypted,
// its version and the number of annotations on its pages.
func requireMergedPDF(t *testing.T, out []byte, pages, annots int, pdf20 bool) *model.Context {
	t.Helper()
	ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(out), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatalf("re-read merged output: %v", err)
	}
	if ctx.PageCount != pages {
		t.Fatalf("page count %d, want %d", ctx.PageCount, pages)
	}
	if ctx.Encrypt != nil || bytes.Contains(out, []byte("/Encrypt")) {
		t.Fatal("merged output is encrypted")
	}
	for _, entry := range ctx.Table {
		if entry == nil {
			continue
		}
		if d, ok := entry.Object.(types.Dict); ok && d["Filter"] == types.Name("Standard") {
			t.Fatal("merged output contains an encryption dictionary")
		}
	}
	if ctx.PDF20() != pdf20 {
		t.Fatalf("PDF 2.0 = %v, want %v", ctx.PDF20(), pdf20)
	}
	total := 0
	for i := 1; i <= pages; i++ {
		pd, _, _, err := ctx.PageDict(context.Background(), i, false)
		if err != nil {
			t.Fatal(err)
		}
		arr, err := ctx.DereferenceArray(pd["Annots"])
		if err != nil {
			t.Fatal(err)
		}
		total += len(arr)
	}
	if total != annots {
		t.Fatalf("annotation count %d, want %d", total, annots)
	}
	return ctx
}

// requireMergeInputError asserts that err is a *MergeInputError for index that matches target.
func requireMergeInputError(t *testing.T, err error, index int, target error) {
	t.Helper()
	var inputErr *MergeInputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("expected *MergeInputError, got %v", err)
	}
	if inputErr.Index != index {
		t.Fatalf("MergeInputError index %d, want %d", inputErr.Index, index)
	}
	if !errors.Is(err, target) {
		t.Fatalf("expected %v, got %v", target, err)
	}
}

// mergeTestFingerprint describes the version, size, catalog keys and pages of out.
// Object numbers are not compared: MergeRaw numbers objects differently from run to run.
func mergeTestFingerprint(t *testing.T, out []byte) string {
	t.Helper()
	ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(out), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatalf("re-read merged output: %v", err)
	}
	root, err := ctx.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(root))
	for k := range root {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	s := fmt.Sprintf("v%s size%d root%v", ctx.VersionString(), *ctx.Size, keys)
	for i := 1; i <= ctx.PageCount; i++ {
		pd, _, _, err := ctx.PageDict(context.Background(), i, false)
		if err != nil {
			t.Fatal(err)
		}
		s += fmt.Sprintf(" p%d%v%v", i, pd["MediaBox"], pd["Annots"] != nil)
	}
	return s
}

// panickingMergeReader panics on every access.
type panickingMergeReader struct{}

func (panickingMergeReader) Read([]byte) (int, error) { panic("boom") }

func (panickingMergeReader) Seek(int64, int) (int64, error) { panic("boom") }

func TestMergeRawWithReportZeroOptionsEqualsMergeRaw(t *testing.T) {
	inputs := func() []io.ReadSeeker {
		return mergeTestReaders(mergeTestPDF(1, false), mergeTestPDF(2, false), mergeTestPDF(1, true))
	}
	for _, mode := range []int{model.ValidationRelaxed, model.ValidationStrict} {
		for _, divider := range []bool{false, true} {
			t.Run(fmt.Sprintf("mode%d/divider%v", mode, divider), func(t *testing.T) {
				conf := model.NewDefaultConfiguration()
				conf.ValidationMode = mode

				var raw, withReport bytes.Buffer
				if err := MergeRaw(context.Background(), inputs(), &raw, divider, conf); err != nil {
					t.Fatalf("MergeRaw: %v", err)
				}
				report, err := MergeRawWithReport(context.Background(), inputs(), &withReport, conf, MergeOptions{DividerPage: divider})
				if err != nil {
					t.Fatalf("MergeRawWithReport: %v", err)
				}
				if len(report.Inputs) != 3 {
					t.Fatalf("report has %d inputs, want 3", len(report.Inputs))
				}
				if a, b := mergeTestFingerprint(t, raw.Bytes()), mergeTestFingerprint(t, withReport.Bytes()); a != b {
					t.Fatalf("fingerprints differ:\nMergeRaw: %s\nreport:   %s", a, b)
				}
			})
		}
	}

	valid := mergeTestPDF(1, false)
	encrypted := mergeTestEncrypt(t, valid, model.NewAESConfiguration("", "owner", 256))
	rawErr := MergeRaw(context.Background(), mergeTestReaders(valid, encrypted), io.Discard, false, nil)
	_, reportErr := MergeRawWithReport(context.Background(), mergeTestReaders(valid, encrypted), io.Discard, nil, MergeOptions{})
	if rawErr == nil || reportErr == nil {
		t.Fatalf("expected both merges to fail, got %v and %v", rawErr, reportErr)
	}
	requireMergeInputError(t, reportErr, 1, pdfcpu.ErrEncrypted)
	if rawErr.Error() != reportErr.Error() {
		t.Fatalf("error texts differ:\nMergeRaw: %s\nreport:   %s", rawErr, reportErr)
	}
}

func TestMergeRawWithReportOwnerPasswordOnly(t *testing.T) {
	// Owner-only with no permissions, the shape of PermissionsNone in downstream callers.
	noPermissions := model.NewAESConfiguration("", "owner", 256)
	noPermissions.Permissions = model.PermissionsNone
	encryptions := map[string]*model.Configuration{
		"AES-256":                 model.NewAESConfiguration("", "owner", 256),
		"AES-256/PermissionsNone": noPermissions,
		"AES-128":                 model.NewAESConfiguration("", "owner", 128),
		"RC4-128":                 model.NewRC4Configuration("", "owner", 128),
	}
	for name, encConf := range encryptions {
		encrypted := mergeTestEncrypt(t, mergeTestPDF(2, true), encConf)
		for pos := range 3 {
			t.Run(fmt.Sprintf("%s/%d", name, pos), func(t *testing.T) {
				plain := mergeTestPDF(1, false)
				inputs := [][]byte{plain, plain}
				inputs = slices.Insert(inputs, pos, encrypted)

				_, err := MergeRawWithReport(context.Background(), mergeTestReaders(inputs...), io.Discard, nil, MergeOptions{})
				requireMergeInputError(t, err, pos, pdfcpu.ErrEncrypted)

				var out bytes.Buffer
				report, err := MergeRawWithReport(context.Background(), mergeTestReaders(inputs...), &out, nil, MergeOptions{AllowEncrypted: true})
				if err != nil {
					t.Fatal(err)
				}
				for i, in := range report.Inputs {
					if in.Encrypted != (i == pos) {
						t.Fatalf("input %d Encrypted = %v", i, in.Encrypted)
					}
				}
				requireMergedPDF(t, out.Bytes(), 4, 1, false)
			})
		}
	}
}

func TestMergeRawWithReportUserPassword(t *testing.T) {
	userEncrypted := mergeTestEncrypt(t, mergeTestPDF(1, false), model.NewAESConfiguration("user", "owner", 256))
	inputs := mergeTestReaders(mergeTestPDF(1, false), userEncrypted, mergeTestPDF(1, false))

	_, err := MergeRawWithReport(context.Background(), inputs, io.Discard, nil, MergeOptions{AllowEncrypted: true})
	requireMergeInputError(t, err, 1, pdfcpu.ErrWrongPassword)
	if IsDeterministicFailure(err) {
		t.Fatal("a missing user password must not be a deterministic failure")
	}
}

func TestMergeRawWithReportPDF20(t *testing.T) {
	pdf20Header := mergeTestPDF20(t, mergeTestPDF(2, false))
	pdf20Catalog := failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R /Version /2.0 >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 596 842] >>",
	)
	for name, pdf20 := range map[string][]byte{"header": pdf20Header, "catalog": pdf20Catalog} {
		t.Run(name, func(t *testing.T) {
			plain := mergeTestPDF(1, false)

			_, err := MergeRawWithReport(context.Background(), mergeTestReaders(plain, pdf20), io.Discard, nil, MergeOptions{})
			requireMergeInputError(t, err, 1, pdfcpu.ErrUnsupportedVersion)

			var lowered bytes.Buffer
			loweredReport, err := MergeRawWithReport(context.Background(), mergeTestReaders(plain, pdf20), &lowered, nil, MergeOptions{LowerPDF20: true})
			if err != nil {
				t.Fatal(err)
			}
			requireMergedPDF(t, lowered.Bytes(), 3, 0, false)
			if loweredReport.Inputs[0].LoweredFromPDF20 || !loweredReport.Inputs[1].LoweredFromPDF20 {
				t.Fatalf("LoweredFromPDF20 = %v, %v; want false, true", loweredReport.Inputs[0].LoweredFromPDF20, loweredReport.Inputs[1].LoweredFromPDF20)
			}

			for _, lower := range []bool{false, true} {
				var out bytes.Buffer
				report, err := MergeRawWithReport(context.Background(), mergeTestReaders(pdf20, plain), &out, nil, MergeOptions{LowerPDF20: lower})
				if err != nil {
					t.Fatalf("LowerPDF20=%v: %v", lower, err)
				}
				for i, in := range report.Inputs {
					if in.LoweredFromPDF20 {
						t.Fatalf("LowerPDF20=%v: input %d is reported as lowered behind a PDF 2.0 first input", lower, i)
					}
				}
				requireMergedPDF(t, out.Bytes(), 3, 0, true)
			}
		})
	}
}

func TestMergeRawWithReportPDF20AnnotationWithoutAppearance(t *testing.T) {
	clean20 := mergeTestPDF20(t, mergeTestPDF(1, false))
	noAP20 := mergeTestPDF20(t, mergeTestPDF(1, true))
	noAP17 := mergeTestPDF(1, true)
	plain17 := mergeTestPDF(1, false)

	cases := []struct {
		name   string
		inputs [][]byte
		annots int
		pdf20  bool
	}{
		{"2.0-noAP first", [][]byte{noAP20, plain17}, 1, true},
		{"2.0-noAP second", [][]byte{plain17, noAP20}, 1, false},
		{"2.0-noAP + 1.7-noAP", [][]byte{noAP20, noAP17}, 2, true},
		{"2.0-noAP twice", [][]byte{noAP20, noAP20}, 2, true},
		{"clean 2.0 first + 1.7-noAP", [][]byte{clean20, noAP17}, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			report, err := MergeRawWithReport(context.Background(), mergeTestReaders(tc.inputs...), &out, nil, MergeOptions{LowerPDF20: true})
			if err != nil {
				t.Fatal(err)
			}
			requireMergedPDF(t, out.Bytes(), 2, tc.annots, tc.pdf20)
			for i, in := range tc.inputs {
				if bytes.Equal(in, noAP20) && report.Inputs[i].Notices.Len() == 0 {
					t.Fatalf("input %d: expected a notice for the annotation without /AP", i)
				}
			}
		})
	}

	t.Run("strict read", func(t *testing.T) {
		conf := model.NewDefaultConfiguration()
		conf.ValidationMode = model.ValidationStrict
		_, err := MergeRawWithReport(context.Background(), mergeTestReaders(noAP20, plain17), io.Discard, conf, MergeOptions{})
		requireMergeInputError(t, err, 0, ErrMissingAnnotationAppearance)
	})

	t.Run("strict write", func(t *testing.T) {
		conf := model.NewDefaultConfiguration()
		conf.ValidationMode = model.ValidationStrict
		inputs := [][]byte{clean20, noAP17}

		_, err := MergeRawWithReport(context.Background(), mergeTestReaders(inputs...), io.Discard, conf, MergeOptions{LowerPDF20: true})
		if err == nil || !errors.Is(err, ErrMissingAnnotationAppearance) {
			t.Fatalf("expected ErrMissingAnnotationAppearance, got %v", err)
		}
		var inputErr *MergeInputError
		if errors.As(err, &inputErr) {
			t.Fatalf("write failure must not be a MergeInputError: %v", err)
		}
		rawErr := MergeRaw(context.Background(), mergeTestReaders(inputs...), io.Discard, false, conf)
		if rawErr == nil || rawErr.Error() != err.Error() {
			t.Fatalf("error texts differ:\nMergeRaw: %v\nreport:   %v", rawErr, err)
		}
	})
}

func TestMergeRawWithReportOrphanWidgetIntegerSubtype(t *testing.T) {
	// /Subtype is an integer, not a name, in an unreferenced annotation.
	f := failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>",
		"<< /Subtype 1413830740 /T (f) >>",
	)
	simple := mergeTestPDF(1, false)
	for _, mode := range []int{model.ValidationRelaxed, model.ValidationStrict} {
		for _, order := range [][][]byte{{f, simple}, {simple, f}} {
			t.Run(fmt.Sprintf("mode%d/first-is-f=%v", mode, bytes.Equal(order[0], f)), func(t *testing.T) {
				conf := model.NewDefaultConfiguration()
				conf.ValidationMode = mode

				rawErr := MergeRaw(context.Background(), mergeTestReaders(order...), io.Discard, false, conf)
				var out bytes.Buffer
				_, err := MergeRawWithReport(context.Background(), mergeTestReaders(order...), &out, conf, MergeOptions{AllowEncrypted: true, LowerPDF20: true})
				if (rawErr == nil) != (err == nil) {
					t.Fatalf("MergeRaw err %v, MergeRawWithReport err %v", rawErr, err)
				}
				if err == nil {
					requireMergedPDF(t, out.Bytes(), 2, 0, false)
				}
			})
		}
	}
}

func TestMergeRawWithReportNoticesPerInput(t *testing.T) {
	clean20 := mergeTestPDF20(t, mergeTestPDF(1, false))
	noAP20 := mergeTestPDF20(t, mergeTestPDF(1, true))

	report, err := MergeRawWithReport(context.Background(), mergeTestReaders(clean20, noAP20, clean20), io.Discard, nil, MergeOptions{LowerPDF20: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []int{0, 1, 0}
	for i, in := range report.Inputs {
		if in.Notices.Len() != want[i] {
			t.Fatalf("input %d: %d notices, want %d", i, in.Notices.Len(), want[i])
		}
	}
	notice, ok := report.Inputs[1].Notices.First()
	if !ok || !errors.Is(notice.Cause, ErrMissingAnnotationAppearance) {
		t.Fatalf("input 1: first notice %v does not match ErrMissingAnnotationAppearance", notice.Cause)
	}
}

func TestMergeContexts(t *testing.T) {
	readAll := func(pdfs ...[]byte) []*model.Context {
		ctxs := make([]*model.Context, len(pdfs))
		for i, pdf := range pdfs {
			ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(pdf), model.NewTolerantConfiguration())
			if err != nil {
				t.Fatal(err)
			}
			ctxs[i] = ctx
		}
		return ctxs
	}
	owner := mergeTestEncrypt(t, mergeTestPDF(1, false), model.NewAESConfiguration("", "owner", 256))
	twoPage := mergeTestPDF(2, false)
	pdf20 := mergeTestPDF20(t, mergeTestPDF(1, false))

	// Input 0 is encrypted: the read succeeds with the tolerant configuration,
	// so the merge refuses it unless AllowEncrypted is set.
	ctxs := readAll(owner, twoPage, pdf20)
	err := MergeContexts(context.Background(), ctxs, io.Discard, MergeOptions{})
	requireMergeInputError(t, err, 0, pdfcpu.ErrEncrypted)

	// An encrypted input behind input 0 is refused the same way.
	err = MergeContexts(context.Background(), readAll(twoPage, owner), io.Discard, MergeOptions{})
	requireMergeInputError(t, err, 1, pdfcpu.ErrEncrypted)

	var out bytes.Buffer
	if err := MergeContexts(context.Background(), readAll(owner, twoPage, pdf20), &out, MergeOptions{AllowEncrypted: true, LowerPDF20: true}); err != nil {
		t.Fatal(err)
	}
	requireMergedPDF(t, out.Bytes(), 4, 0, false)

	a := readAll(twoPage)[0]
	err = MergeContexts(context.Background(), []*model.Context{a, readAll(twoPage)[0], a}, io.Discard, MergeOptions{})
	requireMergeInputError(t, err, 2, errDuplicateMergeContext)

	err = MergeContexts(context.Background(), []*model.Context{readAll(twoPage)[0], nil}, io.Discard, MergeOptions{})
	requireMergeInputError(t, err, 1, ErrMissingContext)

	// The write notice for an annotation without /AP lands in the report of ctxs[0].
	dest := readAll(pdf20)[0]
	if err := MergeContexts(context.Background(), []*model.Context{dest, readAll(mergeTestPDF(1, true))[0]}, io.Discard, MergeOptions{LowerPDF20: true}); err != nil {
		t.Fatal(err)
	}
	notice, ok := dest.ValidationReport().First()
	if !ok || !errors.Is(notice.Cause, ErrMissingAnnotationAppearance) {
		t.Fatalf("expected a write notice for the annotation without /AP, got %v", notice.Cause)
	}
}

func TestMergeRawWithReportStructTreeInvalid(t *testing.T) {
	broken := failureTestStructTreePDF(3, "<< /Kids [99 0 R] >>")
	simple := mergeTestPDF(1, false)

	for pos, inputs := range [][][]byte{{broken, simple}, {simple, broken}} {
		t.Run(fmt.Sprintf("broken-at-%d", pos), func(t *testing.T) {
			// Kept by the tolerant configuration: the output keeps the tree of the first input only.
			var out bytes.Buffer
			report, err := MergeRawWithReport(context.Background(), mergeTestReaders(inputs...), &out, nil, MergeOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !report.Inputs[pos].StructTreeInvalid || report.Inputs[pos].StructTreeRemoved {
				t.Fatalf("input %d: StructTreeInvalid %t, StructTreeRemoved %t, want true and false",
					pos, report.Inputs[pos].StructTreeInvalid, report.Inputs[pos].StructTreeRemoved)
			}
			// The default configuration rejects the kept tree, so read the output tolerantly.
			ctx, rereadReport, err := ReadAndValidateWithReport(context.Background(), bytes.NewReader(out.Bytes()), nil)
			if err != nil {
				t.Fatalf("tolerant re-read: %v", err)
			}
			if ctx.PageCount != 2 {
				t.Fatalf("page count %d, want 2", ctx.PageCount)
			}
			root, err := ctx.Catalog()
			if err != nil {
				t.Fatal(err)
			}
			_, hasTree := root["StructTreeRoot"]
			if pos == 0 && (!hasTree || !rereadReport.StructTreeInvalid) {
				t.Fatalf("pos 0: tree kept %t, re-read invalid %t, want both true", hasTree, rereadReport.StructTreeInvalid)
			}
			if pos == 1 && hasTree {
				t.Fatal("pos 1: output has a StructTreeRoot, want the first input's catalog only")
			}

			// Removal, as the configuration asked before KeepInvalidStructTree existed.
			removeConf := model.NewTolerantConfiguration()
			removeConf.KeepInvalidStructTree = false
			removeConf.RemoveInvalidStructTree = true
			var removed bytes.Buffer
			report, err = MergeRawWithReport(context.Background(), mergeTestReaders(inputs...), &removed, removeConf, MergeOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !report.Inputs[pos].StructTreeInvalid || !report.Inputs[pos].StructTreeRemoved {
				t.Fatalf("remove: input %d: StructTreeInvalid %t, StructTreeRemoved %t, want both true",
					pos, report.Inputs[pos].StructTreeInvalid, report.Inputs[pos].StructTreeRemoved)
			}
			removedCtx := requireMergedPDF(t, removed.Bytes(), 2, 0, false)
			if pos == 0 {
				removedRoot, err := removedCtx.Catalog()
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := removedRoot["StructTreeRoot"]; ok {
					t.Fatal("output catalog keeps the removed StructTreeRoot")
				}
			}

			failConf := model.NewTolerantConfiguration()
			failConf.KeepInvalidStructTree = false
			failConf.RemoveInvalidStructTree = false
			_, err = MergeRawWithReport(context.Background(), mergeTestReaders(inputs...), io.Discard, failConf, MergeOptions{})
			requireMergeInputError(t, err, pos, model.ErrInvalidStructTree)
		})
	}
}

func TestMergeRawWithReportContextErrors(t *testing.T) {
	t.Run("canceled with cause", func(t *testing.T) {
		cause := errors.New("merge stopped by test")
		c := failureTestCanceledContext(cause)
		_, err := MergeRawWithReport(c, mergeTestReaders(mergeTestPDF(1, false), mergeTestPDF(2, false)), io.Discard, nil, MergeOptions{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
		if context.Cause(c) != cause {
			t.Fatalf("context cause changed to %v", context.Cause(c))
		}
		if IsDeterministicFailure(err) {
			t.Fatal("a context error must not be a deterministic failure")
		}
	})

	t.Run("deadline at every check", func(t *testing.T) {
		failures, wrapped := 0, 0
		for checks := 0; checks < 400; checks++ {
			c := &deadlineAfterChecksContext{Context: context.Background(), checks: checks}
			var out bytes.Buffer
			_, err := MergeRawWithReport(c, mergeTestReaders(mergeTestPDF(1, true), mergeTestPDF(2, false)), &out, nil, MergeOptions{})
			if err == nil {
				break
			}
			failures++
			if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, model.ErrInvalidPDF) {
				t.Fatalf("checks %d: unexpected error %v", checks, err)
			}
			var inputErr *MergeInputError
			if errors.As(err, &inputErr) {
				wrapped++
			} else if !(checks == 0 && err == context.DeadlineExceeded) && !strings.HasPrefix(err.Error(), "merge: ") {
				// Only the entry check (checks == 0) and write stage errors are not input errors.
				t.Fatalf("checks %d: context error outside an input is not a MergeInputError: %v", checks, err)
			}
		}
		if failures == 0 {
			t.Fatal("no deadline was reached")
		}
		if wrapped == 0 {
			t.Fatal("no context error was reported as a MergeInputError")
		}
	})

	t.Run("panic", func(t *testing.T) {
		_, err := MergeRawWithReport(context.Background(), []io.ReadSeeker{panickingMergeReader{}}, io.Discard, nil, MergeOptions{})
		var panicErr *PanicError
		if !errors.As(err, &panicErr) {
			t.Fatalf("expected *PanicError, got %v", err)
		}
	})
}

func TestMergeRawWithReportSharedConfiguration(t *testing.T) {
	conf := model.NewDefaultConfiguration()
	owner := mergeTestEncrypt(t, mergeTestPDF(1, false), model.NewAESConfiguration("", "owner", 256))
	simple := mergeTestPDF(1, false)

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			var out bytes.Buffer
			_, err := MergeRawWithReport(context.Background(), mergeTestReaders(owner, simple), &out, conf, MergeOptions{AllowEncrypted: true})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	if conf.Cmd != model.VALIDATE || !conf.CreateBookmarks {
		t.Fatalf("caller configuration changed: Cmd %v, CreateBookmarks %v", conf.Cmd, conf.CreateBookmarks)
	}
}

// countingMergeReader counts the rewinds to the start of the input, one per read pass.
type countingMergeReader struct {
	io.ReadSeeker
	rewinds int
}

func (r *countingMergeReader) Seek(offset int64, whence int) (int64, error) {
	if offset == 0 && whence == io.SeekStart {
		r.rewinds++
	}
	return r.ReadSeeker.Seek(offset, whence)
}

func TestMergeRawWithReportReadsEachInputOnce(t *testing.T) {
	owner := mergeTestEncrypt(t, mergeTestPDF(1, false), model.NewAESConfiguration("", "owner", 256))
	pdfs := [][]byte{mergeTestPDF(1, true), owner, mergeTestPDF20(t, mergeTestPDF(1, false))}

	// Baseline: the rewinds of one plain read of each input.
	baseline := make([]int, len(pdfs))
	for i, pdf := range pdfs {
		r := &countingMergeReader{ReadSeeker: bytes.NewReader(pdf)}
		if _, err := ReadAndValidate(context.Background(), r, model.NewTolerantConfiguration()); err != nil {
			t.Fatal(err)
		}
		if r.rewinds == 0 {
			t.Fatalf("input %d: no rewind counted, the counting reader is not used", i)
		}
		baseline[i] = r.rewinds
	}

	rsc := make([]io.ReadSeeker, len(pdfs))
	counters := make([]*countingMergeReader, len(pdfs))
	for i, pdf := range pdfs {
		counters[i] = &countingMergeReader{ReadSeeker: bytes.NewReader(pdf)}
		rsc[i] = counters[i]
	}
	var out bytes.Buffer
	if _, err := MergeRawWithReport(context.Background(), rsc, &out, nil, MergeOptions{AllowEncrypted: true, LowerPDF20: true}); err != nil {
		t.Fatal(err)
	}
	for i, r := range counters {
		if r.rewinds != baseline[i] {
			t.Fatalf("input %d: %d rewinds, want %d (one read pass)", i, r.rewinds, baseline[i])
		}
	}
}

func TestMergeRawWithReportWriteNoticeOncePerAnnotation(t *testing.T) {
	// The widget is reached through /Annots and /Fields, the Square through
	// /Annots and back from its Popup through /Parent. Neither has /AP.
	pdf := failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R /AcroForm << /Fields [4 0 R] >> >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Annots [4 0 R 5 0 R] >>",
		"<< /Type /Annot /Subtype /Widget /FT /Tx /T (f) /DA (/Helv 0 Tf 0 g) /Rect [100 100 200 200] /F 4 >>",
		"<< /Type /Annot /Subtype /Square /Rect [100 300 200 400] /F 4 /Popup 6 0 R >>",
		"<< /Type /Annot /Subtype /Popup /Rect [0 0 100 100] /Parent 5 0 R >>",
	)
	var out bytes.Buffer
	report, err := MergeRawWithReport(context.Background(), mergeTestReaders(mergeTestPDF20(t, pdf)), &out, nil, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	requireMergedPDF(t, out.Bytes(), 1, 2, true)
	if n := report.Output.Len(); n != 2 {
		t.Fatalf("write notices %d, want one per annotation (2)", n)
	}
	for notice := range report.Output.All() {
		if !errors.Is(notice.Cause, ErrMissingAnnotationAppearance) {
			t.Fatalf("write notice %v does not match ErrMissingAnnotationAppearance", notice.Cause)
		}
	}
}

func TestMergeRawWithReportOutputNotices(t *testing.T) {
	clean20 := mergeTestPDF20(t, mergeTestPDF(1, false))
	noAP20 := mergeTestPDF20(t, mergeTestPDF(1, true))
	noAP17 := mergeTestPDF(1, true)
	plain17 := mergeTestPDF(1, false)

	// The annotation without /AP is only checked when the 2.0 output is written.
	report, err := MergeRawWithReport(context.Background(), mergeTestReaders(clean20, noAP17), io.Discard, nil, MergeOptions{LowerPDF20: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.Inputs[1].Notices.Len() != 0 || report.Output.Len() != 1 {
		t.Fatalf("read notices %d, output notices %d; want 0 and 1", report.Inputs[1].Notices.Len(), report.Output.Len())
	}
	if notice, ok := report.Output.First(); !ok || !errors.Is(notice.Cause, ErrMissingAnnotationAppearance) {
		t.Fatalf("output notice %v does not match ErrMissingAnnotationAppearance", notice.Cause)
	}

	// Input 0 has a read notice for the annotation; Output holds only the write
	// notice for it, not the read notice again.
	report, err = MergeRawWithReport(context.Background(), mergeTestReaders(noAP20, plain17), io.Discard, nil, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Inputs[0].Notices.Len() != 1 || report.Output.Len() != 1 {
		t.Fatalf("read notices %d, output notices %d; want 1 and 1", report.Inputs[0].Notices.Len(), report.Output.Len())
	}

	report, err = MergeRawWithReport(context.Background(), mergeTestReaders(clean20, plain17), io.Discard, nil, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Output.Empty() {
		t.Fatalf("output notices %d for a clean merge, want 0", report.Output.Len())
	}
}

// mergeTestReadAll reads each pdf with the tolerant configuration, for MergeContexts.
func mergeTestReadAll(t *testing.T, pdfs ...[]byte) []*model.Context {
	t.Helper()
	ctxs := make([]*model.Context, len(pdfs))
	for i, pdf := range pdfs {
		ctx, err := ReadAndValidate(context.Background(), bytes.NewReader(pdf), model.NewTolerantConfiguration())
		if err != nil {
			t.Fatal(err)
		}
		ctxs[i] = ctx
	}
	return ctxs
}

func TestMergeContextsChecksBeforeChanging(t *testing.T) {
	twoPage := mergeTestPDF(2, false)
	pdf20 := mergeTestPDF20(t, mergeTestPDF(1, false))
	owner := mergeTestEncrypt(t, mergeTestPDF(1, false), model.NewAESConfiguration("", "owner", 256))

	t.Run("version refusal", func(t *testing.T) {
		ctxs := mergeTestReadAll(t, twoPage, pdf20, owner)
		conf := ctxs[0].Configuration
		err := MergeContexts(context.Background(), ctxs, io.Discard, MergeOptions{})
		requireMergeInputError(t, err, 1, pdfcpu.ErrUnsupportedVersion)
		if ctxs[0].Configuration != conf {
			t.Fatal("a refused merge replaced the configuration of ctxs[0]")
		}

		// The same contexts merge once the options allow the inputs.
		var out bytes.Buffer
		if err := MergeContexts(context.Background(), ctxs, &out, MergeOptions{AllowEncrypted: true, LowerPDF20: true}); err != nil {
			t.Fatal(err)
		}
		requireMergedPDF(t, out.Bytes(), 4, 0, false)
	})

	t.Run("encryption refusal", func(t *testing.T) {
		ctxs := mergeTestReadAll(t, twoPage, twoPage, owner)
		conf := ctxs[0].Configuration
		err := MergeContexts(context.Background(), ctxs, io.Discard, MergeOptions{})
		requireMergeInputError(t, err, 2, pdfcpu.ErrEncrypted)
		if ctxs[0].Configuration != conf {
			t.Fatal("a refused merge replaced the configuration of ctxs[0]")
		}

		// Input 1 was not consumed by the refused merge.
		var out bytes.Buffer
		if err := MergeContexts(context.Background(), ctxs, &out, MergeOptions{AllowEncrypted: true}); err != nil {
			t.Fatal(err)
		}
		requireMergedPDF(t, out.Bytes(), 5, 0, false)
	})
}

func TestMergeContextsSharedXRefTable(t *testing.T) {
	ctxs := mergeTestReadAll(t, mergeTestPDF(2, false))
	// A shallow copy is a different *model.Context with the same XRefTable.
	shallow := *ctxs[0]
	ctxs = append(ctxs, &shallow)
	err := MergeContexts(context.Background(), ctxs, io.Discard, MergeOptions{})
	requireMergeInputError(t, err, 1, errDuplicateMergeContext)
}

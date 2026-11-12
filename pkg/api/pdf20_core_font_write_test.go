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
	"fmt"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// pdf20CoreFontPDF has one page that uses font as F1. It is a PDF 2.0 file.
func pdf20CoreFontPDF(t *testing.T, font string) []byte {
	t.Helper()
	const content = "BT /F1 24 Tf 20 100 Td (Hello) Tj ET"
	pdf := failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 5 0 R /Resources << /Font << /F1 4 0 R >> >> >>",
		font,
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
	)
	return mergeTestPDF20(t, pdf)
}

// pdf20CoreFontWrite writes the PDF 2.0 file with font F1 and returns the font
// dictionary of the written file and the validation notices of the write.
func pdf20CoreFontWrite(t *testing.T, font string) (types.Dict, model.ValidationReport) {
	t.Helper()
	ctx, err := ReadAndValidate(t.Context(), bytes.NewReader(pdf20CoreFontPDF(t, font)), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	before := ctx.ValidationReport()
	var out bytes.Buffer
	if err := WriteContext(t.Context(), ctx, &out); err != nil {
		t.Fatal(err)
	}
	report := ctx.ValidationReport().Since(before)

	written, err := ReadAndValidate(t.Context(), bytes.NewReader(out.Bytes()), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	_, _, inh, err := written.PageDict(t.Context(), 1, false)
	if err != nil {
		t.Fatal(err)
	}
	fonts, err := written.DereferenceDict(inh.Resources["Font"])
	if err != nil {
		t.Fatal(err)
	}
	f1, err := written.DereferenceDict(fonts["F1"])
	if err != nil {
		t.Fatal(err)
	}
	return f1, report
}

func TestWritePDF20KeepsStandardFontWithoutReconstructibleMetrics(t *testing.T) {
	tests := []struct {
		name string
		font string
		want string
	}{
		{
			name: "MacRoman encoding",
			font: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /MacRomanEncoding >>",
			want: "MacRomanEncoding",
		},
		{
			name: "partial widths",
			font: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 32 /LastChar 32 /Widths [278] >>",
			want: "partial",
		},
		{
			name: "glyph without metrics",
			font: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding << /Differences [65 /foo] >> >>",
			want: "foo",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f1, report := pdf20CoreFontWrite(t, tc.font)
			if _, ok := f1["FontDescriptor"]; ok {
				t.Fatal("font without reconstructible metrics must be written unchanged")
			}
			if report.Len() != 1 {
				t.Fatalf("expected one notice, got %d", report.Len())
			}
			notice, _ := report.First()
			if notice.Disposition != model.NoticeDigested || !strings.Contains(notice.Message, tc.want) {
				t.Fatalf("unexpected notice: disposition %q message %q", notice.Disposition, notice.Message)
			}
		})
	}
}

func TestWritePDF20CompletesStandardFontMetrics(t *testing.T) {
	f1, report := pdf20CoreFontWrite(t, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	if _, ok := f1["FontDescriptor"]; !ok {
		t.Fatal("expected the FontDescriptor of a completed standard font")
	}
	if _, ok := f1["Widths"]; !ok {
		t.Fatal("expected the Widths of a completed standard font")
	}
	if !report.Empty() {
		t.Fatalf("expected no notices, got %d", report.Len())
	}
}

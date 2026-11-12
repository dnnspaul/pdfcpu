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
	"io"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func extractResourcesTestStream(entries, content string) string {
	return fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", entries, len(content), content)
}

// formWithoutResourcesPDF has a Form XObject without Resources of its own.
// The form draws its text with the page's font F1, so F1 must be kept.
func formWithoutResourcesPDF() []byte {
	return failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << /Font << /F1 5 0 R /F2 5 0 R >> /XObject << /Fm1 6 0 R >> >> >>",
		extractResourcesTestStream("", "/Fm1 Do"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		extractResourcesTestStream("/Type /XObject /Subtype /Form /BBox [0 0 595 842]", "BT /F1 24 Tf 20 100 Td (Hello) Tj ET"),
	)
}

// nestedFormWithoutResourcesPDF has a Form XObject Fm1 with Resources of its own, which
// names the Form XObject Fm2 without Resources. Fm2 draws its text with the page's font F1,
// so F1 must be kept although the page content names only Fm1.
func nestedFormWithoutResourcesPDF() []byte {
	return failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << /Font << /F1 5 0 R /F2 5 0 R >> /XObject << /Fm1 6 0 R >> >> >>",
		extractResourcesTestStream("", "/Fm1 Do"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		extractResourcesTestStream("/Type /XObject /Subtype /Form /BBox [0 0 595 842] /Resources << /XObject << /Fm2 7 0 R >> >>", "/Fm2 Do"),
		extractResourcesTestStream("/Type /XObject /Subtype /Form /BBox [0 0 595 842]", "BT /F1 24 Tf 20 100 Td (Hello) Tj ET"),
	)
}

// annotationAppearanceFontPDF has an annotation whose normal appearance stream, without
// Resources, draws with the page's font F1. The page content names no resource.
func annotationAppearanceFontPDF() []byte {
	return failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << /Font << /F1 5 0 R /F2 5 0 R >> >> /Annots [7 0 R] >>",
		extractResourcesTestStream("", "q Q"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		extractResourcesTestStream("/Type /XObject /Subtype /Form /BBox [0 0 595 842]", "BT /F1 24 Tf 20 100 Td (Hello) Tj ET"),
		"<< /Type /Annot /Subtype /Widget /Rect [0 0 100 100] /AP << /N 6 0 R >> >>",
	)
}

// unusedFontPDF has an inherited font dictionary of which the page uses F1 only.
func unusedFontPDF() []byte {
	return failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /Resources << /Font << /F1 5 0 R /F2 5 0 R >> >> >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R >>",
		extractResourcesTestStream("", "BT /F1 24 Tf 20 100 Td (Hello) Tj ET"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
	)
}

// extractResourcesTestFonts extracts page 1 of pdf and returns the font
// resource dictionary of the extracted page.
func extractResourcesTestFonts(t *testing.T, pdf []byte) types.Dict {
	t.Helper()
	ctx, err := ReadAndValidate(t.Context(), bytes.NewReader(pdf), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	r, err := ExtractPage(t.Context(), ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ReadAndValidate(t.Context(), bytes.NewReader(b), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	_, _, inh, err := out.PageDict(t.Context(), 1, false)
	if err != nil {
		t.Fatal(err)
	}
	fonts, err := out.DereferenceDict(inh.Resources["Font"])
	if err != nil {
		t.Fatal(err)
	}
	return fonts
}

func TestExtractPageKeepsResourcesOfFormWithoutResources(t *testing.T) {
	fonts := extractResourcesTestFonts(t, formWithoutResourcesPDF())
	if _, ok := fonts["F1"]; !ok {
		t.Fatalf("font F1 used by the form was removed: %v", fonts)
	}
	if _, ok := fonts["F2"]; !ok {
		t.Fatalf("font F2 was removed although the page keeps all inherited resources: %v", fonts)
	}
}

func TestExtractPageKeepsFontOfNestedFormWithoutResources(t *testing.T) {
	fonts := extractResourcesTestFonts(t, nestedFormWithoutResourcesPDF())
	if _, ok := fonts["F1"]; !ok {
		t.Fatalf("font F1 used by the nested form was removed: %v", fonts)
	}
}

func TestExtractPageKeepsFontOfAnnotationAppearance(t *testing.T) {
	fonts := extractResourcesTestFonts(t, annotationAppearanceFontPDF())
	if _, ok := fonts["F1"]; !ok {
		t.Fatalf("font F1 used by the annotation appearance was removed: %v", fonts)
	}
}

func TestExtractPageTrimsUnusedResources(t *testing.T) {
	fonts := extractResourcesTestFonts(t, unusedFontPDF())
	if _, ok := fonts["F1"]; !ok {
		t.Fatalf("used font F1 was removed: %v", fonts)
	}
	if _, ok := fonts["F2"]; ok {
		t.Fatalf("unused font F2 was kept: %v", fonts)
	}
}

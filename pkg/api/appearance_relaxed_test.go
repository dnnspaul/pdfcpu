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
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func appearanceRelaxedTestStream(entries, content string) string {
	return fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", entries, len(content), content)
}

// appearanceRelaxedPDF has a square annotation whose normal appearance is a
// Form XObject without /BBox. The page has /Resources so that only the
// appearance is invalid.
func appearanceRelaxedPDF() []byte {
	return failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> /Annots [4 0 R] >>",
		"<< /Type /Annot /Subtype /Square /Rect [10 10 100 100] /AP << /N 5 0 R >> >>",
		appearanceRelaxedTestStream("/Type /XObject /Subtype /Form", "0 0 m 100 100 l S"),
	)
}

// appearanceRelaxedNormal returns the raw /N entry of the first annotation of
// page pageNr of ctx.
func appearanceRelaxedNormal(t *testing.T, ctx *model.Context, pageNr int) types.Object {
	t.Helper()
	page, _, _, err := ctx.PageDict(t.Context(), pageNr, false)
	if err != nil {
		t.Fatal(err)
	}
	annots, err := ctx.DereferenceArray(page["Annots"])
	if err != nil || len(annots) != 1 {
		t.Fatalf("page %d: annotations %v, err %v", pageNr, annots, err)
	}
	annot, err := ctx.DereferenceDict(annots[0])
	if err != nil {
		t.Fatal(err)
	}
	ap, err := ctx.DereferenceDict(annot["AP"])
	if err != nil {
		t.Fatal(err)
	}
	n, ok := ap.Find("N")
	if !ok {
		t.Fatalf("page %d: normal appearance was removed", pageNr)
	}
	return n
}

func TestRelaxedReadKeepsCorruptAppearance(t *testing.T) {
	ctx, report, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(appearanceRelaxedPDF()), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	notice, ok := report.Notices.First()
	if !ok {
		t.Fatal("expected a notice for the corrupt appearance")
	}
	if notice.Path != "AP/N" || notice.Disposition != model.NoticeDigested || notice.ObjectNumber != 5 {
		t.Fatalf("unexpected notice: path %q disposition %q object %d", notice.Path, notice.Disposition, notice.ObjectNumber)
	}
	if _, ok := appearanceRelaxedNormal(t, ctx, 1).(types.IndirectRef); !ok {
		t.Fatal("read: corrupt normal appearance was not kept as a reference")
	}

	var extracted bytes.Buffer
	r, err := ExtractPage(t.Context(), ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(&extracted, r); err != nil {
		t.Fatal(err)
	}
	out, err := ReadAndValidate(t.Context(), bytes.NewReader(extracted.Bytes()), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := appearanceRelaxedNormal(t, out, 1).(types.IndirectRef); !ok {
		t.Fatal("extract: corrupt normal appearance was not kept as a reference")
	}

	var merged bytes.Buffer
	rsc := []io.ReadSeeker{bytes.NewReader(failureTestValidPDF()), bytes.NewReader(appearanceRelaxedPDF())}
	if err := MergeRaw(t.Context(), rsc, &merged, false, model.NewDefaultConfiguration()); err != nil {
		t.Fatal(err)
	}
	mergedCtx, err := ReadAndValidate(t.Context(), bytes.NewReader(merged.Bytes()), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := appearanceRelaxedNormal(t, mergedCtx, 2).(types.IndirectRef); !ok {
		t.Fatal("merge: corrupt normal appearance was not kept as a reference")
	}
}

func TestStrictReadRejectsCorruptAppearance(t *testing.T) {
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationStrict
	_, err := ReadAndValidate(t.Context(), bytes.NewReader(appearanceRelaxedPDF()), conf)
	if err == nil || !strings.Contains(err.Error(), "BBox") {
		t.Fatalf("expected strict validation to reject the appearance without BBox, got %v", err)
	}
}

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

package pdfcpu

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// ocNoticeTestPDF builds a PDF whose object i+1 is objs[i], with a valid xref table.
func ocNoticeTestPDF(objs ...string) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objs))
	for i, body := range objs {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return buf.Bytes()
}

// ocNoticeTestContext reads a PDF built from the catalog and the objects after it.
func ocNoticeTestContext(t *testing.T, catalog string, objs ...string) *model.Context {
	t.Helper()
	all := append([]string{catalog, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>"}, objs...)
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	ctx, err := Read(t.Context(), bytes.NewReader(ocNoticeTestPDF(all...)), conf)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

// requireOCNotice checks that ctx has one skipped notice with the path and a message containing msg.
func requireOCNotice(t *testing.T, ctx *model.Context, path, msg string) {
	t.Helper()
	var found []model.ValidationNotice
	for _, n := range ctx.ValidationReport().Notices() {
		if n.Disposition == model.NoticeSkipped && n.Path == path && strings.Contains(n.Message, msg) {
			found = append(found, n)
		}
	}
	if len(found) != 1 {
		t.Fatalf("skipped notices at %s containing %q: %d, want 1; report: %v", path, msg, len(found), ctx.ValidationReport().Notices())
	}
}

// TestCarryOCPropertiesNoticeWhenUnreadable checks that extraction does not fail when the
// optional content groups of the source cannot be read. The properties are not carried
// over and the source gets one notice.
func TestCarryOCPropertiesNoticeWhenUnreadable(t *testing.T) {
	src := ocNoticeTestContext(t,
		"<< /Type /Catalog /Pages 2 0 R /OCProperties << /OCGs 5 0 R /D << >> >> >>",
		"<< /Type /OCG /Name (Layer) >>",
		"[4 0 R]",
	)
	// The reader accepts the group array, so the damage is made after reading: the
	// array object is an integer. The properties cannot be read from the source.
	src.Table[5].Object = types.Integer(42)
	dest := ocNoticeTestContext(t, "<< /Type /Catalog /Pages 2 0 R >>")

	carryOCProperties(src, dest, map[int]int{})

	requireOCNotice(t, src, "OCProperties", "optional content properties not carried over")
	if _, found := dest.RootDict["OCProperties"]; found {
		t.Fatal("OCProperties carried over from an unreadable source")
	}
}

// TestMergeOCPropertiesNoticeWhenUnreadable checks that a merge does not fail when the
// group array of a merged input cannot be read. The merge resolves the references of the
// input in the destination, where the input's objects are renumbered, so the damage is
// made there. The destination keeps its own properties and gets one notice.
func TestMergeOCPropertiesNoticeWhenUnreadable(t *testing.T) {
	src := ocNoticeTestContext(t,
		"<< /Type /Catalog /Pages 2 0 R /OCProperties << /OCGs 5 0 R /D << >> >> >>",
		"<< /Type /OCG /Name (Layer) >>",
		"[4 0 R]",
	)
	dest := ocNoticeTestContext(t,
		"<< /Type /Catalog /Pages 2 0 R /OCProperties << /OCGs [4 0 R] /D << >> >> >>",
		"<< /Type /OCG /Name (Own) >>",
		"42",
	)

	if err := mergeOCProperties(src, dest); err != nil {
		t.Fatalf("merge: %v", err)
	}

	requireOCNotice(t, dest, "OCProperties", "optional content properties of a merged input are not carried over")
	rootDict, err := dest.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	props, err := dest.DereferenceDict(rootDict["OCProperties"])
	if err != nil {
		t.Fatal(err)
	}
	groups, err := dest.DereferenceArray(props["OCGs"])
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("destination OCGs changed: %d groups, want 1", len(groups))
	}
}

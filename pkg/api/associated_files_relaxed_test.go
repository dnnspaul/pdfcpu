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
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// associatedFilesInvoiceXML is the content of the embedded file of associatedFilesTestPDF.
const associatedFilesInvoiceXML = "<Invoice/>"

// associatedFilesNames is the EmbeddedFiles Names array of associatedFilesTestPDF with one entry.
const associatedFilesNames = "[(factur-x.xml) 4 0 R]"

// associatedFilesTestPDF builds a PDF with the header version and one page. The
// EmbeddedFiles name tree has the Names array names. Object 4 is a file
// specification with /AFRelationship /Data for the embedded file object 5.
// The catalog has /AF catalogAF unless catalogAF is empty. The page has /AF [4 0 R].
func associatedFilesTestPDF(version, names, catalogAF string) []byte {
	af := ""
	if catalogAF != "" {
		af = " /AF " + catalogAF
	}
	pdf := failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R /Names << /EmbeddedFiles << /Names "+names+" >> >>"+af+" >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> /AF [4 0 R] >>",
		"<< /Type /Filespec /F (factur-x.xml) /AFRelationship /Data /EF << /F 5 0 R >> >>",
		fmt.Sprintf("<< /Type /EmbeddedFile /Subtype /text#2Fxml /Length %d >>\nstream\n%s\nendstream", len(associatedFilesInvoiceXML), associatedFilesInvoiceXML),
	)
	// The header version has the same length in all versions used here, so the xref offsets stay valid.
	return append([]byte("%PDF-"+version), pdf[len("%PDF-1.7"):]...)
}

func strictTestConfiguration() *model.Configuration {
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationStrict
	return conf
}

// requireSingleNotice returns the only notice of report and checks its disposition.
func requireSingleNotice(t *testing.T, report model.ValidationReport, disposition model.NoticeDisposition) model.ValidationNotice {
	t.Helper()
	if report.Len() != 1 || report.Dropped() != 0 {
		t.Fatalf("got %d notices (%d dropped), want 1: %v", report.Len(), report.Dropped(), report.Notices())
	}
	notice, _ := report.First()
	if notice.Disposition != disposition || notice.Phase != model.NoticePhaseValidate {
		t.Fatalf("notice %+v: want disposition %s, phase validate", notice, disposition)
	}
	return notice
}

func TestReadAndValidateRelaxedAFRelationshipBeforePDF14(t *testing.T) {
	pdf := associatedFilesTestPDF("1.3", associatedFilesNames, "[4 0 R]")

	ctx, report, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(pdf), nil)
	if err != nil {
		t.Fatalf("relaxed read: %v", err)
	}
	if ctx == nil {
		t.Fatal("relaxed read: nil context")
	}
	notice := requireSingleNotice(t, report.Notices, model.NoticeDigested)
	if !strings.Contains(notice.Message, "AFRelationship") || !strings.Contains(notice.Message, "requires version 1.4") {
		t.Fatalf("notice message %q does not describe the AFRelationship version", notice.Message)
	}
	if notice.ObjectNumber != 4 {
		t.Fatalf("notice object number %d, want 4", notice.ObjectNumber)
	}
	if !notice.HasVersions || notice.ActualVersion != model.V13 || notice.RequiredVersion != model.V14 {
		t.Fatalf("notice versions: has=%v actual=%s required=%s", notice.HasVersions, notice.ActualVersion, notice.RequiredVersion)
	}
	var versionErr *model.VersionRequirementError
	if !errors.As(notice.Cause, &versionErr) {
		t.Fatalf("notice cause %v is not a VersionRequirementError", notice.Cause)
	}

	_, err = ReadAndValidate(t.Context(), bytes.NewReader(pdf), strictTestConfiguration())
	if !errors.Is(err, model.ErrVersionTooLow) {
		t.Fatalf("strict read: got %v, want ErrVersionTooLow", err)
	}
}

func TestReadAndValidateRelaxedAFRelationshipNoticeOncePerObject(t *testing.T) {
	// Two name tree entries share the file specification object 4.
	pdf := associatedFilesTestPDF("1.3", "[(a.xml) 4 0 R (b.xml) 4 0 R]", "")

	_, report, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(pdf), nil)
	if err != nil {
		t.Fatalf("relaxed read: %v", err)
	}
	requireSingleNotice(t, report.Notices, model.NoticeDigested)
}

func TestReadAndValidateRelaxedAFRelationshipSincePDF14Unchanged(t *testing.T) {
	pdf := associatedFilesTestPDF("1.4", associatedFilesNames, "")

	_, report, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(pdf), nil)
	if err != nil {
		t.Fatalf("relaxed read: %v", err)
	}
	if !report.Notices.Empty() {
		t.Fatalf("relaxed read of PDF 1.4: got notices %v, want none", report.Notices.Notices())
	}
}

func TestReadAndValidateRelaxedCatalogAFPDF20(t *testing.T) {
	pdf := associatedFilesTestPDF("2.0", associatedFilesNames, "[4 0 R]")

	ctx, report, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(pdf), nil)
	if err != nil {
		t.Fatalf("relaxed read: %v", err)
	}
	notice := requireSingleNotice(t, report.Notices, model.NoticeDigested)
	if notice.Path != "AF" || !strings.Contains(notice.Message, "PDF 2.0 associated files not supported") {
		t.Fatalf("notice %q (path %q) does not describe the catalog AF", notice.Message, notice.Path)
	}
	if notice.ObjectNumber != 1 {
		t.Fatalf("notice object number %d, want 1", notice.ObjectNumber)
	}
	catalog, err := ctx.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if _, found := catalog.Find("AF"); !found {
		t.Fatal("relaxed read removed the catalog AF entry, want it kept")
	}

	_, err = ReadAndValidate(t.Context(), bytes.NewReader(pdf), strictTestConfiguration())
	if err == nil || !strings.Contains(err.Error(), "PDF 2.0 associated files not supported") {
		t.Fatalf("strict read: got %v, want the associated files error", err)
	}
}

func TestReadAndValidateRelaxedCatalogAFEntries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		catalogAF string
		ok        bool
	}{
		{"file spec and missing object", "[4 0 R 9 0 R]", true},
		{"file spec and null", "[4 0 R null]", true},
		{"integer entry", "[4 0 R 7]", false},
		{"string entry", "[(factur-x.xml)]", false},
		{"pages dict entry", "[2 0 R]", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pdf := associatedFilesTestPDF("2.0", associatedFilesNames, tc.catalogAF)
			_, _, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(pdf), nil)
			if tc.ok && err != nil {
				t.Fatalf("relaxed read of AF %s: %v", tc.catalogAF, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("relaxed read of AF %s: want an error", tc.catalogAF)
			}
		})
	}
}

// requireAssociatedFiles re-reads pdf in relaxed mode and checks that it has
// pages pages, that no /AF entry of the catalog or of a page dangles, and,
// if embedded, that the embedded invoice survived.
func requireAssociatedFiles(t *testing.T, pdf []byte, pages int, embedded bool) {
	t.Helper()
	ctx, _, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(pdf), nil)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if ctx.PageCount != pages {
		t.Fatalf("page count %d, want %d", ctx.PageCount, pages)
	}

	requireAFEntries := func(where string, d types.Dict) {
		t.Helper()
		af, found := d.Find("AF")
		if !found {
			return
		}
		entries, err := ctx.DereferenceArray(af)
		if err != nil {
			t.Fatalf("%s: AF: %v", where, err)
		}
		for i, o := range entries {
			fs, err := ctx.DereferenceDict(o)
			if err != nil || fs == nil {
				t.Fatalf("%s: AF[%d]: dangling or not a dict: %v", where, i, err)
			}
			ef, err := ctx.DereferenceDict(fs["EF"])
			if err != nil || ef == nil {
				t.Fatalf("%s: AF[%d]: EF dangling: %v", where, i, err)
			}
		}
	}

	catalog, err := ctx.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	requireAFEntries("catalog", catalog)
	for i := 1; i <= ctx.PageCount; i++ {
		page, _, _, err := ctx.PageDict(t.Context(), i, false)
		if err != nil {
			t.Fatalf("page %d: %v", i, err)
		}
		requireAFEntries(fmt.Sprintf("page %d", i), page)
	}

	if !embedded {
		return
	}
	attachments, err := ctx.ExtractAttachments(t.Context(), nil)
	if err != nil {
		t.Fatalf("extract attachments: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("got %d attachments, want 1", len(attachments))
	}
	content, err := io.ReadAll(attachments[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != associatedFilesInvoiceXML {
		t.Fatalf("attachment content %q, want %q", content, associatedFilesInvoiceXML)
	}
}

func TestAssociatedFilesRelaxedWriteExtractMerge(t *testing.T) {
	for _, tc := range []struct {
		name string
		pdf  []byte
	}{
		{"AFRelationship before 1.4", associatedFilesTestPDF("1.3", associatedFilesNames, "[4 0 R]")},
		{"catalog AF in 2.0", associatedFilesTestPDF("2.0", associatedFilesNames, "[4 0 R]")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(tc.pdf), nil)
			if err != nil {
				t.Fatalf("relaxed read: %v", err)
			}

			var out bytes.Buffer
			if err := WriteContext(t.Context(), ctx, &out); err != nil {
				t.Fatalf("write: %v", err)
			}
			requireAssociatedFiles(t, out.Bytes(), 1, true)

			var pages [][]byte
			err = ExtractPagesContext(t.Context(), ctx, nil, func(r io.Reader, _ int) error {
				b, err := io.ReadAll(r)
				if err != nil {
					return err
				}
				pages = append(pages, b)
				return nil
			})
			if err != nil {
				t.Fatalf("extract pages: %v", err)
			}
			if len(pages) != 1 {
				t.Fatalf("extracted %d pages, want 1", len(pages))
			}
			requireAssociatedFiles(t, pages[0], 1, false)

			small := mergeTestPDF(1, false)
			for _, inputs := range [][][]byte{{tc.pdf, small}, {small, tc.pdf}} {
				merged := &bytes.Buffer{}
				_, err := MergeRawWithReport(t.Context(), mergeTestReaders(inputs...), merged, nil, MergeOptions{LowerPDF20: true})
				if err != nil {
					t.Fatalf("merge: %v", err)
				}
				requireAssociatedFiles(t, merged.Bytes(), 2, true)
			}
		})
	}
}

func TestWriteKeepsPageAF(t *testing.T) {
	// The file specification is referenced by the page /AF only, not by the name tree.
	pdf := failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> /AF [4 0 R] >>",
		"<< /Type /Filespec /F (factur-x.xml) /EF << /F 5 0 R >> >>",
		fmt.Sprintf("<< /Type /EmbeddedFile /Subtype /text#2Fxml /Length %d >>\nstream\n%s\nendstream", len(associatedFilesInvoiceXML), associatedFilesInvoiceXML),
	)

	ctx, err := ReadAndValidate(t.Context(), bytes.NewReader(pdf), strictTestConfiguration())
	if err != nil {
		t.Fatalf("strict read: %v", err)
	}
	var out bytes.Buffer
	if err := WriteContext(t.Context(), ctx, &out); err != nil {
		t.Fatalf("write: %v", err)
	}
	requireAssociatedFiles(t, out.Bytes(), 1, false)

	err = ExtractPagesContext(t.Context(), ctx, nil, func(r io.Reader, _ int) error {
		b, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		requireAssociatedFiles(t, b, 1, false)
		return nil
	})
	if err != nil {
		t.Fatalf("extract pages: %v", err)
	}
}

// pageEntriesTestPDF has two pages. Page 1 has /OutputIntents, /DPart and the developer
// key /Foo, which refer to objects the page writer does not list. Each object has a /Tag.
// Page 2 is tagged p2, so a copy of it in the output of page 1 is detected.
func pageEntriesTestPDF() []byte {
	return failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 9 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> /AF [4 0 R] /OutputIntents 6 0 R /DPart 7 0 R /Foo 8 0 R >>",
		"<< /Type /Filespec /F (factur-x.xml) /EF << /F 5 0 R >> >>",
		fmt.Sprintf("<< /Type /EmbeddedFile /Subtype /text#2Fxml /Length %d >>\nstream\n%s\nendstream", len(associatedFilesInvoiceXML), associatedFilesInvoiceXML),
		"<< /Type /OutputIntent /S /GTS_PDFA1 /Tag (oi) >>",
		"<< /Type /DPart /Tag (dpart) >>",
		"<< /Tag (foo) >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> /Tag (p2) >>",
	)
}

// requirePageEntries checks that the page pageNr of pdf has its unlisted entries and
// that each one resolves to the object written for it.
func requirePageEntries(t *testing.T, pdf []byte, pageNr int) {
	t.Helper()
	ctx, _, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(pdf), nil)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	page, _, _, err := ctx.PageDict(t.Context(), pageNr, false)
	if err != nil {
		t.Fatalf("page %d: %v", pageNr, err)
	}
	for _, e := range []struct{ key, tag string }{{"OutputIntents", "oi"}, {"DPart", "dpart"}, {"Foo", "foo"}} {
		d, err := ctx.DereferenceDict(page[e.key])
		if err != nil || d == nil {
			t.Fatalf("page %d: %s dangling: %v", pageNr, e.key, err)
		}
		if tag := d.StringEntry("Tag"); tag == nil || *tag != e.tag {
			t.Fatalf("page %d: %s tag %v, want %s", pageNr, e.key, tag, e.tag)
		}
	}
}

// requireNoPage2 checks that the page tagged p2 is not in pdf, so that page 1 was
// written without the rest of the page tree.
func requireNoPage2(t *testing.T, pdf []byte) {
	t.Helper()
	ctx, _, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(pdf), nil)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	for objNr, entry := range ctx.Table {
		if entry == nil || entry.Free {
			continue
		}
		o, err := ctx.Dereference(types.IndirectRef{ObjectNumber: types.Integer(objNr), GenerationNumber: 0})
		if err != nil {
			t.Fatalf("object %d: %v", objNr, err)
		}
		d, _ := o.(types.Dict)
		if sd, ok := o.(types.StreamDict); ok {
			d = sd.Dict
		}
		if tag := d.StringEntry("Tag"); tag != nil && *tag == "p2" {
			t.Fatalf("object %d: page 2 written into the output of page 1", objNr)
		}
	}
}

func TestWriteKeepsPageEntries(t *testing.T) {
	pdf := pageEntriesTestPDF()

	ctx, err := ReadAndValidate(t.Context(), bytes.NewReader(pdf), strictTestConfiguration())
	if err != nil {
		t.Fatalf("strict read: %v", err)
	}
	var out bytes.Buffer
	if err := WriteContext(t.Context(), ctx, &out); err != nil {
		t.Fatalf("write: %v", err)
	}
	requireAssociatedFiles(t, out.Bytes(), 2, false)
	requirePageEntries(t, out.Bytes(), 1)

	// Without object streams the page tree node is plain text. Walking /Parent while the
	// page is written writes that node a second time, with the kids of the input.
	plain := strictTestConfiguration()
	plain.WriteObjectStream = false
	plain.WriteXRefStream = false
	plainCtx, err := ReadAndValidate(t.Context(), bytes.NewReader(pdf), plain)
	if err != nil {
		t.Fatalf("strict read, plain: %v", err)
	}
	var plainOut bytes.Buffer
	if err := WriteContext(t.Context(), plainCtx, &plainOut); err != nil {
		t.Fatalf("write, plain: %v", err)
	}
	if n := bytes.Count(plainOut.Bytes(), []byte("/Type/Pages")); n != 1 {
		t.Fatalf("page tree node written %d times, want 1", n)
	}
	requirePageEntries(t, plainOut.Bytes(), 1)

	var pages [][]byte
	err = ExtractPagesContext(t.Context(), ctx, []string{"1"}, func(r io.Reader, _ int) error {
		b, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		pages = append(pages, b)
		return nil
	})
	if err != nil {
		t.Fatalf("extract pages: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("extracted %d pages, want 1", len(pages))
	}
	requireAssociatedFiles(t, pages[0], 1, false)
	requirePageEntries(t, pages[0], 1)
	requireNoPage2(t, pages[0])

	dir := t.TempDir()
	if err := Split(t.Context(), bytes.NewReader(pdf), dir, "split", 1, strictTestConfiguration()); err != nil {
		t.Fatalf("split: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("split wrote %d files, want 2", len(files))
	}
	sort.Strings(files)
	split, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	requireAssociatedFiles(t, split, 1, false)
	requirePageEntries(t, split, 1)
	requireNoPage2(t, split)

	var trimmed bytes.Buffer
	if err := Trim(t.Context(), bytes.NewReader(pdf), &trimmed, []string{"1"}, strictTestConfiguration()); err != nil {
		t.Fatalf("trim: %v", err)
	}
	requireAssociatedFiles(t, trimmed.Bytes(), 1, false)
	requirePageEntries(t, trimmed.Bytes(), 1)
	requireNoPage2(t, trimmed.Bytes())

	small := mergeTestPDF(1, false)
	for _, tc := range []struct {
		inputs [][]byte
		pageNr int
	}{
		{[][]byte{pdf, small}, 1},
		{[][]byte{small, pdf}, 2},
	} {
		merged := &bytes.Buffer{}
		if _, err := MergeRawWithReport(t.Context(), mergeTestReaders(tc.inputs...), merged, nil, MergeOptions{LowerPDF20: true}); err != nil {
			t.Fatalf("merge: %v", err)
		}
		requireAssociatedFiles(t, merged.Bytes(), 3, false)
		requirePageEntries(t, merged.Bytes(), tc.pageNr)
	}
}

// pageTreeNodeTestPDF has a nested page tree: the root node 2 has kids 3 (a node with
// /Parent 2 0 R) and 8, and node 3 has kid 4, page 1. The entries rootExtra, innerExtra
// and pageExtra are added to nodes 2 and 3 and to page 1. Object 5 is tagged foo, 6 is
// a DPart whose /Start and /End refer to page tree nodes and pages, 7 is tagged oi, and
// page 8 is tagged p2.
func pageTreeNodeTestPDF(rootExtra, innerExtra, pageExtra string) []byte {
	return failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 8 0 R] /Count 2 "+rootExtra+" >>",
		"<< /Type /Pages /Parent 2 0 R /Kids [4 0 R] /Count 1 "+innerExtra+" >>",
		"<< /Type /Page /Parent 3 0 R /MediaBox [0 0 595 842] /Resources << >> "+pageExtra+" >>",
		"<< /Tag (foo) >>",
		"<< /Type /DPart /Tag (dpart) /Start 2 0 R /End 8 0 R >>",
		"<< /Tag (oi) >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> /Tag (p2) >>",
	)
}

// writeTestPDF reads pdf with conf and writes it again.
func writeTestPDF(t *testing.T, pdf []byte, conf *model.Configuration) []byte {
	t.Helper()
	ctx, err := ReadAndValidate(t.Context(), bytes.NewReader(pdf), conf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out bytes.Buffer
	if err := WriteContext(t.Context(), ctx, &out); err != nil {
		t.Fatalf("write: %v", err)
	}
	return out.Bytes()
}

// requirePageTreeNodeOnce checks that out has nodes page tree nodes, each written once,
// and that it holds pages pages.
func requirePageTreeNodeOnce(t *testing.T, out []byte, nodes, pages int) {
	t.Helper()
	if n := bytes.Count(out, []byte("/Type/Pages")); n != nodes {
		t.Fatalf("page tree nodes written %d times, want %d", n, nodes)
	}
	ctx, _, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(out), nil)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if ctx.PageCount != pages {
		t.Fatalf("page count %d, want %d", ctx.PageCount, pages)
	}
}

func tagString(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// TestWritePageTreeNodeWrittenOnce checks that a page tree node that an unlisted page entry
// refers to is written once, with the kids of the output, by a plain write and by Trim.
func TestWritePageTreeNodeWrittenOnce(t *testing.T) {
	for _, e := range []struct {
		name      string
		pageExtra string
	}{
		{"developer key to another page", "/Foo 8 0 R"},
		{"developer key to the root node", "/Foo 2 0 R"},
		{"developer key to the inner node", "/Foo 3 0 R"},
		{"DPart with Start and End", "/DPart 6 0 R"},
		{"PrivPages to the root node", "/PrivPages 2 0 R"},
	} {
		t.Run(e.name, func(t *testing.T) {
			pdf := pageTreeNodeTestPDF("", "", e.pageExtra)
			plain := strictTestConfiguration()
			plain.WriteObjectStream = false
			plain.WriteXRefStream = false

			out := writeTestPDF(t, pdf, plain)
			requirePageTreeNodeOnce(t, out, 2, 2)

			var trimmed bytes.Buffer
			if err := Trim(t.Context(), bytes.NewReader(pdf), &trimmed, []string{"1"}, plain); err != nil {
				t.Fatalf("trim: %v", err)
			}
			requirePageTreeNodeOnce(t, trimmed.Bytes(), 1, 1)
		})
	}

	// A zip merge keeps the page tree of the output. The renumbered input root and inner node
	// are not part of it, so they must not be written with the kids of the input.
	t.Run("zip merge with a page entry to the input root", func(t *testing.T) {
		pdf := pageTreeNodeTestPDF("", "", "/Foo 2 0 R")
		plain := strictTestConfiguration()
		plain.WriteObjectStream = false
		plain.WriteXRefStream = false

		var zipped bytes.Buffer
		if err := MergeCreateZip(t.Context(), bytes.NewReader(pdf), bytes.NewReader(pdf), &zipped, plain); err != nil {
			t.Fatalf("zip merge: %v", err)
		}
		requirePageTreeNodeOnce(t, zipped.Bytes(), 2, 4)
	})
}

// orphanPagesTestPDF returns a PDF with one page and object 4, a /Pages dict that is not part of
// the page tree, tagged orphan. catalogExtra and pageExtra are added to the catalog and the page.
func orphanPagesTestPDF(catalogExtra, pageExtra string) []byte {
	return failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R "+catalogExtra+" >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> "+pageExtra+" >>",
		"<< /Type /Pages /Kids [] /Count 0 /Tag (orphan) >>",
	)
}

// TestWriteOrphanPagesDictKept checks that a /Pages dict that is referenced from the catalog or
// from a page, but is not a node of the page tree, is written, so that the reference resolves.
func TestWriteOrphanPagesDictKept(t *testing.T) {
	for _, e := range []struct {
		name         string
		catalogExtra string
		pageExtra    string
	}{
		{"from the catalog", "/OldPages 4 0 R", ""},
		{"from a page", "", "/OldPages 4 0 R"},
	} {
		t.Run(e.name, func(t *testing.T) {
			pdf := orphanPagesTestPDF(e.catalogExtra, e.pageExtra)
			plain := strictTestConfiguration()
			plain.WriteObjectStream = false
			plain.WriteXRefStream = false

			out := writeTestPDF(t, pdf, plain)
			requirePageTreeNodeOnce(t, out, 2, 1)
			if !bytes.Contains(out, []byte("(orphan)")) {
				t.Fatal("orphan /Pages dict not written")
			}

			if e.pageExtra != "" {
				var trimmed bytes.Buffer
				if err := Trim(t.Context(), bytes.NewReader(pdf), &trimmed, []string{"1"}, plain); err != nil {
					t.Fatalf("trim: %v", err)
				}
				requirePageTreeNodeOnce(t, trimmed.Bytes(), 2, 1)
			}
		})
	}
}

// TestAddPagesOrphanPagesDictKept checks that extraction keeps a /Pages dict that a page refers to
// when the dict is not a node of the input page tree, even though its /Parent names the root.
func TestAddPagesOrphanPagesDictKept(t *testing.T) {
	pdf := failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> /OldPages 4 0 R >>",
		"<< /Type /Pages /Parent 2 0 R /Kids [] /Count 0 /Tag (orphan) >>",
	)
	plain := strictTestConfiguration()
	plain.WriteObjectStream = false
	plain.WriteXRefStream = false

	var trimmed bytes.Buffer
	if err := Trim(t.Context(), bytes.NewReader(pdf), &trimmed, []string{"1"}, plain); err != nil {
		t.Fatalf("trim: %v", err)
	}
	requirePageTreeNodeOnce(t, trimmed.Bytes(), 2, 1)
	if !bytes.Contains(trimmed.Bytes(), []byte("(orphan)")) {
		t.Fatal("orphan /Pages dict not written")
	}
}

// TestWritePageTreeNodeKeepsRemainingEntries checks that an entry the page tree node writer
// does not list is written for the root and the inner node, and that each resolves to its object.
func TestWritePageTreeNodeKeepsRemainingEntries(t *testing.T) {
	pdf := pageTreeNodeTestPDF("/Foo 5 0 R", "/Foo 7 0 R", "")
	plain := strictTestConfiguration()
	plain.WriteObjectStream = false
	plain.WriteXRefStream = false
	for _, conf := range []*model.Configuration{strictTestConfiguration(), plain} {
		out := writeTestPDF(t, pdf, conf)
		ctx, _, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(out), nil)
		if err != nil {
			t.Fatalf("re-read: %v", err)
		}
		root, err := ctx.DereferenceDict(*ctx.XRefTable.Root)
		if err != nil {
			t.Fatal(err)
		}
		pages, err := ctx.DereferenceDict(root["Pages"])
		if err != nil {
			t.Fatal(err)
		}
		inner, err := ctx.DereferenceDict(pages.ArrayEntry("Kids")[0])
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range []struct {
			node types.Dict
			tag  string
		}{{pages, "foo"}, {inner, "oi"}} {
			d, err := ctx.DereferenceDict(e.node["Foo"])
			if err != nil || d == nil {
				t.Fatalf("Foo of page tree node dangling: %v", err)
			}
			if tag := d.StringEntry("Tag"); tag == nil || *tag != e.tag {
				t.Fatalf("Foo of page tree node tag %q, want %s", tagString(tag), e.tag)
			}
		}
	}
}

// TestWriteRemainingPageEntriesInKeyOrder checks that the unlisted page entries are written in
// ascending key order, so repeated writes produce the same object order.
func TestWriteRemainingPageEntriesInKeyOrder(t *testing.T) {
	pdf := pageTreeNodeTestPDF("", "", "/OutputIntents 7 0 R /DPart 6 0 R /Foo 5 0 R")
	plain := strictTestConfiguration()
	plain.WriteObjectStream = false
	plain.WriteXRefStream = false
	for i := 0; i < 20; i++ {
		out := writeTestPDF(t, pdf, plain)
		a := bytes.Index(out, []byte("(foo)"))
		b := bytes.Index(out, []byte("(dpart)"))
		c := bytes.Index(out, []byte("(oi)"))
		if a < 0 || b < 0 || c < 0 || !(b < a && a < c) {
			t.Fatalf("write %d: object order dpart=%d foo=%d oi=%d, want dpart, foo, oi", i, b, a, c)
		}
	}
}

// flatPageTreePDF returns a PDF with n pages that are all kids of the root page tree node.
func flatPageTreePDF(n int) []byte {
	kids := make([]string, n)
	objs := make([]string, 0, n+2)
	for i := range kids {
		kids[i] = fmt.Sprintf("%d 0 R", i+3)
	}
	objs = append(objs,
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n))
	for i := 0; i < n; i++ {
		objs = append(objs, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> >>")
	}
	return failureTestPDF(objs...)
}

// TestAddPagesWalksInputPageTreeOnce checks that AddPages walks the page tree of the source once,
// not once per call: the first call builds the page tree index and every later call reuses it.
// The cost of that one walk is checked in pkg/pdfcpu (TestAddPageTreeNodeObjNrsVisitsEachDictOnce).
func TestAddPagesWalksInputPageTreeOnce(t *testing.T) {
	const pages, calls = 4000, 300
	conf := strictTestConfiguration()
	src, err := ReadAndValidate(t.Context(), bytes.NewReader(flatPageTreePDF(pages)), conf)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	dest, err := ReadAndValidate(t.Context(), bytes.NewReader(flatPageTreePDF(1)), conf)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	var index *model.PageTreeIndex
	for i := 0; i < calls; i++ {
		if err := pdfcpu.AddPages(t.Context(), src, dest, []int{1}, false); err != nil {
			t.Fatalf("add page 1, call %d: %v", i, err)
		}
		if i == 0 {
			if src.InputPageTree == nil {
				t.Fatal("no page tree index after AddPages")
			}
			index = src.InputPageTree
		} else if src.InputPageTree != index {
			t.Fatalf("page tree index rebuilt by call %d, want the index of the first call", i+1)
		}
	}
}

// TestAddPagesMalformedParentNodeNotCopied checks that a node that Kids reach is the input page tree
// node whatever its /Parent says. The inner node names a missing /Parent, so its stale copy must
// not be written. The /Foo reference of the kept page dangles in the output, like a reference
// to a missing object in the input.
func TestAddPagesMalformedParentNodeNotCopied(t *testing.T) {
	pdf := failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 2 >>",
		"<< /Type /Pages /Parent 8 0 R /Kids [4 0 R 5 0 R] /Count 2 /Tag (inner) >>",
		"<< /Type /Page /Parent 3 0 R /MediaBox [0 0 595 842] /Resources << >> /Foo 3 0 R >>",
		"<< /Type /Page /Parent 3 0 R /MediaBox [0 0 595 842] /Resources << >> >>",
	)
	plain := model.NewDefaultConfiguration()
	plain.ValidationMode = model.ValidationRelaxed
	plain.WriteObjectStream = false
	plain.WriteXRefStream = false

	var trimmed bytes.Buffer
	if err := Trim(t.Context(), bytes.NewReader(pdf), &trimmed, []string{"1"}, plain); err != nil {
		t.Fatalf("trim: %v", err)
	}
	requirePageTreeNodeOnce(t, trimmed.Bytes(), 1, 1)
	if bytes.Contains(trimmed.Bytes(), []byte("(inner)")) {
		t.Fatal("stale copy of the input page tree node written")
	}
}

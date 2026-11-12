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
	"io"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// catalogNamesTestPDF builds a PDF 1.7 with one page whose catalog has the extra entries catalog.
func catalogNamesTestPDF(catalog string) []byte {
	return failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R "+catalog+" >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> >>",
	)
}

// catalogNameCases are presentational catalog and viewer preferences name entries
// with a value the specification does not define.
var catalogNameCases = []struct {
	name, catalog, path, entry, value string
	viewerPreferences                 bool
}{
	{"PageMode", "/PageMode /UseAtachments", "PageMode", "PageMode", "UseAtachments", false},
	{"PageLayout", "/PageLayout /TwoPages", "PageLayout", "PageLayout", "TwoPages", false},
	{"NonFullScreenPageMode", "/ViewerPreferences << /NonFullScreenPageMode /UseAtachments >>", "ViewerPreferences.NonFullScreenPageMode", "NonFullScreenPageMode", "UseAtachments", true},
	{"Direction", "/ViewerPreferences << /Direction /RTL >>", "ViewerPreferences.Direction", "Direction", "RTL", true},
	{"ViewArea", "/ViewerPreferences << /ViewArea /PageBox >>", "ViewerPreferences.ViewArea", "ViewArea", "PageBox", true},
	{"ViewClip", "/ViewerPreferences << /ViewClip /PageBox >>", "ViewerPreferences.ViewClip", "ViewClip", "PageBox", true},
	{"PrintArea", "/ViewerPreferences << /PrintArea /PageBox >>", "ViewerPreferences.PrintArea", "PrintArea", "PageBox", true},
	{"PrintClip", "/ViewerPreferences << /PrintClip /PageBox >>", "ViewerPreferences.PrintClip", "PrintClip", "PageBox", true},
	{"Duplex", "/ViewerPreferences << /Duplex /Duplex >>", "ViewerPreferences.Duplex", "Duplex", "Duplex", true},
}

// catalogNameEntry returns the entry of the catalog, or of its viewer preferences, of ctx.
func catalogNameEntry(t *testing.T, ctx *model.Context, entry string, viewerPreferences bool) (types.Object, bool) {
	t.Helper()
	d, err := ctx.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if viewerPreferences {
		if d, err = ctx.DereferenceDict(d["ViewerPreferences"]); err != nil || d == nil {
			t.Fatalf("viewer preferences: %v", err)
		}
	}
	return d.Find(entry)
}

// requireCatalogNameValue checks that the entry of ctx is the name value.
func requireCatalogNameValue(t *testing.T, ctx *model.Context, entry, value string, viewerPreferences bool) {
	t.Helper()
	o, found := catalogNameEntry(t, ctx, entry, viewerPreferences)
	if !found {
		t.Fatalf("%s removed, want it kept", entry)
	}
	n, ok := o.(types.Name)
	if !ok || n.Value() != value {
		t.Fatalf("%s is %v, want /%s", entry, o, value)
	}
}

func TestReadAndValidateRelaxedUnknownNameKept(t *testing.T) {
	for _, tc := range catalogNameCases {
		t.Run(tc.name, func(t *testing.T) {
			pdf := catalogNamesTestPDF(tc.catalog)

			ctx, report, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(pdf), nil)
			if err != nil {
				t.Fatalf("relaxed read: %v", err)
			}
			notice := requireSingleNotice(t, report.Notices, model.NoticeDigested)
			if notice.Path != tc.path {
				t.Fatalf("notice path %q, want %q", notice.Path, tc.path)
			}
			if !strings.Contains(notice.Message, "entry="+tc.entry+" invalid dict entry") {
				t.Fatalf("notice message %q does not name entry %s", notice.Message, tc.entry)
			}
			if notice.ObjectNumber != 1 {
				t.Fatalf("notice object number %d, want 1", notice.ObjectNumber)
			}
			requireCatalogNameValue(t, ctx, tc.entry, tc.value, tc.viewerPreferences)

			var vErr *model.ValidationError
			_, _, err = ReadAndValidateWithReport(t.Context(), bytes.NewReader(pdf), strictTestConfiguration())
			if !errors.As(err, &vErr) {
				t.Fatalf("strict read: got %v, want a ValidationError", err)
			}

			// The written file keeps the value. Strict mode still rejects it, and a
			// relaxed read of the written file reports the same notice again.
			var out bytes.Buffer
			if err := WriteContext(t.Context(), ctx, &out); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, _, err = ReadAndValidateWithReport(t.Context(), bytes.NewReader(out.Bytes()), strictTestConfiguration())
			if !errors.As(err, &vErr) {
				t.Fatalf("strict read of written output: got %v, want a ValidationError", err)
			}
			written, wReport, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(out.Bytes()), nil)
			if err != nil {
				t.Fatalf("relaxed read of written output: %v", err)
			}
			requireSingleNotice(t, wReport.Notices, model.NoticeDigested)
			requireCatalogNameValue(t, written, tc.entry, tc.value, tc.viewerPreferences)

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
			// Extract builds a new catalog without these entries, and merge keeps the
			// catalog of the first input only. That is the same for valid values, so
			// only the relaxed read of the extracted page is checked here.
			if _, _, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(pages[0]), nil); err != nil {
				t.Fatalf("relaxed read of extracted page: %v", err)
			}

			small := mergeTestPDF(1, false)
			for i, inputs := range [][][]byte{{pdf, small}, {small, pdf}} {
				merged := &bytes.Buffer{}
				report, err := MergeRawWithReport(t.Context(), mergeTestReaders(inputs...), merged, nil, MergeOptions{})
				if err != nil {
					t.Fatalf("merge: %v", err)
				}
				notices := 0
				for _, in := range report.Inputs {
					notices += in.Notices.Len()
				}
				if notices != 1 {
					t.Fatalf("merge: %d input notices, want 1", notices)
				}
				mergedCtx, _, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(merged.Bytes()), nil)
				if err != nil {
					t.Fatalf("relaxed read of merged output: %v", err)
				}
				if i == 0 {
					requireCatalogNameValue(t, mergedCtx, tc.entry, tc.value, tc.viewerPreferences)
				}
			}
		})
	}
}

func TestReadAndValidateRelaxedUnknownPageModeIsUseNone(t *testing.T) {
	ctx, _, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(catalogNamesTestPDF("/PageMode /UseAtachments")), nil)
	if err != nil {
		t.Fatalf("relaxed read: %v", err)
	}
	if ctx.PageMode == nil || *ctx.PageMode != model.PageModeUseNone {
		t.Fatalf("page mode %v, want UseNone", ctx.PageMode)
	}

	ctx, _, err = ReadAndValidateWithReport(t.Context(), bytes.NewReader(catalogNamesTestPDF("/PageLayout /TwoPages")), nil)
	if err != nil {
		t.Fatalf("relaxed read: %v", err)
	}
	if ctx.PageLayout != nil {
		t.Fatalf("page layout %v, want absent", ctx.PageLayout)
	}

	// Known values too new for the declared version are kept with a notice, but
	// not applied in memory, like unknown values.
	for _, tc := range []struct {
		catalog, version, entry, value string
	}{
		{"/PageMode /UseOC", "1.3", "PageMode", "UseOC"},
		{"/PageLayout /TwoPageLeft", "1.4", "PageLayout", "TwoPageLeft"},
	} {
		pdf := bytes.Replace(catalogNamesTestPDF(tc.catalog), []byte("%PDF-1.7"), []byte("%PDF-"+tc.version), 1)
		ctx, report, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(pdf), nil)
		if err != nil {
			t.Fatalf("relaxed read of %s in PDF %s: %v", tc.catalog, tc.version, err)
		}
		requireSingleNotice(t, report.Notices, model.NoticeDigested)
		requireCatalogNameValue(t, ctx, tc.entry, tc.value, false)
		if tc.entry == "PageMode" && (ctx.PageMode == nil || *ctx.PageMode != model.PageModeUseNone) {
			t.Fatalf("%s in PDF %s: page mode %v, want UseNone", tc.catalog, tc.version, ctx.PageMode)
		}
		if tc.entry == "PageLayout" && ctx.PageLayout != nil {
			t.Fatalf("%s in PDF %s: page layout %v, want absent", tc.catalog, tc.version, ctx.PageLayout)
		}
	}
}

// TestReadAndValidateRelaxedViewerPreferenceTooNewKept covers viewer preference
// names that the declared version does not allow yet, such as /ViewArea in a PDF 1.3 file.
func TestReadAndValidateRelaxedViewerPreferenceTooNewKept(t *testing.T) {
	for _, tc := range []struct {
		catalog, version, entry, value string
	}{
		{"/ViewerPreferences << /ViewArea /PageBox >>", "1.3", "ViewArea", "PageBox"},
		{"/ViewerPreferences << /PrintClip /PageBox >>", "1.3", "PrintClip", "PageBox"},
		{"/ViewerPreferences << /Duplex /Duplex >>", "1.3", "Duplex", "Duplex"},
		{"/ViewerPreferences << /Direction /RTL >>", "1.2", "Direction", "RTL"},
	} {
		t.Run(tc.version+"/"+tc.entry, func(t *testing.T) {
			pdf := bytes.Replace(catalogNamesTestPDF(tc.catalog), []byte("%PDF-1.7"), []byte("%PDF-"+tc.version), 1)

			ctx, report, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(pdf), nil)
			if err != nil {
				t.Fatalf("relaxed read: %v", err)
			}
			notice := requireSingleNotice(t, report.Notices, model.NoticeDigested)
			if notice.Path != "ViewerPreferences."+tc.entry {
				t.Fatalf("notice path %q, want ViewerPreferences.%s", notice.Path, tc.entry)
			}
			if !strings.Contains(notice.Message, "entry="+tc.entry+": unsupported in version "+tc.version) {
				t.Fatalf("notice message %q does not report the version", notice.Message)
			}
			requireCatalogNameValue(t, ctx, tc.entry, tc.value, true)

			var vErr *model.ValidationError
			_, _, err = ReadAndValidateWithReport(t.Context(), bytes.NewReader(pdf), strictTestConfiguration())
			if !errors.As(err, &vErr) {
				t.Fatalf("strict read: got %v, want a ValidationError", err)
			}

			var out bytes.Buffer
			if err := WriteContext(t.Context(), ctx, &out); err != nil {
				t.Fatalf("write: %v", err)
			}
			written, wReport, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(out.Bytes()), nil)
			if err != nil {
				t.Fatalf("relaxed read of written output: %v", err)
			}
			requireSingleNotice(t, wReport.Notices, model.NoticeDigested)
			requireCatalogNameValue(t, written, tc.entry, tc.value, true)
		})
	}
}

func TestReadAndValidateRelaxedCatalogNamesUnchanged(t *testing.T) {
	// Accepted values stay as they are and produce no notice.
	for _, tc := range []struct {
		name, catalog, entry string
		viewerPreferences    bool
	}{
		{"PageMode None digest", "/PageMode /None", "PageMode", false},
		{"PageLayout Continuous", "/PageLayout /Continuous", "PageLayout", false},
		{"PrintScaling Fit", "/ViewerPreferences << /PrintScaling /Fit >>", "PrintScaling", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, report, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(catalogNamesTestPDF(tc.catalog)), nil)
			if err != nil {
				t.Fatalf("relaxed read: %v", err)
			}
			if !report.Notices.Empty() {
				t.Fatalf("relaxed read: notices %v, want none", report.Notices.Notices())
			}
			if _, found := catalogNameEntry(t, ctx, tc.entry, tc.viewerPreferences); !found {
				t.Fatalf("relaxed read removed %s, want it kept", tc.entry)
			}
		})
	}

	// Values of the wrong type still fail in relaxed mode.
	for _, catalog := range []string{
		"/PageMode (UseNone)",
		"/ViewerPreferences << /Duplex 2 >>",
	} {
		if _, _, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(catalogNamesTestPDF(catalog)), nil); err == nil {
			t.Fatalf("relaxed read of %s: want an error", catalog)
		}
	}
}

// TestReadAndValidateRelaxedPrintScalingBeforeVersionNotSet checks that a PrintScaling the
// header version does not allow stays in the dict, but the viewer preferences model does not
// take its value, since it did not validate.
func TestReadAndValidateRelaxedPrintScalingBeforeVersionNotSet(t *testing.T) {
	pdf := bytes.Replace(catalogNamesTestPDF("/ViewerPreferences << /PrintScaling /AppDefault >>"), []byte("%PDF-1.7"), []byte("%PDF-1.2"), 1)
	ctx, _, err := ReadAndValidateWithReport(t.Context(), bytes.NewReader(pdf), nil)
	if err != nil {
		t.Fatalf("relaxed read: %v", err)
	}
	if ctx.ViewerPref == nil {
		t.Fatal("viewer preferences not read")
	}
	if ctx.ViewerPref.PrintScaling != nil {
		t.Fatalf("PrintScaling set to %v before PDF 1.6 read with an error, want unset", *ctx.ViewerPref.PrintScaling)
	}
	requireCatalogNameValue(t, ctx, "PrintScaling", "AppDefault", true)
}
